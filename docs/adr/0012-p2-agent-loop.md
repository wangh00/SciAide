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
- `internal/app/agent` 独立拥有 `ContextBuilder` 和 `Loop`。Run 不设累计模型 Turn、累计 Tool Call 或墙钟时长上限，复杂任务由模型持续推进，直到正常完成、用户停止或具体请求/工具失败。
- ContextBuilder 固定安全系统规则，裁剪旧会话但保留最新消息和已完成 ToolResult；ToolResult 通过独立 tool role 回填，并由 Provider Adapter 包裹为不可信数据。
- 每个模型工具调用都必须依次经过 `ToolRegistry → JSON Schema → PolicyEngine/Approval → ToolExecutor`。模型只提供工具名、Provider Call ID 与参数，不能提供风险、权限、版本或幂等属性。
- 遇到审批时 AgentLoop 不保留等待 goroutine，Run 和 ToolCall 持久化为等待态后退出。用户解决全部审批后，PermissionFacade 显式调用 `chat.Resume`；恢复过程先执行已获准的 running ToolCall，再重新构建上下文继续模型轮次。
- `chat.Service` 只负责原子创建 Run/消息、异步调度、取消、快照和 RunEvent 发布；AgentLoop 不依赖 Wails。
- 应用重启仍把未完成 Approval、ToolCall、Run 标为 expired/interrupted，不自动恢复或重放非幂等调用。

## 影响

- FakeModel 已覆盖 Tool Call → 执行 → ToolResult 回填 → 最终回答；另有审批暂停不执行、上下文裁剪和超过旧版累计上限仍继续运行的回归测试。
- OpenAI-compatible 服务必须正确支持 Chat Completions `tools/tool_calls`；不兼容实现会返回可理解的模型协议错误，而不会降级为无审计执行。
- P2.5 需要在前端展示 `approval.required` 和 pending Approval，并调用已有 ResolveApproval；后端闭环与恢复入口已具备。
- 模型 Turn 仍通过 `runs.model_turns` 原子持久化，ToolCall 仍完整保存；这些字段仅用于统计、审计和恢复，不作为拒绝继续执行的计数器。

## 复核补充（2026-08-13）

P2.1～P2.4 安全复核后进一步收紧：Wails 不再暴露直接 Policy 评估或 Executor 执行入口；运行时 Deadline 会主动取消卡住的模型流；累计 ToolResult 与 Tool Definition 数量纳入上下文上限；JSON Schema/Instance 拒绝尾随 JSON；Tool Schema、Arguments、Permission 数量和资源长度均有硬边界。上述约束属于同一决策的纵深防御，不改变持久化协议。

二次复核补充统一 Run 终止边界：主动取消或 AgentLoop 失败会在同一 SQLite 事务内关闭 Run、Assistant Message、所有非终态 ToolCall 和 pending Approval，并写入单一终态 RunEvent；`waiting_approval` 即使没有活动 goroutine 也可取消。Executor 在工具忽略取消并迟到返回时重新读取 ToolCall 状态，禁止用迟到结果覆盖终态。终态 Run 也拒绝旧 goroutine 的普通 Update 回写。

P2.5 的用户权限入口收敛为 `Plan` 与 `Full Access` 两档。`Plan` 对每个 ToolCall 请求一次用户决定，`Full Access` 对已注册且通过参数/边界校验的 ToolCall 自动放行；风险信息只作展示，不覆盖用户选择。拒绝仍作为普通、持久化的 ToolResult 回填给 Provider，后续是否继续输出及输出内容完全由模型决定，应用层不拼接替代回答。审批恢复以 Approval ID 作为幂等键：同一审批的重复点击只触发一次恢复，不同审批周期可以继续恢复同一 Run。

## 运行策略修订（2026-08-20）

旧版 `RunBudget` 会在浏览器/MCP 任务完成最后一次工具调用后、生成最终回答前过早终止 Run。因此删除 Run 级 Turn/Tool/Time 累计上限及 `MODEL_TURN_BUDGET_EXCEEDED`、`TOOL_CALL_BUDGET_EXCEEDED`、`RUN_DURATION_BUDGET_EXCEEDED` 终止路径。用户停止与中断仍通过 Run Context 取消；模型 HTTP 和单次 Tool/MCP 保留各自超时；上下文仍按模型窗口压缩。这些边界不限制一个 Run 可以经历的总轮数。

## Prompt Cache 稳定性修订（2026-08-20）

