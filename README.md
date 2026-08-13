# SciAide

面向科研工作者的本地优先桌面 AI Agent，支持自定义模型、工具调用、MCP、Skill、科研知识库和可信引用。

当前阶段：**P2 Agent Loop、内置工具与权限系统**。

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
- 可脚本化 `FakeChatModel`、Provider Fixture 测试、威胁模型、ADR 和 CI

## 开发

依赖安装和命令见 [`docs/development.md`](docs/development.md)。完整架构与阶段门禁见 [`start.md`](start.md)。
