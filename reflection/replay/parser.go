package replay

import "strings"

// parseLessons 从模型输出里提取 INSIGHT 行，并给每条 lesson 附上来源任务。
func parseLessons(text string, sourceTasks []string) []Lesson {
	lessons := make([]Lesson, 0, 3)
	current := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "INSIGHT:") {
			continue
		}
		if current != "" {
			lessons = append(lessons, newLesson(current, sourceTasks))
		}
		current = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
	}
	if current != "" {
		lessons = append(lessons, newLesson(current, sourceTasks))
	}
	return lessons
}

// newLesson 统一设置 Python dataclass 里的默认 confidence 和 provenance。
func newLesson(insight string, sourceTasks []string) Lesson {
	tasks := make([]string, 0, len(sourceTasks))
	for _, task := range sourceTasks {
		task = strings.TrimSpace(task)
		if task != "" {
			tasks = append(tasks, task)
		}
	}
	return Lesson{
		Insight:     strings.TrimSpace(insight),
		SourceTasks: tasks,
		Confidence:  0.5,
	}
}
