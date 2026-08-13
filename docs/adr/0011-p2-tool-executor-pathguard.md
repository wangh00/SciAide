# ADR-0011：P2 ToolExecutor 与 Workspace PathGuard

- 状态：已采纳
- 日期：2026-08-13

## 决策

所有实际 Tool 调用统一经过 `internal/app/tool.Executor`。Executor 仅接受已经处于 `running` 的持久化 ToolCall，并在调用前再次校验 Run 所属 Project、当前 Registry Definition 与历史安全快照。执行受到 context、默认 30 秒超时、显式取消、单 Call 并发锁、panic 隔离和结果大小限制保护；公开 Result 不包含内部错误或 panic 内容。

Workspace 文件能力通过 `internal/tools/pathguard` 和 Go `os.Root` 打开。传入路径必须为项目相对路径，先经 `Clean + Abs/Rel` 组件边界检查，再由 `os.Root` 在打开时约束符号链接不能逃逸根目录。P2.3 内置工具仅提供非递归目录列表和有界 UTF-8 文本读取；二进制文件、绝对路径、路径穿越和逃逸链接均拒绝。

内置工具在 Bootstrap 注册到进程内 ToolRegistry。注册并不赋予执行权限；ToolCall 仍必须经过 Schema 校验、PolicyEngine 与 Approval。Executor 和取消能力通过最小 ToolFacade 暴露，P2.4 AgentLoop 将复用同一入口。

## 影响

- 超大文本结果按 UTF-8 边界截断并标记 `truncated`；超大结构化结果失败关闭。
- 当前只读工具不递归遍历，不读取 Workspace 外文件，也不跟随逃逸链接。
- Go `os.Root` 依赖当前 Go 1.25 项目基线；若降低 Go 版本，需要等价的句柄级安全实现。
- Artifact 落盘、写文件原子替换和删除回收属于 P2.6。
