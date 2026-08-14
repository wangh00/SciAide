# ADR-0017：Provider 原生 Turn 状态与严格回放

- 状态：已接受
- 日期：2026-08-14

## 背景

统一成 `text + tool_call + usage` 的最低公共协议足以完成普通聊天，但会丢失供应商要求继续携带的状态。Anthropic 的 `thinking`、`signature`、`redacted_thinking`，以及 OpenAI Responses 的 reasoning item/encrypted state 都不是普通 UI 文本。工具调用后的下一轮若缺失、改写或错误重排这些内容，服务端可能拒绝请求，恢复后的会话也无法可靠继续。

## 决策

SciAide 将用户可见消息和 Provider 原生协议状态分离：

1. `messages/message_parts` 继续作为前端可见的会话内容；
2. `provider_turn_items` 保存单次模型请求完成后产生的原生 assistant items；
3. Provider Item 按 `run + turn_index + item_ordinal` 唯一定位，只允许相同内容的幂等重写，禁止覆盖已保存的签名或 payload；
4. Provider Item 不进入聊天 Snapshot、Wails DTO 或默认 UI；
5. AgentLoop 在 ToolCall 提交审批或执行前先保存完整 Provider Turn；
6. 恢复及下一模型轮次按原 ordinal 重放 assistant items，再附加对应的 ToolResult；
7. Adapter 只能消费与自身 API 协议匹配的 Provider Turn，协议不匹配时失败关闭，不能静默降级成文本。

## Anthropic Messages 映射

Anthropic 流按 content block index 累积：

- `text_delta` → `text` block；
- `thinking_delta + signature_delta` → `thinking` block；
- `redacted_thinking.data` → `redacted_thinking` block；
- `input_json_delta` → `tool_use` block。

只有收到 `content_block_stop` 后，block 才成为不可变 Provider Item。下一轮严格恢复为：

```text
assistant: thinking/signature, redacted_thinking, text, tool_use
user:      tool_result
assistant: ...
```

Thinking 缺失 signature、redacted block 缺失 data、重复 ordinal、未完成 block、超限 payload 和修改既有 item 均作为协议错误处理。

## 上下文与安全

- Provider Turn 与其 ToolResult 是不可拆分的协议组；不能只保留 ToolCall 或只保留可见文本。
- 当前运行中的 Provider Turn 优先完整保留；超过上下文上限时失败并进入后续压缩策略，而不是生成可能无效的请求。
- ToolResult 仍经过不可信内容包装和累计大小限制。
- 原始 thinking 不是完整内部思维链的保证，也不默认展示；后续 UI 只消费独立的摘要/状态投影。

## 分阶段落地

- P3.5.1：Anthropic thinking/signature/redacted_thinking 的累计、持久化和工具轮次回放；
- P3.5.2：OpenAI Responses reasoning/encrypted item；
- P3.5.3：推理证据、Token、折叠 UI 与 Provider-safe compaction。

截至 2026-08-14，P3.5.1～P3.5.3 的实现和自动化 Provider Fixture 已落地。Responses-compatible 真实接口已经完成三轮模型请求、两次 MCP 工具调用及原生 Provider Item 落库/回放；另一路工具拒绝测试仍可正常完成回答。实测服务返回 reasoning summary 和 reasoning token，但 `encrypted_content` 为空字符串，所以非空加密 payload 的严格回放仍由自动化 Fixture 证明；真实 Anthropic E2E 尚待验收。运行记录分别保存 reasoning token、推理状态证据和 Anthropic 签名证据；只有收到 reasoning/thinking item 或供应商明确报告 reasoning token，界面才显示“已观察到思考”，HTTP 参数未被拒绝只显示“参数已接受”。上下文压缩按完整 Provider Turn 选择最新后缀，并把对应 ToolResult 作为同一不可拆分协议组处理；单个最新原生协议组无法放入窗口时明确失败，不生成结构损坏的请求。

## 结果

模型协议层不再把需要回传的推理状态误当成临时界面文字；程序退出、审批暂停和工具执行后都可以从本地事实源恢复同一 Provider Turn，同时避免把签名或脱敏数据暴露给前端。
