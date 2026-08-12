# SciAide

面向科研工作者的本地优先桌面 AI Agent，支持自定义模型、工具调用、MCP、Skill、科研知识库和可信引用。

当前阶段：**P1 模型配置与流式聊天闭环（首个可用纵向版本）**。

## 当前能力

- Wails + React + TypeScript
- Application / Port / Adapter 边界
- SQLite 版本化迁移与 Project / Conversation / Message / Run 持久化
- OS 应用目录分离
- 统一错误、事件 Envelope 和日志脱敏/轮转
- Windows Credential Manager 密钥隔离，前端只显示状态和掩码
- OpenAI-compatible 流式 Provider、连接测试、错误分类和安全重试
- 通过 `/v1/models` 自动发现、搜索和选择模型，并保留手动 Model ID 兜底
- 多轮聊天、停止生成、Usage、RunEvent 与 Snapshot 恢复
- 可脚本化 `FakeChatModel`、Provider Fixture 测试、威胁模型、ADR 和 CI

## 开发

依赖安装和命令见 [`docs/development.md`](docs/development.md)。完整架构与阶段门禁见 [`start.md`](start.md)。
