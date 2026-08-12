# ADR-0005：P1 聊天持久化与流式事件

- 状态：已采纳
- 日期：2026-08-12

## 决策

P1 使用结构化 `Conversation → Message → MessagePart` 保存多轮内容，并为每次生成创建独立 `Run`。内部流事件为 `run.started`、`content.started/delta/completed`、`usage.updated` 及 `run.completed/failed/cancelled`。

事件沿用版本化 `events.Envelope`，保存至 `run_events` 后才通过 Wails 发布。文本增量由应用层合并；流式正文周期性保存，终态 Message 和 Run 在最终事件发布前落库。前端通过 `GetRunSnapshot` 周期校准，不能把事件流当事实来源。

启动时把遗留 `queued/running` Run 及其 Assistant Message 原子改为 `interrupted/incomplete`，不自动重放远端模型请求。

OpenCode 的结构化 Message Part、Run/Cancel、流处理和 Fixture 思想仅用于设计验证；SciAide 没有复制代码，也没有引入其运行时依赖。
