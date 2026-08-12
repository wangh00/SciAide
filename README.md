# SciBuddy

面向科研工作者的本地优先桌面 AI Agent，支持自定义模型、工具调用、MCP、Skill、科研知识库和可信引用。

当前阶段：**P0 工程基线与架构护栏**。

## 当前骨架

- Wails + React + TypeScript
- Application / Port / Adapter 边界
- SQLite 版本化迁移与 Project Repository
- OS 应用目录分离
- 统一错误、事件 Envelope 和日志脱敏/轮转
- 可脚本化 `FakeChatModel`
- 初始威胁模型、ADR、测试和 CI

## 开发

依赖安装和命令见 [`docs/development.md`](docs/development.md)。完整架构与阶段门禁见 [`start.md`](start.md)。

