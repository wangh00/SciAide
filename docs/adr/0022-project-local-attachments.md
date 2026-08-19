# ADR-0022：项目本地附件与派生数据目录

## 决策

SciAide 继续把全局配置、SQLite、Skill、MCP、日志与小型全局缓存保存在 `~/.sciaide`。附件原件、文档解析结果、OCR/索引缓存、单次 Run 临时文件和科研 Artifact 默认跟随项目，统一收敛到 `<Workspace>/.sciaide`。

```text
<Workspace>/.sciaide/
├── project.json
├── attachments/objects/<sha256>/
├── cache/documents/
├── artifacts/
└── tmp/
```

普通 Workspace 工具隐藏并拒绝 `.sciaide` 路径。模型只能用项目作用域的附件 ID 调用 `builtin.attachment.list`、`builtin.document.inspect`、`builtin.document.read` 和 `builtin.document.search`。这些工具仍经过 ToolRegistry、Plan/Full Access、Schema、超时、取消和结果上限。

附件导入使用项目目录内暂存文件、SHA256 和同卷原子重命名。PDF 使用纯 Go 解析器按页提取；DOCX/XLSX 使用有 ZIP 路径、条目数、展开体积和压缩比限制的 XML 解析；TXT/Markdown/CSV 要求有效 UTF-8。解析缓存删除后可从持久附件原件重建。

项目根不再散落 `.sciaide-workspace.json`。启动时只在旧 marker 与数据库项目 ID 匹配时迁移为 `.sciaide/project.json`；已有但不属于该项目的 `.sciaide` 目录不会被接管。不可用的外部磁盘不会在启动时被重新创建。

## 原因

科研项目常包含大量 PDF、扫描件和表格。把这些副本与派生索引全部写入系统盘会造成不可控的容量增长，也会使项目迁移后丢失本地材料。项目私有目录使大体积数据跟随用户选择的磁盘，同时把 SciAide 内部文件限制在一个可识别、可忽略和可清理的位置。

## 后果

- 清理 `cache` 不得删除 `attachments` 或 `artifacts`。
- 删除/移除外部 Workspace 时默认保留 `.sciaide`；托管 Workspace 仍整体移动到回收备份。
- 扫描 PDF 的 OCR、FTS5/Embedding 索引和正式 Artifact 生命周期由后续阶段补充，但必须复用同一项目私有根。
- 用户后续显式配置独立缓存路径时，只覆盖 `cache`，不能把附件原件误标为可清理缓存。
