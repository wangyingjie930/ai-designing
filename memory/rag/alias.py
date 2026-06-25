import argparse
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Optional

import yaml

if __package__ is None or __package__ == "":
    sys.path.append(str(Path(__file__).resolve().parents[2]))

from memory.rag.config import resolve_output_dir
from memory.rag.manifest import load_manifest, write_yaml


# ResolvedAlias 表示稳定 alias 当前直接指向的真实 collection。
@dataclass(frozen=True)
class ResolvedAlias:
    alias: str
    status: str
    collection: str
    collection_dir: Path
    corpus_revision: str
    release_manifest: Optional[Path]
    golden_report: Optional[Path]


# 构建命令行参数；alias 是业务侧线上稳定入口，指向某个已通过评测的真实 collection。
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Manage stable RAG collection aliases.")
    parser.add_argument("--env", default=None, help="Optional .env path; default is repo root .env.")
    subparsers = parser.add_subparsers(dest="command", required=True)

    switch_parser = subparsers.add_parser("switch", help="Point an alias at the manifest's generated collection.")
    switch_parser.add_argument("--manifest", default="manifest.yaml", help="Manifest whose collection should become the alias target.")
    switch_parser.add_argument("--alias", default=None, help="Alias name; default is <collection_prefix>_current.")
    switch_parser.add_argument("--force", action="store_true", help="Switch even if golden questions have not passed.")

    status_parser = subparsers.add_parser("status", help="Print current alias state.")
    status_parser.add_argument("--alias", default="payroll_rag_current")

    resolve_parser = subparsers.add_parser("resolve", help="Resolve stable alias to a concrete collection.")
    resolve_parser.add_argument("--alias", default="payroll_rag_current")
    return parser


# CLI 入口提供 alias 切换、查看和解析，供发布流程和业务查询复用。
def main() -> int:
    args = build_parser().parse_args()
    rag_dir = Path(__file__).resolve().parent
    env_path = Path(args.env).resolve() if args.env else None
    output_dir = resolve_output_dir(env_path)
    try:
        if args.command == "switch":
            manifest_path = _resolve_rag_path(rag_dir, args.manifest)
            state = switch_alias_to_manifest(
                output_dir=output_dir,
                manifest_path=manifest_path,
                alias_override=args.alias,
                require_passed=not args.force,
            )
            print("alias_switched: %s" % state["alias"])
            print("status: %s" % state["status"])
            print("collection: %s" % state["collection"])
            print("collection_dir: %s" % state["collection_dir"])
        elif args.command == "status":
            state = load_alias_state(output_dir, args.alias)
            print(yaml.safe_dump(state, allow_unicode=True, sort_keys=False).rstrip())
        elif args.command == "resolve":
            resolved = resolve_alias(output_dir, args.alias)
            print("alias: %s" % resolved.alias)
            print("status: %s" % resolved.status)
            print("collection: %s" % resolved.collection)
            print("collection_dir: %s" % resolved.collection_dir)
            print("corpus_revision: %s" % resolved.corpus_revision)
        return 0
    except Exception as exc:
        print("rag alias failed: %s" % exc, file=sys.stderr)
        return 1


# 解析相对于 memory/rag 的输入路径，方便在仓库根目录和子目录都能执行。
def _resolve_rag_path(rag_dir: Path, value: str) -> Path:
    path = Path(value)
    return path if path.is_absolute() else rag_dir / path


# 按 manifest 的 prefix + pipeline signature 找到目标 collection，并切换 alias。
def switch_alias_to_manifest(
    output_dir: Path,
    manifest_path: Path,
    alias_override: Optional[str],
    require_passed: bool = True,
) -> Dict[str, Any]:
    manifest = load_manifest(manifest_path)
    alias = alias_override or manifest.collection_alias
    collection_dir = output_dir / manifest.collection
    return switch_alias_to_collection(
        output_dir=output_dir,
        alias=alias,
        collection=manifest.collection,
        collection_dir=collection_dir,
        require_passed=require_passed,
    )


