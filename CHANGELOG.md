# Changelog

## [Unreleased]

### Added

- 增加客户端全局用量统计页，支持全部 API/模型汇总、按配置/模型筛选、今天/近 7 天/近 30 天/自定义日期范围以及本地日期趋势。
- 将 OpenAI-compatible 用量归一化为实际输入、输出、缓存读取、缓存创建四个互斥 Token 桶，并按缓存读取占可缓存输入的比例计算命中率。
- 增加 MCP Server 配置与状态 UI，支持 stdio 和 Streamable HTTP。
- 增加兼容 `mcpServers` 结构的 JSON 批量导入；导入项默认不受信任且不自动连接，敏感 `env` 自动进入系统凭据库。
- Windows 桌面版以隐藏控制台方式运行 stdio MCP 子进程，避免 `npx.cmd` 等命令弹出可关闭的 CMD 宿主窗口。
- MCP 配置保存成功后显示自动消失的顶部 Toast，并将长连接操作明确命名为“连接并启用”。
- 使用官方 Go MCP SDK 完成 initialize、协议/版本协商及 Tools、Resources、Prompts 分页发现。
- MCP Tool 以稳定命名空间动态注册到统一 ToolRegistry，并复用现有 Schema、Plan/Full Access、ToolExecutor 和审计链路。
- 支持 MCP SecretEnv 设置/清除，明文仅存 Windows Credential Manager，SQLite 只保存引用。
- 增加能力列表变更刷新、异常断开状态恢复和启动时陈旧运行状态修复。
- 完成 P2.5 会话级 `Plan` / `Full Access` 权限模式，并将模式快照持久化到每个 Run。
- 增加审批卡片、ToolCall 时间线、最近 Run Snapshot 恢复与工具结果状态展示。
- 支持停止当前 Run，以及通过 cancel-then-start 在同一会话中中断后继续。

### Changed

- stdio 参数不经过 Shell，子进程仅继承最小 allowlist 环境；远程 MCP 默认要求 HTTPS，HTTP 仅允许回环地址。
- MCP Resource/Prompt 只发现和展示，不自动注入模型上下文。
- `Plan` 对每个 ToolCall 只审批一次；`Full Access` 自动放行已注册且通过工程边界校验的工具。
- 风险等级仅用于提示和审计，不再限制用户选择；历史 PermissionGrant 不参与运行时决策。
- 拒绝审批后将普通 denied ToolResult 回填模型，不由程序补写或改写模型回答。

### Fixed

- Snapshot 仅更新其所属会话，避免迟到轮询污染已切换的研究会话。
- 审批恢复按 Approval ID 去重，并阻止旧的终态 Run 被误当作当前活动 Run 执行 steer。
