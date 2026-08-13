# Changelog

## [Unreleased]

### Added

- 完成 P2.5 会话级 `Plan` / `Full Access` 权限模式，并将模式快照持久化到每个 Run。
- 增加审批卡片、ToolCall 时间线、最近 Run Snapshot 恢复与工具结果状态展示。
- 支持停止当前 Run，以及通过 cancel-then-start 在同一会话中中断后继续。

### Changed

- `Plan` 对每个 ToolCall 只审批一次；`Full Access` 自动放行已注册且通过工程边界校验的工具。
- 风险等级仅用于提示和审计，不再限制用户选择；历史 PermissionGrant 不参与运行时决策。
- 拒绝审批后将普通 denied ToolResult 回填模型，不由程序补写或改写模型回答。

### Fixed

- Snapshot 仅更新其所属会话，避免迟到轮询污染已切换的研究会话。
- 审批恢复按 Approval ID 去重，并阻止旧的终态 Run 被误当作当前活动 Run 执行 steer。
