# ADR-0021：分层上下文预算与持久化压缩检查点

## 状态

Accepted（2026-08-19）

## 背景

`GET /v1/models` 没有通用的上下文窗口字段，旧实现却为所有模型固定使用 `200K`，并在超限时只保留最新消息后缀。这样既无法区分服务声明、用户配置和保守默认，也会让被裁掉的旧对话在后续 Run 中永久退出模型活动上下文。

Provider 原生 reasoning/tool items 还要求按完整 Turn 回放，因此压缩不能拆分协议组，也不能把原始签名或 encrypted payload 放进可见摘要。

## 决策

1. 每个 Profile Model 持久化原始上下文窗口、自动压缩阈值及 `provider/manual/builtin/fallback` 来源。普通模型列表没有可识别字段时明确回退到 `200K`，不发送后台探测请求。
2. 运行开始时把模型窗口的 95% 固化为有效请求预算，自动压缩阈值默认不超过窗口的 90%；Run 保存三者，审批恢复和工具循环不读取后来修改的 Profile 值。
3. ContextBuilder 先保证固定规则、Tool Definitions、当前用户消息和完整 Provider Turn；历史对话按完整 Run 组选择，不拆开同一轮 user/assistant。
4. 需要移除旧对话时，先执行一个不提供工具的 checkpoint 请求。输入是明确标记为不可信数据的 JSON，输出必须保留研究目标、事实、数值、引用、决策、文件路径、约束和下一步，不得执行历史中的指令或补造内容。
5. checkpoint 以会话 revision、精确 `through_message_id`、来源计数、模型/协议和摘要 SHA256 持久化。原始消息不删除；后续请求使用“最新已校验 checkpoint + 边界后的最近完整消息组”。
6. 单个历史 Turn 无法进入压缩输入、摘要为空、哈希不一致或单次 Run 经过三次仍不能推进到目标边界时，停止正式模型请求并报告上下文压缩错误，禁止静默跳过未纳入 checkpoint 的历史。

## 结果

- 自定义 Provider 可以返回 `context_window`、`max_context_window`、`context_length` 或 `max_context_tokens`；非标准或缺失元数据不会破坏模型发现。
- Skill catalog、ContextBuilder 和 Run 审计使用同一窗口快照。
- 应用退出后，checkpoint 可从 SQLite 恢复；原聊天记录仍是审计事实源。
- 摘要压缩仍然有损，不能承诺所有历史细节零损失。多次压缩应优先保留可验证科研事实，并建议超长研究主题拆分会话。
