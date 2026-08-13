# ADR-0009：P2 统一工具协议与状态持久化

- 状态：已采纳
- 日期：2026-08-13

## 决策

内置工具、MCP 与 Skill 工具统一实现 `internal/app/tool` 中的 `Tool` 接口。工具定义必须包含全限定名称、版本、输入/输出 Schema、风险、权限要求和幂等属性；模型只提供工具名、Provider Call ID 与参数，不能自行声明风险、权限或版本。

P2 首版 Schema 校验器支持有界 JSON Schema 子集：对象/数组/基础类型、properties、required、additionalProperties、items、enum/const、数值与长度约束、pattern。未知断言关键字采用失败关闭策略，不能静默跳过。

`ToolCall` 状态为 `pending → awaiting_approval → running → completed/failed/denied/cancelled/interrupted`。终态不能回到运行态，状态更新使用期望旧状态实现乐观并发。ToolCall、ToolResult 与对应 RunEvent 在同一 SQLite 事务中提交；`provider_call_id` 防止同一 Run 重复接收，`run_id + idempotency_key` 防止同一 Run 重复执行。应用启动时只将未完成调用标为 `interrupted`，不自动重放。

工具定义的权限要求、风险和幂等属性在提出调用时做快照，防止 ToolRegistry 后续升级改变历史审计语义。工具异常必须转换为结构化 Result，不能把 panic 或内部堆栈返回模型。