AgentLoop 将“旧请求前缀保持不变、仅在末尾追加新内容”作为三协议共同约束。Chat Completions 不发送非标准缓存参数，交由供应商自动匹配前缀；OpenAI Responses 发送固定的会话级 `prompt_cache_key`；Anthropic Messages 在最新稳定内容块设置 `cache_control: {type: "ephemeral"}`，使工具、系统规则和此前消息共同进入缓存前缀。兼容网关明确拒绝可选字段时，同一请求移除该字段重试，协议客户端在配置不变期间记住该能力结论。

完整 ToolResult 仍是本地界面和审计事实源；模型上下文另存版本化、有界、不可变的结果快照。首次完成工具调用时生成快照，后续请求不再根据新结果、客户端版本或显示状态重新裁剪旧内容。旧数据库没有快照的记录使用确定性兼容回退，不改写原始结果。

上下文压缩请求复用正常会话的稳定系统规则、已校验 checkpoint、Skill 目录和工具定义，按原始 user/assistant 角色重放待压缩历史，仅在末尾追加 checkpoint 指令。压缩仍是显式记录的历史替换边界，但摘要请求本身不再因为替换系统提示词或二次 JSON 编码而无条件破坏既有缓存。

## 最终回答边界修订（2026-08-20）

一个用户 Run 可以包含多次模型请求，但只有不再请求工具的最后一次模型响应可以写入 assistant 正文。每次模型请求使用独立缓冲：带 ToolCall 的响应将可见过程说明和供应商允许展示的推理摘要持久化为不可变 `run_steps`，协议原生 ProviderTurn 仍独立保存并参与后续回放；最终响应则原子完成 assistant 消息和 Run。失败、取消或等待审批均不得把尚未确认性质的流式文本伪装成最终回答。

前端把 RunStep、完整 ToolCall 和审批按时间合并为一个运行时间线。运行时显示“处理中 + 耗时”并默认展开，完成后显示“已处理 + 耗时”并自动折叠；最终回答始终位于该时间线之后。Chat Completions 兼容服务若把 `<think>` 包装泄漏到普通 content，仅移除明确的思考标签及其包裹内容，不改写其余可见答案。

## 模型连接恢复修订（2026-08-20）

模型网络重试统一归 Agent 层所有，协议适配器只负责一次建流和协议兼容协商。建流前的瞬时连接、TLS 握手、响应头超时、HTTP 408/429/5xx 执行首次请求加最多 4 次重试；SSE 建立后在完成事件前中断，最多重放同一逻辑模型请求 5 次。退避采用带抖动的指数间隔并尊重 `Retry-After`，Run Context 取消可以立即打断等待。认证、参数、上下文、协议格式和永久证书校验错误不重试。

每次流尝试均先写入临时缓冲。只有收到完整完成事件后，usage、推理状态、ProviderItem、ToolCall、RunStep 和最终文本才允许提交；失败尝试整体丢弃，重放不得增加 `model_turns`，也不得重复执行工具或累计 Token。自动上下文压缩遵守同一事务边界。前端通过临时 `run.retrying` / `run.retry.recovered` 事件展示重连进度，状态不进入模型上下文，成功、终止或切换会话时自动清除。

## 最终回答可用性修订（2026-08-21）

模型轮次草稿日志、用量统计、推理观察和 RunStep 时间线属于辅助审计，不得因为单次写入失败而丢弃已经完整生成的回答。Responses/Anthropic 的 ProviderItem 在带工具调用时仍是下一轮请求的强一致前置条件；不带工具调用的最终轮无需回放，审计写入失败时允许继续原子提交最终正文。核心 Run/Message 终态、ToolResult、审批状态和需要继续回放的 ProviderTurn 仍保持失败关闭。

模型请求未知工具或给出不符合已发布 Schema 的参数时，Agent 不再直接终止 Run。该调用以不可执行的失败 ToolResult 持久化并回填同一协议上下文，由模型在下一轮修正工具或参数；若连失败 ToolResult 都无法持久化，则保持失败关闭，避免重复执行或伪造工具结果。

最终正文不再先写一次 `streaming` 消息再完成 Run，而只通过 `runs.Complete` 在同一事务内提交 assistant 正文、可用引用和 Run 终态。引用候选读取属于增强能力：读取失败时正文仍正常提交，只是不生成本轮可点击引用；核心终态事务失败仍保持失败关闭。

自动上下文压缩不再设置固定通过次数。只要新检查点的 `ThroughMessageID` 持续前进就继续处理超长历史；边界不前进或回到已见边界才视为循环并失败关闭。压缩请求的用量统计属于辅助遥测，写入失败不得丢弃已经完整、通过校验的检查点或阻止后续回答。

MCP `timeoutSeconds` 同时约束连接、能力发现和每次 `CallTool`，包括 stdio 子进程。单次调用超时会使该 MCP 会话退役，并由 ToolExecutor 保存为失败 ToolResult 回填模型；它不会直接终止 Agent Run。
