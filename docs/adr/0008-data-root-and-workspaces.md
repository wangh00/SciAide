# ADR-0008：统一数据根目录与 Workspace 生命周期

## 决策

生产数据统一放在 `~/.sciaide`：配置、数据库、缓存、日志、Skill、MCP 和备份各自分目录。默认 Workspace 位于 `~/.sciaide/data/workspaces/<project-id>`；用户也可选择任意外部目录。

从早期 `%LOCALAPPDATA%/SciAide` 布局迁移时只复制缺失文件、不覆盖新数据、不删除旧目录，并写入幂等迁移标记。Windows Credential Manager 中的 `secret_ref` 不变，无需复制密钥。

移除托管项目时，Workspace 先移动到 `~/.sciaide/backups/trash`，数据库事务失败则尝试恢复。移除外部项目永远只删除 SciAide 数据库记录，不删除外部文件。存在 queued/running Run 的项目或会话必须先停止运行；删除时同步清理无外键的 `run_events`。
