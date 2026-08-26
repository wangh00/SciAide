# ADR-0030：科研 Artifact 的不可变版本、来源快照与对象存储

## 上下文

聊天回答、Workspace 文件和 ToolResult 都可能成为科研交付物，但它们原先分别属于消息、普通文件和工具审计记录，缺少统一的版本身份、内容完整性与来源关系。直接把 Workspace 当产物库会把临时文件误当正式结果，也无法在会话、模型配置或知识索引变化后解释历史产物。P6.1 需要先建立稳定内核，再由 P6.2 增加 DOCX/PDF 等派生导出。

## 决定

1. Artifact 是项目级可变聚合，只允许修改名称、active/trashed 状态和当前版本指针；ArtifactVersion、Blob、Lineage 与 Citation snapshot 创建后不可更新。
2. SQLite 是 Artifact 元数据唯一事实源。真实字节按项目和 SHA256 存入 `<Workspace>/.sciaide/artifacts/objects/<prefix>/<sha256>`；Blob 身份由项目、哈希、大小和对象路径决定，文件名与 MIME 解释保存在各自 Version 中。
3. 保存先在同一项目私有根写入随机暂存文件并计算 SHA256，校验大小后原子发布对象，再用单个 SQLite 事务创建 Blob、Version、Lineage、Citation 并切换当前版本。元数据失败可能留下无引用对象，启动恢复负责清理；不能留下已发布的半条数据库版本。
4. 首批入口仅包括：显式保存已完成的完整助手回答、用户显式登记 Workspace 文件，以及成功 ToolResult 显式声明 `workspacePath` 的文件。ID-only `ArtifactRef` 仍是附件或来源引用，不能自动升级为科研产物。
5. Tool 文件登记必须同时满足项目 Workspace confinement、私有 `.sciaide` 排除、reparse/symlink 防逃逸，以及调用时持久化的 `workspace.read`/`workspace.write` 资源范围。登记失败不能改写已成功的 Tool 结果，后续由幂等恢复重试。
6. 每个版本保存生成时的 Run、Message、ToolCall、模型、协议、Skill 和 Workspace 路径快照。活外键只用于导航，删除 Conversation 时允许置空；字符串快照与版本本身继续保留。项目删除则显式删除整个 Artifact 聚合和对象所属 Workspace。
7. 助手回答只复制已经由 P5 持久化的正式 Citation。Tool 产物只有来自 `builtin.knowledge.search`、通过 Run 绑定、证据 SHA256 和项目身份校验的 CitationRef 才进入“可信引用”；普通 MCP、Skill 或其他 Tool 自报 Citation 只留在原始 Tool 审计中。
8. `source_key` 为自动来源提供幂等身份。助手回答按 Message，Workspace 文件按路径和内容哈希，Tool 产物按 ToolCall 与结果 ordinal 标识；同一键内容冲突必须报错，不能静默覆盖。
9. 下载始终写入随机临时文件，完整校验大小与 SHA256 后再原子发布，并拒绝覆盖已存在目标。预览只提供有界 UTF-8 文本和受支持的栅格图片；完整性检查是独立显式操作。

## 理由

- 不可变 Version 与内容地址把“名称调整”和“科研证据变化”分开，历史结果不会被原地覆盖。
- 元数据与大文件分离后，全局数据库保持轻量，项目可继续使用用户选择的磁盘。
- 来源快照不依赖会话和可重建索引永久存在，同时保留可用时的活导航关系。
- Tool 产物采用显式路径契约和原权限快照，避免扫描整个 Workspace 或让结果字段扩大工具权限。
- 复用 P5 可信引用规则，避免任意外部 Tool 通过自报 Citation 获得本地可信标识。

## 负面影响

- 对象先于 SQLite 事务发布，崩溃时可能短暂留下无引用文件；启动恢复需要扫描项目对象目录。
- P6.1 回收站不释放版本和对象空间，也没有永久删除单个 Artifact 的入口。
- 文本和图片预览是有界的，Office/PDF、复杂表格、代码差异和引用样式渲染留给后续阶段。
- Tool 文件登记当前发生在 Tool 成功持久化之后，大文件哈希会增加该次执行完成后的等待时间。

## 重新评估条件

- P6.2 增加 DOCX/PDF 导出时，派生文件必须创建新的显式版本或导出记录，不能覆盖原始 ArtifactVersion。
- 增加永久删除或空间回收时，需要基于 Blob 引用计数和可恢复策略设计，不能直接删除内容地址对象。
- 项目备份/恢复必须验证版本、Blob、Lineage 和 Citation 的完整图，并明确排除 API Key 与系统凭据。
- 若未来允许第三方 Tool 声明可信 Citation，必须引入签名来源或独立验证器，不能放宽当前知识工具白名单。
