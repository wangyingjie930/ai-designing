# reflection/replay

`reflection/replay` 是把附件里的 Python `ExperienceReplay` 翻译成 Go + Eino ADK 的版本。

它保留三层语义：

- L0 `ExecutionTrace`：一次活动或运营任务执行的原始轨迹。
- L1 `Reflection`：失败轨迹触发的单次复盘，输出 root cause、lesson、prevention。
- L2 `Lesson`：当近期失败率相对前一窗口出现 spike 时，从多个失败里抽跨任务经验。

适合的痛点不是 coding，而是“运营活动、销售承接、客户沟通等重复执行中反复踩同类坑”。`cmd/replay-retrospective-agent` 用 AI 公开课报名转化做默认场景：先记录历史成功/失败轨迹，抽出可复用 lesson，再让 ADK agent 给下一场活动输出执行提醒。

本地验证：

```bash
env GOCACHE=/private/tmp/ai-designing-gocache go test ./reflection/replay ./cmd/replay-retrospective-agent
```
