# SciAide

面向科研工作者的本地优先桌面 AI Agent，支持自定义模型、工具调用、MCP、Skill、科研知识库和可信引用。

当前阶段：**P7.1～P7.6 已完成；下一阶段为 P8 发布加固**。

## 当前能力

- Wails + React + TypeScript
- Application / Port / Adapter 边界
- SQLite 版本化迁移与 Project / Conversation / Message / Run 持久化
- 项目级科研 Artifact：不可变版本、SHA256 内容寻址对象、Run/Message/Tool/Skill 来源快照、可信引用快照、完整性校验与回收站
- ArtifactVersion 可派生确定性 DOCX/PDF，支持 GB/T 7714-2015 与 APA 7；导出记录不可变且不覆盖原件，相同来源和选项幂等复用
- 默认数据根目录 `~/.sciaide`，旧 AppData 数据首次启动安全迁移
- 默认托管 Workspace 与用户自选外部目录；支持安全移除项目/会话
- 项目附件、解析缓存和 Artifact 默认收敛到 `<Workspace>/.sciaide`，大体积科研数据跟随用户选择的磁盘；全局 Skill/MCP/SQLite 仍保存在 `~/.sciaide`
- 统一错误、事件 Envelope 和日志脱敏/轮转
- Windows Credential Manager 密钥隔离，前端只显示状态和掩码
- OpenAI-compatible 流式 Provider、连接测试、错误分类和安全重试
- 一个 API 配置（Base URL + Key）可保存多个模型；支持 `/v1/models` 多选、手动添加和聊天时切换
- 多轮聊天、停止生成、Usage、RunEvent 与 Snapshot 恢复
- P2 工具协议基线：ToolCall/ToolResult 状态机、参数 Schema 校验、审计事件与持久化
- P2 权限基线：统一 ToolRegistry、精确资源 PolicyEngine、逐项 Approval、作用域 Grant 与重启恢复
- P2 执行基线：有界 ToolExecutor、取消/超时/panic 隔离、Workspace PathGuard、列目录与 UTF-8 文本读取工具
- 支持选择或拖放 PDF、DOCX、XLSX、TXT、Markdown、CSV/TSV；PDF 保留页码并整理碎片换行、英文断词及重复页眉页脚，DOCX 保留标题层级、章节路径、列表、表格行和 OpenXML 元数据
- 聊天支持 JPEG、PNG 和 WebP 原生图片输入；当前模型明确拒图后，宿主级识图兜底按优先级调用用户配置的视觉渠道，再把不可信图片描述回交原文本模型
- 项目不内置识图模型、端点或密钥；用户可在“模型与 API → 识图兜底”添加、测试、启停和删除自定义协议、Base URL、Model ID 与 API Key，密钥只存 Windows Credential Manager
- 内置附件列表、文档检查、按定位读取和搜索工具；附件以消息 `media` part 持久化，解析缓存可从 SHA256 原件重建
- 聊天附件默认只供当前对话读取；顶部独立知识库窗口支持显式导入、查看状态和移出索引，`builtin.knowledge.search` 只跨已加入知识库的文献检索
- Document、ImportJob 与 IndexVersion 元数据保存在全局 SQLite，Chunk 正文和词法索引位于 `<Workspace>/.sciaide/cache/knowledge`；删除派生缓存后可从项目附件重建
- `bounded-unit-v2` 将长页稳定拆分为最大 1,600 rune 的可定位 Chunk；中英文确定性词项进入 contentless FTS5，正文只保存一份
- BM25、标题/短语加权和单文档结果配额提升跨文献排序；知识 ToolResult 正文限制为 8,000 rune，Structured 不再重复 snippet
- 默认不使用 Embedding；用户可在项目知识库中配置 OpenAI 兼容 `/v1/embeddings`，验证成功后通过独立 IndexVersion 影子构建项目向量
- 混合检索使用 BM25、余弦相似度与 RRF，支持文档/格式过滤和重叠片段去重；Embedding 断线自动回退 BM25
- 相同查询的向量缓存在当前项目 `index-vN.db`，不保存查询明文；每个索引版本最多保留 512 条并按 LRU 清理
- `builtin.knowledge.search@3` 为片段返回绑定 Run、IndexVersion、Chunk 和原文 SHA256 的稳定 `[K-...]` 标记；最终回答只持久化通过工具来源与证据快照校验的实际使用引用，正文、引用和 Run 完成状态原子提交
- 聊天中的已验证引用可点击查看来源、页码/段落/Sheet 定位、原文和证据哈希；伪造、变形或跨 Run 标记不会显示为可信引用
- P5.1 v1 在 v2 影子构建期间继续可用，只有全部 ready 文档完成并校验后才原子切换
- 知识库展示等待、解析、分块/向量化、提交等任务阶段及解析质量诊断；支持取消、显式重试和单文档重建，重建完成前继续查询上一份可用索引
- 固定中英文科研语料同时覆盖 BM25 与确定性混合检索，持续校验 Hit Rate、Recall、MRR 和来源定位率；扫描型 PDF 只提示缺少文本，当前不内置 OCR
- 公共科研数据库 Connector 接入 OpenAlex、Crossref、arXiv、PubMed、Europe PMC 和 Semantic Scholar；模型始终使用固定 `Catalog / Search / Fetch` 工具面，来源增加不会扩大 Tool Schema
- 项目级文献发现支持多源部分失败、保守去重、纳入/排除/笔记，以及开放全文或明确披露为元数据/摘要的知识库导入；在线命中本身不会取得可信引用身份
- 每个候选文献具有规范书目、字段级来源、冲突选择和用户修订历史；证据矩阵支持研究问题、方法、样本/数据集、结论、局限和笔记，并将模型证据强制置为待复核
- Message 与 Artifact Citation 保存生成时的规范书目和证据等级快照；GB/T 7714-2015 与 APA 7 从该快照渲染，历史 ArtifactExport 不会因后续书目修订而改变
- P3 MCP：stdio / Streamable HTTP 配置、显式信任、initialize 与 Tools/Resources/Prompts 能力发现
- 兼容 Claude Desktop、Cursor、Codex 常见的 `mcpServers` JSON，可一次粘贴并导入多个 Server
- MCP Tool 使用稳定的 `mcp.<namespace>.<tool>` 名称进入统一 ToolRegistry、Plan/Full Access 审批和 ToolExecutor
- MCP stdio 仅继承最小环境；SecretEnv 明文保存在 Windows Credential Manager；Resource/Prompt 不自动注入上下文
- Anthropic thinking/signature/redacted_thinking 与 Responses reasoning/encrypted content 按 Provider Turn 不可变持久化，并在工具结果后严格回放；原始协议状态不进入聊天 Snapshot
- 推理证据区分“参数已接受”和“已观察到思考”，支持 reasoning token 汇总、默认折叠的安全状态卡，以及不拆分原生推理/工具协议组的上下文压缩
- 每个模型独立保存上下文窗口及 `provider/manual/builtin/fallback` 来源；运行时使用 95% 有效预算和不高于 90% 的自动压缩阈值，普通 `/v1/models` 缺少元数据时明确回退 200K
- 超长会话先生成无工具的结构化科研 checkpoint，再以“已校验摘要 + 最近完整对话组”继续；checkpoint 带消息边界、revision 和 SHA256，原始聊天记录不删除
- 聊天框输入 `/` 可筛选并执行本地命令；`/mcp`、`/skill`、`/knowledge`、`/model`、`/reasoning`、`/permission`、`/status` 和 `/usage` 进入运行时二级面板，本地命令不会作为消息发送给模型
- `/reasoning` 与 `/permission` 可快速切换当前会话的思考强度和工具权限；`/status` 只读汇总模型、上下文、checkpoint、MCP、Skill、知识库和识图兜底状态
- `/mcp` 按 Server 展示真实启动/关闭状态、Tools/Resources/Prompts，并可直接连接或断开；`/skill` 只列出可作为入口且允许加载的动态 Skill，选择后插入 `Use the <name> skill:` 供模型显式加载
- `/compact` 显式生成并持久化可校验 checkpoint，运行中的会话不会并发压缩
- `builtin.shell.execute` 与 `builtin.python.execute` 通过统一 Registry、Schema、Policy、审批、取消和审计管道运行；Windows 使用 Job Object 约束完整进程树，stdout/stderr 有界返回但持续排空，应用密钥不下传
- 本地执行是用户授权后的当前账户进程，不是强安全沙箱；工作目录、脚本和声明产物受 Workspace/链接边界校验，但获准代码仍可能主动访问其他本机路径或网络
- 每个项目可创建独立 Python 虚拟环境并固化解释器、依赖锁、`pip freeze` 和环境指纹；依赖安装使用 `builtin.python.environment.install` 单独审批，环境更新采用暂存重建和原子替换
- 项目 Python Kernel 串行保持变量与导入，支持 stdout/stderr、JSON、表格、异常和 Matplotlib PNG；声明输入只读、输出限定 `analysis-output/`，成功和异常均生成复现哈希，默认 1 GiB 进程树内存预算并在 15 分钟空闲后回收
- 内置“XLSX 清洗与描述统计”Workflow 使用项目 Kernel 和 Python 标准库读取首张工作表，生成清洗 CSV、统计 CSV、SVG 图及可独立重放脚本；输入、环境、代码和四类产物均进入哈希与 Artifact 来源链，不要求预装 pandas/openpyxl
- Shell、一次性 Python 和项目 Kernel 获准执行后默认允许联网，不要求域名白名单或额外网络审批；`pip install` 仍因改变项目环境单独确认，模型 API Key 与 MCP Secret 不注入子进程
- 可选 Workflow Studio 支持版本化 JSON Schema、静态图预览、不可变编译快照、Tool/MCP/Shell/Python/人工决策节点、逐步审批、暂停/恢复/取消、重启恢复和副作用未知结果确认；Run 详情展示冻结环境 Manifest、Kernel 复现哈希及 Citation/分析产物/报告/正式导出的产物图谱，普通聊天继续直接使用 Agent Loop
- 内置参考科研 Workflow 串联公共数据库检索、人工候选筛选、材料导入、知识索引、本地证据检索、人工 Citation 选择、Python 分析、CSV/SVG Artifact、Markdown 报告以及 DOCX/PDF 正式导出
- 完整助手回答可显式保存为 Markdown Artifact；Workspace 文件可登记或追加为新版本；Tool 只有显式声明 `workspacePath` 时才自动登记产物，普通附件引用不会误入产物库
- 科研产物窗口可预览 Markdown、PDF、DOCX、XLSX、CSV/TSV 的标题、段落、代码和表格结构，并按历史版本生成、查看和下载正式导出
- OpenScience 动态 Skill 体系：311 个默认 Skill 及 `LICENSE`/`NOTICE` 共 1,624 个文件只读嵌入 EXE；逐文件 Manifest 固定 OpenScience `2.0.31` 的路径、大小和 SHA256，启动时不复制到用户目录
- Skill 仅使用带 frontmatter 的 UTF-8 `SKILL.md`；来源解析优先级固定为 `project > user > installed > default`，同名高优先级来源覆盖低优先级来源并显示覆盖关系
- Project 来源扫描 Workspace 的 `.openscience/`、`.synsc/` 及 OpenScience 配置中的相对 `skills.paths`；当前明确不扫描或兼容 `.claude/skills`
- 新 Run 只注入分类摘要、固定科研路由和有界重排候选，不预注入任何 Skill 正文；中英文请求与 Skill 元数据对称映射到科研概念，支持英文词形归一化、明确否定和可选 `routing-aliases` 双语扩展，再结合最近三条用户任务与同会话最近加载项召回最多 20 项、向模型展示 16 项，模型最终决定是否调用 `builtin.skill.load`
- 路由候选、分数组成、连续性来源、输入 SHA256 与实际加载结果按 Run 审计；版本化 320 条中文/英文/混合评测门禁当前达到 Recall@16 `98.5%`、MRR `0.834`、无关请求误召回 `0%`、否定误命中 `0%`
- `builtin.skill.load` 可浏览分类或按精确名称渐进加载正文；首次加载会把完整正文、来源和内容/全包 SHA256 写入 `run_dynamic_skills`，后续分页与工具续轮复用同一不可变快照
- 短 Skill 正文完整加载；长 Markdown 先返回章节目录并按稳定章节读取，完整正文仍形成不可变 Run 快照；无标题长文保留有界分页兜底
- `builtin.skill.resource.list` 先列出本 Run 已加载 Skill 的附属资源类型、大小和文本可读性，`builtin.skill.resource.read_text` 再读取哈希匹配包内的规范相对路径 UTF-8 文本；二进制 asset 只展示元数据，脚本源码绝不由 Skill 机制自动执行
- `allowed-tools` 只与实时 Registry 做能力诊断并显示可用/MCP/缺失，实际权限仍由具体工具决定；新 Run 使用当前任务、最近用户上下文、中文科研别名和最近真实加载 Skill 做有界召回，再由主模型重排
- 管理页支持搜索、分类/来源/能力筛选、逐项允许加载、全开/全关、目录刷新、User Skill 创建/编辑/可恢复删除，以及经本地审查、固定 commit SHA 和警告二次确认的 Git 安装；P7.3 v2 能力审计分为原生可用 4、需要本地依赖 200、需要外部服务 87、当前不可用 20
- 每个当前生效的 Default/Installed/User/Project Skill 都可在管理页查看完整包目录树并逐文件浏览；UTF-8 文本显示只读源码，二进制或超大文件只显示元数据，磁盘包与目录哈希漂移时失败关闭并要求刷新
- 旧版 `$skill-id`、关键词 trigger、项目版本绑定、依赖/冲突协调和两个 SciAide 内置 Skill 已退出新 Run 的生产路径；旧 Run 继续只读恢复原不可变快照，Artifact 与项目归档同时兼容旧/新 Skill provenance
- 项目可导出为版本化 `.sciaide-project` 无密钥归档，并默认恢复为新的托管项目；归档包含项目关系及附件、知识索引和 Artifact 真实对象，不包含 API Key、MCP 配置/Secret、识图或 Embedding 凭据和权限授权
- 可脚本化 `FakeChatModel`、Provider Fixture 测试、威胁模型、ADR 和 CI

## 开发

新会话或接手开发请先读 [`docs/CURRENT_STATE.md`](docs/CURRENT_STATE.md)。依赖安装和命令见 [`docs/development.md`](docs/development.md)，完整架构与阶段门禁见 [`start.md`](start.md)。
