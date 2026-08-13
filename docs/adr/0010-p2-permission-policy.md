# ADR-0010：P2 ToolRegistry、权限策略与审批持久化

- 状态：已采纳
- 日期：2026-08-13

## 决策

ToolRegistry 是内置、MCP 与 Skill 工具进入 Agent Runtime 的唯一进程内注册点。注册时验证并深拷贝 Definition，重名注册失败，不能用后注册工具覆盖既有风险、权限或 Schema。ToolCall 只保存 Registry 中受信任 Definition 的快照；模型不能声明或降低风险、权限和版本。

PolicyEngine 以 `Project + Run + ToolCall` 为评估上下文。中风险及以上工具即使未声明资源权限，也会加入精确绑定工具名的 `tool.invoke` 合成权限。授权匹配必须同时精确匹配 Project、Tool、PermissionKind 和 Resource；Run 授权还必须匹配 Run，禁止目录前缀、父域名或其他模糊扩大。

P2.5 将策略入口收敛为会话级 `Plan` 与 `Full Access`。`Plan` 为整个 ToolCall 创建一次 call-scope Approval，接受后运行，拒绝后置为 denied；`Full Access` 自动放行已注册且通过 Schema 校验的工具。权限与风险明细仍作为 UI 信息和审计快照，但风险等级不覆盖用户选择。历史 PermissionGrant 保留作迁移兼容，不再参与运行时策略，也不再新增。

Approval 和 RunEvent 在同一 SQLite 事务内提交。应用重启时按 `pending Approval → ToolCall → Run` 的顺序恢复：审批变为 expired 并写审计事件，未完成 ToolCall 和 Run 变为 interrupted，任何有副作用的调用都不自动重放。

## 影响

- 前端只能解决 pending Approval，不能创建或复用长期 Grant。
- P2.5 不提供“始终允许”或其他长期 Grant；持续自动放行只能由用户显式切换当前会话到 `Full Access`。
- P2.2 只闭合权限决策和审批状态；真正调用 Tool 由后续受限 ToolExecutor 执行。
- 当前审批与 ToolCall/Run 状态分别事务提交，读取端以 Approval 为审批事实并通过恢复顺序处理极短窗口；若后续需要跨聚合严格原子性，应增加专用协调 Repository，而不是把 SQL 放入领域层。

## 重新评估条件

- 引入通配路径、域名组或组织级策略。
- MCP/Skill 需要动态权限发现。
- 多进程执行器要求跨进程租约或分布式锁。
