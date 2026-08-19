# Changelog

## [Unreleased]

## [0.3.0] - 2026-08-19

### Added

- 升级 PDF/DOCX 结构解析：PDF 在稳定页码边界内合并碎片行、恢复英文断词、识别章节并清除重复页眉页脚；DOCX 解析标题层级、章节路径、列表、表格行和核心元数据。
- 文档解析缓存升级至 schema v2，旧缓存按附件 SHA256 原件懒重建，知识库通过新 ParserSchemaVersion 影子索引切换；不引入 OCR 或新增运行依赖。
- 完成 P5.4 Run 绑定的可信引用：知识片段返回绑定 Run、IndexVersion、Chunk 和原文 SHA256 的稳定 `[K-...]` 标记；同一 Chunk 的不同片段不再共用标记，Assistant Message、证据快照和 Run 完成状态原子持久化。
- 聊天回答将已验证引用渲染为可点击编号，展示来源文件、页码/段落/Sheet 定位、原文和证据哈希；伪造或跨 Run 标记明确显示为未验证。
- 模型与 API、MCP、Skills、用量统计和知识库窗口随可用视口自适应放大，并统一提高详情正文、标签、表格及表单控件的可读尺寸。
- 启动窗口按当前屏幕的 DPI 感知逻辑尺寸自适应缩放：1920×1080 保持 1280×800，QHD 同比放大，并限制在 960×640 到 1920×1200 后居中显示。
- 完成 P5.3 可选 Embedding：默认保持 FTS5/BM25，可在知识库窗口配置并验证 OpenAI 兼容 `/v1/embeddings`。
- 向量按项目保存在 `index-vN.db`，IndexVersion 隔离 Base URL、Model ID、实际维度和混合策略；API Key 仅保存到系统凭据库。
- 增加 BM25/余弦相似度 RRF 混合排序、Document ID/格式过滤和重叠片段去重；查询接口不可用时自动回退 BM25。
- 增加项目级查询向量缓存：相同搜索词复用已有向量，过滤条件变化不重复请求；只保存 SHA256，按 IndexVersion 隔离并限制为 512 条 LRU。
- 增加模型 API 错误详情：聊天错误条可展开查看错误码、协议、模型、HTTP 状态和脱敏后的 Provider 响应载荷。
- 增加项目知识库管理窗口：可显式导入、查看索引状态/Chunk 数并移出知识库；移出索引不会删除附件原件或历史消息。
- 增加 P5.2 `bounded-unit-v2` 稳定分块：目标 1,200 rune、最大 1,600 rune、80 rune 重叠，并持久化原 Unit 内的起止范围。
- 增加项目本地 contentless FTS5/BM25；英文/数字/科研标识符与中文双字词项使用同一确定性规范化流程，单字符查询安全回退。
- 增加 v1→v2 影子索引：构建、任务与本地附件覆盖全部通过后才激活新版，旧版在切换前持续提供搜索。
- `builtin.knowledge.search@2` 增加标题/短语加权、每文档结果配额、命中词和源范围，并把模型可见正文限制为 8,000 rune。
- 增加 P5.1 项目级知识索引：Document、ImportJob、IndexVersion 元数据以及跟随 Workspace 的 Chunk 缓存。
- 增加 `builtin.knowledge.search`，可在当前项目多篇已导入文献中统一检索，并返回稳定 Attachment ID、原文件名、片段分数和页码/段落/Sheet 定位。
- 增加知识索引后台队列、启动恢复、既有附件懒补建和项目缓存删除后重建；单篇文档索引使用事务替换，跨项目复合外键失败关闭。
- 增加 P4.6 项目附件基线：文件选择与 Wails 原生拖放、消息 `media` part、SHA256 去重、重启恢复和项目本地持久化。
- 增加 PDF 分页、DOCX 段落/表格、XLSX Sheet/行/公式以及 TXT/Markdown/CSV/TSV 的本地解析兜底；解析缓存删除后可由附件原件重建。
- 增加 `builtin.attachment.list`、`builtin.document.inspect`、`builtin.document.read` 和 `builtin.document.search`，返回有界内容及页码/段落/Sheet 定位引用。
- 增加 `literature-reading@1.1.0`，使用附件文档工具读取论文并保留真实定位符；旧版本和同版本用户包仍不会被静默覆盖。
- 增加每模型上下文窗口、自动压缩阈值和能力来源；支持从兼容模型目录读取多种可选窗口字段，并在元数据缺失时明确使用 200K fallback。
- 增加会话级压缩 checkpoint：结构化科研连续性摘要、精确消息边界、递增 revision、来源计数、模型/协议归属和 SHA256 完整性校验。
- 增加 P4.1 Skill 基础协议：严格解析 `skill.yaml` 与 `SKILL.md`，支持 SciAide 版本约束、Tool 依赖、权限声明和上下文预算。
- 增加全局版本化 Skill 目录扫描、Manifest/内容/全包 SHA256、完整性状态，以及项目级具体版本启用关系的 SQLite 持久化。
- 增加 Skill Wails 后端 Facade，并在启动时同步 Skill 目录与完整性状态。
- 增加 P4.2 本地文件夹和 ZIP Skill 安装，使用随机暂存、严格压缩包预检、原始来源归档及原子目录激活。
- 增加 Skill 显式同版本替换、可恢复卸载、项目引用保护和项目级低版本回滚 API。
- 增加 Skill 来源类型、原文件名、来源 SHA256 和私有归档相对路径持久化。
- 增加 P4.3 Run 级有界 Skill catalog、`$skill-id` 显式选择和确定性 suggest trigger；仅选中 Skill 完整读取 `SKILL.md`。
- 增加不可变 `run_skill_contexts` 快照，并与 `run_skills` 审计在首次模型请求前原子写入；审批恢复和同一 Run 的工具循环复用相同内容。
- 增加 Run 绑定的 `builtin.skill.resource.read_text` 渐进资源读取；仅可读取已选中 Skill 的安全包内 UTF-8 相对路径，并在包替换/卸载后校验来源归档继续复用原版本。
- 显式 Skill 未知、未启用或不可用时写入不可变选择状态而非静默忽略；有界 catalog 始终明确标记被省略项。
- 增加 P4.4 Skill 管理界面：按 Skill ID 聚合多版本，展示完整性、可用性、来源哈希、激活规则和 Tool 依赖诊断。
- 支持通过原生文件/目录选择器安装文件夹或 ZIP Skill，并提供同版本内容替换、项目引用卸载的明确二次确认。
- 支持当前项目固定 Skill 版本、启用/禁用、调整优先级和回滚到已安装且可用的更低版本。
- 增加 P4.5 内置 `literature-reading@1.0.0`，按可复核证据提取研究问题、设计、结果、局限和多文献差异，禁止补造引文与定量结果。
- 增加 P4.5 内置 `academic-writing@1.0.0`，支持学术文本起草、重写、润色、分区审查和审稿回复，并保持引用、数据与科研诚信边界。
- 内置 Skill 原件只读嵌入二进制，启动时复用普通 Skill 的暂存、校验、归档与原子安装流程；同版本用户内容不会被静默覆盖。
- 增加 `builtin` Skill 来源类型和迁移；内置原版可按项目禁用或显式替换，但不能直接卸载。
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

