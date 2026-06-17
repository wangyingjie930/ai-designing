package progresstracking

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// SQLiteStore 把任务计划和每个 checkpoint 持久化到 SQLite，支撑进程崩溃后的恢复。
type SQLiteStore struct {
	db   *sql.DB
	path string
}

// openSQLiteStore 打开 SQLite 数据库，并初始化 progress tracking 需要的表。
func openSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite db path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db, path: path}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// init 建表并建立索引；plan_id 让一个 SQLite 文件能保存多个业务计划。
func (s *SQLiteStore) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS progress_plans (
			plan_id TEXT PRIMARY KEY,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS progress_items (
			plan_id TEXT NOT NULL,
			item_index INTEGER NOT NULL,
			description TEXT NOT NULL,
			status TEXT NOT NULL,
			result TEXT,
			files_modified_json TEXT NOT NULL,
			error TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(plan_id, item_index),
			FOREIGN KEY(plan_id) REFERENCES progress_plans(plan_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_progress_items_plan_status ON progress_items(plan_id, status)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

// Close 关闭 SQLite 连接。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path 返回当前 SQLite 文件路径。
func (s *SQLiteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// loadItems 读取一个计划的全部任务，按 Python list index 的顺序恢复。
func (s *SQLiteStore) loadItems(ctx context.Context, planID string) ([]TaskItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sqlite store is not initialized")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		description, status, result, files_modified_json, error
		FROM progress_items
		WHERE plan_id = ?
		ORDER BY item_index ASC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []TaskItem
	for rows.Next() {
		var item TaskItem
		var status string
		var result sql.NullString
		var filesJSON string
		var itemError sql.NullString
		if err := rows.Scan(&item.Description, &status, &result, &filesJSON, &itemError); err != nil {
			return nil, err
		}
		item.Status = TaskStatus(status)
		if !item.Status.Valid() {
			return nil, fmt.Errorf("invalid task status %q in plan %s", status, planID)
		}
		if result.Valid {
			item.Result = stringPtr(result.String)
		}
		if err := json.Unmarshal([]byte(filesJSON), &item.FilesModified); err != nil {
			return nil, fmt.Errorf("decode files_modified for plan %s: %w", planID, err)
		}
		if itemError.Valid {
			item.Error = stringPtr(itemError.String)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// replacePlan 用一个事务替换计划内容，确保 create_plan 后不会留下半截任务。
func (s *SQLiteStore) replacePlan(ctx context.Context, planID string, items []TaskItem, now time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	timestamp := now.Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO progress_plans(plan_id, created_at, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(plan_id) DO UPDATE SET updated_at = excluded.updated_at`, planID, timestamp, timestamp); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM progress_items WHERE plan_id = ?`, planID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO progress_items(
		plan_id, item_index, description, status, result, files_modified_json, error, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for idx, item := range items {
		filesJSON, err := json.Marshal(item.FilesModified)
		if err != nil {
			return err
		}
		if _, err = stmt.ExecContext(ctx,
			planID,
			idx,
			item.Description,
			string(item.Status),
			nullableString(item.Result),
			string(filesJSON),
			nullableString(item.Error),
			timestamp,
		); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE progress_plans SET updated_at = ? WHERE plan_id = ?`, timestamp, planID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// updateItem 只更新一个任务 checkpoint，模拟 Python 每次 complete/fail 立即保存的语义。
func (s *SQLiteStore) updateItem(ctx context.Context, planID string, index int, item TaskItem, now time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not initialized")
	}
	filesJSON, err := json.Marshal(item.FilesModified)
	if err != nil {
		return err
	}
	timestamp := now.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	result, err := tx.ExecContext(ctx, `UPDATE progress_items SET
		status = ?,
		result = ?,
		files_modified_json = ?,
		error = ?,
		updated_at = ?
		WHERE plan_id = ? AND item_index = ?`,
		string(item.Status),
		nullableString(item.Result),
		string(filesJSON),
		nullableString(item.Error),
		timestamp,
		planID,
		index,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("task index %d not found in plan %s", index, planID)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE progress_plans SET updated_at = ? WHERE plan_id = ?`, timestamp, planID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// nullableString 把可空字符串指针转换成 database/sql 可识别的空值。
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
