# SciAide

面向科研工作者的本地优先桌面 AI Agent，支持自定义模型、工具调用、MCP、Skill、科研知识库和可信引用。

当前阶段：**P3.5 模型协议完整性**。

## 当前能力

- Wails + React + TypeScript
- Application / Port / Adapter 边界
- SQLite 版本化迁移与 Project / Conversation / Message / Run 持久化
- 默认数据根目录 `~/.sciaide`，旧 AppData 数据首次启动安全迁移
- 默认托管 Workspace 与用户自选外部目录；支持安全移除项目/会话
- 统一错误、事件 Envelope 和日志脱敏/轮转
- Windows Credential Manager 密钥隔离，前端只显示状态和掩码
- OpenAI-compatible 流式 Provider、连接测试、错误分类和安全重试
- 一个 API 配置（Base URL + Key）可保存多个模型；支持 `/v1/models` 多选、手动添加和聊天时切换
- 多轮聊天、停止生成、Usage、RunEvent 与 Snapshot 恢复
- P2 工具协议基线：ToolCall/ToolResult 状态机、参数 Schema 校验、审计事件与持久化
- P2 权限基线：统一 ToolRegistry、精确资源 PolicyEngine、逐项 Approval、作用域 Grant 与重启恢复
- P2 执行基线：有界 ToolExecutor、取消/超时/panic 隔离、Workspace PathGuard、列目录与 UTF-8 文本读取工具
- P3 MCP：stdio / Streamable HTTP 配置、显式信任、initialize 与 Tools/Resources/Prompts 能力发现
- 兼容 Claude Desktop、Cursor、Codex 常见的 `mcpServers` JSON，可一次粘贴并导入多个 Server
- MCP Tool 使用稳定的 `mcp.<namespace>.<tool>` 名称进入统一 ToolRegistry、Plan/Full Access 审批和 ToolExecutor
- MCP stdio 仅继承最小环境；SecretEnv 明文保存在 Windows Credential Manager；Resource/Prompt 不自动注入上下文
- Anthropic thinking/signature/redacted_thinking 与 Responses reasoning/encrypted content 按 Provider Turn 不可变持久化，并在工具结果后严格回放；原始协议状态不进入聊天 Snapshot
- 推理证据区分“参数已接受”和“已观察到思考”，支持 reasoning token 汇总、默认折叠的安全状态卡，以及不拆分原生推理/工具协议组的上下文压缩
- 可脚本化 `FakeChatModel`、Provider Fixture 测试、威胁模型、ADR 和 CI

## 开发

依赖安装和命令见 [`docs/development.md`](docs/development.md)。完整架构与阶段门禁见 [`start.md`](start.md)。