- 文档读取和单文档搜索结果现在携带章节路径，并对同页多章节单元的 Citation locator 去重，避免重复工具上下文。
- 可信引用详情默认整理 PDF 提取产生的碎片换行与中文标点空格，并保留“原始文本”切换；数据库 Quote 和证据 SHA256 不做改写。
- 聊天框附件与项目知识库改为独立路径：聊天附件仅解析供对话读取，不再自动建立跨会话索引；缓存重建和版本升级只处理显式知识库成员。
- 知识检索 snippet 只保留在 ToolResult Text；Structured 改为紧凑的 ID、定位和排名元数据，避免同一正文重复消耗模型 Token。
- 项目大体积数据不再默认进入系统盘全局缓存；统一使用 `<Workspace>/.sciaide/{attachments,cache,artifacts,tmp}`，旧根级 marker 在项目 ID 匹配时迁移。
- Run 现在固化原始窗口、95% 有效预算和 90% 自动压缩阈值；ContextBuilder、Skill 预算、审批恢复和工具循环复用同一快照。
- 历史对话按完整 Run 组裁剪；正式请求前必须先把被移除前缀纳入持久化 checkpoint，压缩失败或无法推进时停止请求而不是静默丢失历史。
- 已安装 Skill 的文件发生变化时，目录刷新不再静默覆盖已记录哈希；包会进入不可用状态，等待后续显式安装/升级流程确认。
- Skill 安装、刷新、项目版本切换和卸载在应用服务内串行化；数据库失败时回滚文件激活或恢复卸载目录。
- Skill 正文作为 contextual user 内容而非系统权限注入；Skill Manifest 权限不能放行 ToolCall、改变 Plan/Full Access 或绕过 Workspace 边界。
- 选中 Skill 正文改为当前 Turn 的 contextual fragment：位于压缩后的历史消息之后、本轮用户消息之前；Codex 风格兼容明确为安全导入/渐进文本资源兼容，不等于允许直接执行任意脚本。
- stdio 参数不经过 Shell，子进程仅继承最小 allowlist 环境；远程 MCP 默认要求 HTTPS，HTTP 仅允许回环地址。
- MCP Resource/Prompt 只发现和展示，不自动注入模型上下文。
- `Plan` 对每个 ToolCall 只审批一次；`Full Access` 自动放行已注册且通过工程边界校验的工具。
- 风险等级仅用于提示和审计，不再限制用户选择；历史 PermissionGrant 不参与运行时决策。
- 拒绝审批后将普通 denied ToolResult 回填模型，不由程序补写或改写模型回答。

### Fixed

- 修复模型选择只保存在前端内存、重启后回到默认第一项的问题；现在按会话持久化 API 配置与模型，旧会话从最近一次 Run 自动回填。
- 修复 Responses `response.failed/error` 仅显示“请求未完成”的问题，兼容顶层与嵌套错误结构，并为三种模型协议统一保留有界诊断详情。
- 修复默认 200K 上下文因 HTML 数字输入步进基准错误而无法保存，并提高模型 API Key 输入框的内容字号。
- Snapshot 仅更新其所属会话，避免迟到轮询污染已切换的研究会话。
- 审批恢复按 Approval ID 去重，并阻止旧的终态 Run 被误当作当前活动 Run 执行 steer。