# 切换 alias 前读取 release-manifest，默认只允许通过 golden gate 的 collection 上线。
def switch_alias_to_collection(
    output_dir: Path,
    alias: str,
    collection: str,
    collection_dir: Path,
    require_passed: bool = True,
) -> Dict[str, Any]:
    release_manifest_path = collection_dir / "release-manifest.yaml"
    if not release_manifest_path.exists():
        raise FileNotFoundError("release manifest not found for collection: %s" % release_manifest_path)
    release_manifest = yaml.safe_load(release_manifest_path.read_text(encoding="utf-8")) or {}
    quality_gate = release_manifest.get("quality_gate") or {}
    if require_passed and quality_gate.get("golden_set_passed") is not True:
        raise ValueError("collection has not passed golden questions; use --force only for manual recovery")

    previous_state = _try_load_alias_state(output_dir, alias)
    state = {
        "alias": alias,
        "status": "active",
        "mode": "direct_alias",
        "collection": collection,
        "collection_dir": str(collection_dir),
        "corpus_revision": release_manifest.get("build", {}).get("corpus_revision", ""),
        "pipeline_signature": release_manifest.get("pipeline_signature", ""),
        "release_manifest": str(release_manifest_path),
        "golden_report": quality_gate.get("golden_report"),
        "switched_at": datetime.now(timezone.utc).isoformat(),
        "previous_collection": previous_state.get("collection") if previous_state else None,
        "note": "alias 是线上稳定入口；raw_docs 增量复用同一 collection，组件契约变更通过评测后切到新 collection。",
    }
    write_yaml(alias_state_path(output_dir, alias), state)
    return state


# 解析 alias 当前指向；兼容旧复杂状态文件，读取后按 current 直接解释。
def resolve_alias(output_dir: Path, alias: str) -> ResolvedAlias:
    state = load_alias_state(output_dir, alias)
    return ResolvedAlias(
        alias=str(state["alias"]),
        status=str(state["status"]),
        collection=str(state["collection"]),
        collection_dir=Path(str(state["collection_dir"])),
        corpus_revision=str(state.get("corpus_revision") or state.get("corpus_version") or ""),
        release_manifest=optional_path(state.get("release_manifest")),
        golden_report=optional_path(state.get("golden_report")),
    )


# 读取 alias 状态；如果遇到旧复杂格式，归一化成 direct_alias 视图。
def load_alias_state(output_dir: Path, alias: str) -> Dict[str, Any]:
    path = alias_state_path(output_dir, alias)
    if not path.exists():
        raise FileNotFoundError("alias state not found: %s" % path)
    state = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    return normalize_alias_state(state)


# 读取旧 alias 状态用于记录 previous_collection；不存在时返回 None。
def _try_load_alias_state(output_dir: Path, alias: str) -> Optional[Dict[str, Any]]:
    try:
        return load_alias_state(output_dir, alias)
    except FileNotFoundError:
        return None


# 把历史复杂状态压平成直接 alias 状态，便于从旧实现平滑退回。
def normalize_alias_state(state: Dict[str, Any]) -> Dict[str, Any]:
    if state.get("mode") == "direct_alias" or "collection" in state:
        return state
    target = state.get("current") or state.get("green") or state.get("blue")
    if not target:
        return state
    return {
        "alias": state.get("alias"),
        "status": state.get("status") or "active",
        "mode": "direct_alias",
        "collection": target.get("collection"),
        "collection_dir": target.get("collection_dir"),
        "corpus_revision": target.get("corpus_revision") or target.get("corpus_version"),
        "release_manifest": target.get("release_manifest"),
        "golden_report": target.get("golden_report"),
        "switched_at": state.get("switched_at"),
        "note": "从旧 alias 状态归一化为直接指针视图。",
    }


# 计算稳定 alias 状态文件路径。
def alias_state_path(output_dir: Path, alias: str) -> Path:
    return output_dir / "aliases" / ("%s.yaml" % alias)


# 兼容 YAML 里的空路径字段。
def optional_path(value: Any) -> Optional[Path]:
    if value is None or str(value).strip() == "":
        return None
    return Path(str(value))


if __name__ == "__main__":
    raise SystemExit(main())
