# ADR-0012：P2 Provider Tool Calling 与有界 AgentLoop

- 状态：已采纳
- 日期：2026-08-13

## 上下文

P2.1～P2.3 已建立 Tool 协议、持久化、权限审批和有界执行器，但聊天仍是单次模型流：Provider 不发送工具定义、不解析流式 Tool Call，也不能把 ToolResult 回填模型。若直接把循环堆入 `chat.Service`，会混合 Run 创建、模型协议、权限、执行和 UI 事件职责，并增加审批后后台 goroutine 悬挂或绕过授权的风险。

## 候选方案

1. 在 `chat.Service` 内扩展多轮循环，审批时保持 goroutine 等待。
2. 新建独立 Agent 应用层，审批时安全退出，并通过显式 Resume 恢复。
3. 先自动执行所有低风险工具，等审批 UI 完成后再接 PolicyEngine。

## 决策

采用方案 2。

- `model.Message` 明确表达 assistant `ToolCalls` 与 tool message `ToolCallID`；OpenAI-compatible Adapter 发送标准 `tools`，并按 `index` 有界累积 SSE 中的 `delta.tool_calls`。ID、名称、参数、数量和单行大小均有限制；参数必须是 JSON object，不完整或非法流失败关闭。
- `internal/app/agent` 独立拥有 `ContextBuilder`、`RunBudget` 和 `Loop`。默认预算为 8 个模型 Turn、12 个 Tool Call、5 分钟；每次模型或工具调用前检查，禁止无限循环。
- ContextBuilder 固定安全系统规则，裁剪旧会话但保留最新消息和已完成 ToolResult；ToolResult 通过独立 tool role 回填，并由 Provider Adapter 包裹为不可信数据。
- 每个模型工具调用都必须依次经过 `ToolRegistry → JSON Schema → PolicyEngine/Approval → ToolExecutor`。模型只提供工具名、Provider Call ID 与参数，不能提供风险、权限、版本或幂等属性。
- 遇到审批时 AgentLoop 不保留等待 goroutine，Run 和 ToolCall 持久化为等待态后退出。用户解决全部审批后，PermissionFacade 显式调用 `chat.Resume`；恢复过程先执行已获准的 running ToolCall，再重新构建上下文继续模型轮次。
- `chat.Service` 只负责原子创建 Run/消息、异步调度、取消、快照和 RunEvent 发布；AgentLoop 不依赖 Wails。
- 应用重启仍把未完成 Approval、ToolCall、Run 标为 expired/interrupted，不自动恢复或重放非幂等调用。

## 影响

- FakeModel 已覆盖 Tool Call → 执行 → ToolResult 回填 → 最终回答；另有审批暂停不执行、上下文裁剪和预算测试。
- OpenAI-compatible 服务必须正确支持 Chat Completions `tools/tool_calls`；不兼容实现会返回可理解的模型协议错误，而不会降级为无审计执行。
- P2.5 需要在前端展示 `approval.required` 和 pending Approval，并调用已有 ResolveApproval；后端闭环与恢复入口已具备。
- 模型 Turn 通过 `runs.model_turns` 原子持久化并在每次模型请求前消费；ToolCall 数量由持久化记录累计，持续时间由 `Run.StartedAt` 计算，因此多次审批暂停/恢复不会重置任何一类预算。

## 复核补充（2026-08-13）

P2.1～P2.4 安全复核后进一步收紧：Wails 不再暴露直接 Policy 评估或 Executor 执行入口；运行时 Deadline 会主动取消卡住的模型流；累计 ToolResult 与 Tool Definition 数量纳入上下文上限；JSON Schema/Instance 拒绝尾随 JSON；Tool Schema、Arguments、Permission 数量和资源长度均有硬边界。上述约束属于同一决策的纵深防御，不改变持久化协议。

二次复核补充统一 Run 终止边界：主动取消或 AgentLoop 失败会在同一 SQLite 事务内关闭 Run、Assistant Message、所有非终态 ToolCall 和 pending Approval，并写入单一终态 RunEvent；`waiting_approval` 即使没有活动 goroutine 也可取消。Executor 在工具忽略取消并迟到返回时重新读取 ToolCall 状态，禁止用迟到结果覆盖终态。终态 Run 也拒绝旧 goroutine 的普通 Update 回写。

P2.5 的用户权限入口收敛为 `Plan` 与 `Full Access` 两档。`Plan` 对每个 ToolCall 请求一次用户决定，`Full Access` 对已注册且通过参数/边界校验的 ToolCall 自动放行；风险信息只作展示，不覆盖用户选择。拒绝仍作为普通、持久化的 ToolResult 回填给 Provider，后续是否继续输出及输出内容完全由模型决定，应用层不拼接替代回答。审批恢复以 Approval ID 作为幂等键：同一审批的重复点击只触发一次恢复，不同审批周期可以继续恢复同一 Run。
