# SciAide 当前开发状态

> 更新日期：2026-08-26
> 代码基线：`v0.5.1` 发布源码，包含 P6、动态 Skill 与 P7.1～P7.6；修复 `v0.5.0` 在英文 Windows CI 下的科研产物复现编码、默认 Skill 来源清单换行和本地进程测试稳定性问题
> 用途：供新会话和开发者快速恢复上下文；长期架构与阶段门禁仍以 [`start.md`](../start.md) 为准。

## 如何使用本文档

新会话不需要先遍历整个仓库。建议按以下顺序接手：

1. 阅读本文档，确认当前阶段、约束和未完成范围。
2. 运行 `git status --short` 与 `git diff --stat`，确认本文档之后是否又有改动。
3. 阅读 [`start.md`](../start.md) 中与当前任务相关的章节。
4. 阅读 [`CHANGELOG.md`](../CHANGELOG.md) 的最新发布记录、`Unreleased` 和对应 [`docs/adr`](adr) 决策。
5. 最后使用 `rg` 定向定位代码和测试，不要无目的遍历整个仓库。

当文档与程序行为冲突时，可信度顺序为：当前运行行为、自动化测试、当前工作区代码、本文档、ADR、`start.md` 路线图。路线图描述目标，不代表功能已经实现。

## 当前阶段

- P0～P4.6 已完成：工程基线、聊天与 Agent Loop、权限和工具、MCP、多协议模型、Skill、项目附件与本地文档读取。
- P5 已完成：项目知识库、FTS5/BM25、可选 Embedding 混合检索、可信引用、任务运维、解析诊断和固定语料评测。
- P6.0 已完成：斜杠命令及运行时二级面板、手动压缩、聊天界面与流式渲染加固。
- P6.1 已完成：可信 Artifact 核心、不可变版本、内容寻址对象、来源/引用快照和最小产物管理闭环。
- P6.2 已完成：不可变派生 DOCX/PDF 导出、GB/T 7714-2015 与 APA 7 引用渲染，以及 Markdown/PDF/DOCX/XLSX/CSV 结构化预览。
- P6.3 已完成：OpenAlex、Crossref、arXiv、PubMed、Europe PMC 和 Semantic Scholar 公共 Connector，固定 Catalog/Search/Fetch 工具面及统一网络治理。
- P6.4 已完成：多源文献发现、保守去重、筛选记录、开放全文或披露型元数据附件，以及复用现有 Attachment/Knowledge 队列的本地知识化闭环。
- P6.5 已完成：规范书目、字段级来源与修订历史、证据矩阵、审核状态和完整引用渲染。
- P6.6 已完成：版本化无密钥项目归档与隔离恢复。原版本绑定式多 Skill 协调随后被 OpenScience 动态 Skill 路径取代，只保留旧 Run/归档兼容读取。
- P6 已收尾：形成“公共数据库发现 → 筛选 → 本地知识化 → 可信引用 → Artifact → 正式导出 → 项目恢复”的完整科研闭环。
- P7.1 已完成：受统一权限与审计管道约束的 PowerShell/CMD/Python 一次性本地执行、Windows 进程树清理和显式 Artifact 登记。
- P7.2 已完成：新建虚拟环境位于 `<Workspace>/.sciaide/python/venv`，也可只绑定用户已有 venv；依赖锁和指纹、持久 Kernel、结构化结果、路径无关复现哈希、输出回滚、进程树内存治理，以及真实 CSV/TSV、XLSX 清洗/统计/绘图/脚本 Workflow 均已落地。旧版 `%USERPROFILE%/.sciaide/data/python-envs/<project-id>` 环境继续兼容读取，但新建不再写入全局目录。
- P7.3 已完成：默认 Skill 派生转换、真实执行边界、资源物化和 311 项 v2 能力重审。
- P7.4 已完成：版本化 Workflow Schema、静态编译、不可变 Tool 快照、模板和 Studio 编辑预览。
- P7.5 已完成：可检查点恢复的 Workflow Run/Step 状态机、审批、人工决策、暂停/恢复/取消、重启恢复和副作用确认重试。
- P7.6 已完成：参考科研 Workflow 已串联检索、筛选、知识、Citation、Python 分析、Artifact、Markdown 和 DOCX/PDF 导出；Run 详情同时展示冻结环境 Manifest 和产物图谱，全量门禁与正式 EXE 隔离验收已通过。
- P8～P9 尚未开始。

## 已实现能力

### 应用与数据

- Go + Wails + React + TypeScript 桌面架构，使用 Application / Port / Adapter 分层。
- Project、Conversation、Message、Run、模型配置、MCP、Skill 和审计状态持久化到版本化 SQLite。
- API Key 存入 Windows Credential Manager，前端和普通配置只保留掩码或引用。
- 默认数据根为 `%USERPROFILE%\.sciaide`；用户可以让项目使用任意外部 Workspace。
- 项目附件、解析缓存和知识索引跟随 Workspace，避免大体积科研数据固定占用系统盘。
- Artifact 元数据保存在 SQLite；真实字节按 SHA256 存在 `<Workspace>/.sciaide/artifacts/objects`，版本一经创建不可更新。

### 模型对话与 Agent

- 支持 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 三种协议。
- 支持流式回答、停止、错误详情、安全网络重试、工具续轮、上下文预算和 checkpoint 压缩。
- 支持模型级上下文窗口配置，普通模型元数据缺失时回退到 200K；达到阈值后保留可校验科研摘要和最近完整对话。
- 思考强度采用 `low/medium/high/xhigh/max`，按协议和运行反馈做懒验证与逐档回退。
- Responses 原生 reasoning、Anthropic thinking/signature/redacted thinking 和工具协议项按 Provider Turn 持久化并原样回放，避免工具续轮破坏协议顺序。
- 聊天支持 JPEG、PNG 与 WebP 图片。当前模型首次收到原生图片内容块；HTTP 400/422 明确拒图，或 HTTP 200 正文返回 `[Unsupported Image]`/宿主不可用标记时，由宿主级“识图兜底”机制调用独立视觉渠道，仅将最小视觉事实回交原文本模型。内部渠道、像素传递和信任标签不会进入最终回答；图片内指令仍不能覆盖系统规则或用户请求。该机制不属于 Skill，也不会扫描普通 SciAide 对话模型。
- 项目不携带内置视觉模型、端点或密钥，也不扫描普通 SciAide 对话模型。用户可在“模型与 API → 识图兜底”新增、测试、启停或删除自己的视觉渠道；渠道元数据进入 SQLite，自定义 API Key 仅进入 Windows Credential Manager。未配置渠道时，文本模型明确拒图后会提示用户添加多模态模型。
- 只有 HTTP 400/422 且错误载荷明确表示仅支持文本/不支持图片时才触发回退；超时、鉴权、限流、坏图、格式和尺寸错误不会把模型误标为不支持图片。
- Run 不设置固定模型轮数、累计工具数或总运行时长上限；仍保留用户停止、单次模型请求超时、单次工具超时和上下文保护。
- 当前轮的思考、工具调用和执行步骤收敛为可展开的“已处理 + 耗时”；历史及已中断 Run 也可从对应助手消息按需加载处理记录，最终回答独立呈现。
- 用户停止会将已有草稿、最近处理活动和工具结果保存为有界的终止上下文；下一条“继续”会在新 Run 中参考这些不可信历史数据，但不会恢复原模型流、未落库的隐藏推理状态或精确 Token 位置。

### 缓存与用量

- 三协议使用稳定追加式前缀：Chat Completions 依赖供应商前缀缓存，Responses 使用会话级 `prompt_cache_key`，Anthropic 使用原生 `cache_control` 断点。
- 每个逻辑模型请求写入一条最终请求记录；内部重试不会重复记账。
- 同一 Agent 轮次中的图片能力探测、识图兜底请求和主模型回答使用不同请求 ID，分别展示真实模型、状态、耗时和 Token。
- 成功请求记录供应商返回的输入、缓存读取/创建、输出、推理、首 Token 和耗时。
- 失败、超时和用户取消记录状态码与有限错误详情，取消映射为 `499`；失败记录为零 Token，不参与 Token 与缓存命中率聚合。
- 用量窗口支持今天、近 7/14/30 天、全部、自定义范围，以及当天分钟级时间筛选、状态筛选和请求详情。

### 工具、权限与 MCP

- 所有内置和 MCP Tool 都经过统一 Registry、JSON Schema 校验、PolicyEngine、ToolExecutor、持久化和取消链路。
- `Plan` 模式读取当前 Workspace 及其子目录无需确认；越界读取、写入和其他工具调用按策略请求用户确认。`Full Access` 放行已注册且通过边界校验的工具。
- MCP 支持 stdio 与 Streamable HTTP、常见 `mcpServers` JSON 导入、能力发现、后台隐藏子进程、批量连接和应用退出清理。
- MCP 与 Codex 一样区分宿主外层超时：启动和首次能力发现固定为 30 秒，`tools/call` 按 Server 配置且默认 300 秒；工具参数中的页面加载超时由 MCP Server 自己处理。
- `/mcp` 展示并控制实际 Server 连接状态；保存或测试配置不会形成长期连接，当前需在配置页或 `/mcp` 显式连接，连接后工具才注册，应用退出时统一关闭。

### P7 本地执行、Python 与 Workflow

- 注册 `builtin.shell.execute` 与 `builtin.python.execute`。Shell 支持 PowerShell/CMD；Python 支持内联 `code` 或当前 Workspace 的 `scriptPath`，并固定使用 `-I -u -X utf8` 与参数数组。
- 两个工具均为高风险、非幂等，声明 Workspace 读写与 `process.execute`；Plan 模式逐次审批且审批卡默认展示完整参数、运行时、工作目录和 1～300 秒时间限制，Full Access 仍不能跳过 Registry、Schema 和路径校验。
- 工作目录、脚本和最多 32 个声明产物受 PathGuard 约束，拒绝绝对路径、`.sciaide` 及 symlink/junction/reparse point。只有成功退出且本轮内容新增或改变的显式声明常规文件自动登记 Artifact；失败、中断、旧内容和未声明文件不会登记。
- 子进程只继承核心环境 allowlist，排除名称包含 `KEY`、`SECRET`、`TOKEN` 的变量；Shell 不加载用户 Profile。解释器由宿主 PATH 探测，模型不能传入任意解释器路径。
- Windows 使用挂起启动、Job Object 纳管后恢复和 kill-on-close；超时、用户取消、应用退出以及根进程正常结束后都会清理整个子进程树。stdout/stderr 分别有界保留 64 KiB，达到上限后继续排空并保存总字节数及 SHA256，管道另有 2 秒 drain timeout。
- `process_execution_audits` 保存 ToolCall/Run/Project、工具、解释器路径/版本/SHA256、脚本路径/SHA256、内联命令 SHA256、工作目录、timeout、环境变量名称、PID、退出码、终止原因及输出统计；启动恢复将遗留活动记录标为 `app_shutdown`，项目归档会重映射审计关系。
- 该能力是经用户授权的本机进程执行器，不是强安全沙箱。获准代码仍具有当前账户权限，可能主动访问 Workspace 外绝对路径或网络；Workspace PathGuard 不等价于操作系统文件系统隔离。
- 网络授权采用默认允许策略：联网 Tool、Shell、一次性 Python 和项目 Kernel 的工具调用一旦获准执行，即可直接发起网络请求，不再要求逐域名审批、白名单或单独网络配置。Plan 仍确认整个高风险工具调用，Full Access 自动执行；默认联网不会向子进程注入应用密钥，依赖安装仍因改变项目环境而单独确认。
- 每个项目可从探测或用户选择的基础 Python 3 在 `<Workspace>/.sciaide/python/venv` 创建独立虚拟环境，也可绑定已有 venv；数据库保存环境归属、解释器身份、锁定包、`pip freeze` SHA256 和环境指纹。外部 venv 只验证和运行，托管环境的创建、重建、删除和中断恢复使用项目串行锁及暂存/原子替换。
- `builtin.python.environment.install` 是改变环境的独立高风险 Tool，安装声明包后重建并冻结完整依赖；Skill 不能静默执行 `pip install`。未检测到 Python 时，管理页明确提示安装 Python 3 或选择现有 `python.exe`，SciAide 不自动下载解释器。
- `builtin.python.kernel.execute` 使用项目环境和项目级串行 Kernel；变量与导入只在 Kernel 生命周期内持续，可停止、重启并在 15 分钟空闲后回收。结果包含 stdout/stderr、JSON 值、表格、异常和最多 16 张 Matplotlib PNG。
- Kernel 输入必须是当前 Workspace 常规文件并在执行期间设为只读；声明输出只能新建在 `analysis-output/`。普通输出与 Matplotlib 图片先写入 `.sciaide/tmp/kernel-<UUID>/`，验证后作为单一批次无覆盖发布；外部同名文件抢占会令本批次失败，并且只回滚文件身份仍属于本次执行的输出。环境创建、重建、安装、删除与同项目 Kernel 共用串行锁，异常、取消、超时、越界或输入变化不会留下半产物，`MemoryError` 后淘汰 Kernel。
- Kernel 默认使用 1 GiB Windows Job Object 进程树内存预算；代码、结构化输入、文件输入、环境和输出/异常快照形成稳定 `reproductionSha256`。总复现哈希按声明顺序使用内容摘要而不绑定易变文件名，完整路径到 SHA256 的映射仍独立保存在 provenance；该预算是进程资源治理，不是安全沙箱。
- 内置“数据探索与可复现分析”接受一份 CSV/TSV，用户可直接选择文件、用自然语言填写研究目标，并选择数据概览、数据质量检查或数值分布概览；固定审计脚本生成清洗 CSV、字段统计 CSV、SVG 图、方法说明 Markdown 和可独立重放 `.py`。支持 UTF-8/BOM/GB18030 和标准 CSV 引号换行，限制 20 万行、256 列、200 万单元格及单元格 10000 字符，清洗 CSV 会转义公式型单元格。
- 内置“XLSX 清洗与描述统计”接受一份 `.xlsx`，用项目 Kernel 和 Python 标准库读取首张 Sheet，生成清洗 CSV、数值描述统计 CSV、均值 SVG 和可独立重放 `.py`。XLSX 外层拒绝不安全 ZIP 路径、超大条目、超过 2000 个条目和超过 256 MiB 的解压规模，脚本内部使用与 CSV 同级的行列/单元格限制及公式转义；不需要默认安装 pandas/openpyxl。
- 所有显式 `fileKind` Workflow 输入在 Run 创建前通过 PathGuard 打开并复制到 `research-inputs/<名称>-<完整SHA256>.<扩展名>`；Run 输入哈希记录冻结路径，工具执行前再次核对文件名摘要和实际字节。源文件不修改，外部 symlink/junction、`.sciaide`、目录、路径逃逸、格式伪装、二进制 NUL 和被篡改快照均失败关闭。旧内置分析方案通过精确定义哈希获得兼容输入向导；旧版本已创建但未冻结输入的 Run 不会悄悄读取可变源文件，需重新发起。
- Workflow 是“科研模式”内部的可选确定性执行能力，普通聊天继续使用 Agent Loop。主工作区提供“自由对话 / 科研模式”显式切换；文献发现、知识库、Python 环境与科研产物是项目级公共能力，在两种模式的顶部均可进入。科研模式采用中性浅色实验工作区、石墨标题带和电蓝/青色状态强调，正文、阶段、按钮和运行证据使用可读字号；选择参考方案时保留稳定选中态并明确提示“预览不等于运行”。采用方案后自动进入连续任务配置：选择数据、填写目标/方法、检查 Python 状态、启动并处理授权、查看进度，完成后可直接打开科研产物。JSON、Tool、Schema 和原始快照只在高级设置或折叠技术记录中展示。底层仍支持不可变版本、运行历史、审批、候选/Citation 人工选择、暂停/恢复/取消和事件审计；任务详情从已提交 Step Output 推导冻结环境 Manifest 和 Citation/分析产物/报告/正式导出的产物图谱，不从当前电脑状态猜测历史证据。
- Workflow Port 使用显式 `fileKind`、`control`、`minItems/maxItems` 描述输入能力，运行时和前端都不再按 `input_paths`/`analysis_request` 名称猜测通用自定义方案。保存、文件选择、启动、审批、人工决定和重试使用同步事务锁；方案列表、版本、Run、轮询和 Python 前检使用独立请求门禁，旧响应、旧错误和旧 `finally` 不会覆盖当前选择。
- Workflow 创建前拒绝循环、类型不匹配、未知/变化 Tool、路径逃逸、嵌套密钥和超大图；每个执行节点仍进入现有 ToolRegistry、PolicyEngine、Approval、ToolExecutor 和 Artifact 管道。
- Runtime 只自动恢复未提交的幂等步骤；已提交步骤不重放。非幂等步骤在崩溃后进入 `outcome_unknown`，必须由用户确认副作用后才能重试；人工决策和审批并发提交只有一个成功。
- 成功 ToolResult 在落库前把声明产物冻结到 `.sciaide/artifacts/objects/<sha256>`，并校验工具声明的大小、MIME 与 SHA256；Artifact 登记或应用重启只能读取该不可变对象，不会重新采用 Workspace 路径中的新字节。
- 参考模板实现“公共数据库检索 → 人工筛选 → 导入材料 → 知识索引 → 本地证据检索 → 人工 Citation → Python 分析 → CSV/SVG Artifact → Markdown 报告 → DOCX/PDF 导出”，沿用 P6 不可变来源链。

### Skill

- 当前生产路径是 OpenScience 风格动态 Skill，不再使用旧 `$skill-id`、关键词 trigger、项目版本绑定、优先级/回滚或依赖/冲突预协调。旧 P4 生产实现已收缩为历史 DTO、严格哈希解码/渲染、只读加载、项目归档恢复兼容和一次性退休包归档；不能再创建旧安装/选择状态。
- OpenScience 默认目录包含 311 个 Skill、`LICENSE`/`NOTICE` 在内共 1,624 个文件和约 20.75 MB 内容，只读嵌入 EXE；逐文件 Manifest 固定 OpenScience `2.0.31` 的路径、大小和 SHA256。默认内容不写入 `%USERPROFILE%\.sciaide`，用户可在 Skills 管理页通过只读目录树查看当前生效包，无需从 EXE 解包。
- 目录来源优先级固定为 `project > user > installed > default`。Project 扫描 `.openscience/`、`.synsc/` 及配置中的相对 `skills.paths`；User 与 Git Installed 使用独立目录；当前不扫描 `.claude/skills`。
- 每个新 Run 根据当前用户消息对 `name/category/description/tags/routing-aliases` 评分，最多召回 20 个、向模型提供 16 个候选和必要的分类/科研路由提示；模型负责最终语义判断，Skill 正文不会预先占用上下文。
- 模型调用 `builtin.skill.load` 后才渐进取得正文。第一次成功加载会将完整正文、来源、分类、内容 SHA256 和全包 SHA256 保存到 `run_dynamic_skills`；同一 Run 后续分页、工具续轮、审批恢复和压缩均复用该不可变快照。
- `/skill` 只列 `entry=true` 的动态 Skill，选择后插入 `Use the <name> skill:`；显式选择会要求模型先无旁白调用 loader，再处理用户请求。
- 短 Skill 正文一次完整加载；长 Markdown 先返回最多 256 个互不重叠的章节索引，再以稳定 `section-N` 加载所需章节；无标题长文使用有界分页兜底。两种方式都保存完整正文与 SHA256 的 Run 快照。
- `builtin.skill.resource.list` 列出当前 Run 已加载 Skill 的附属资源路径、reference/asset/script 分类、大小、媒体类型和文本可读性；`builtin.skill.resource.read_text` 再读取规范相对路径 UTF-8 文本，并要求当前包仍与 Run 哈希一致。二进制 asset 可发现但不会伪装成文本，脚本只作为文本读取，Skill 机制没有脚本执行入口。
- `allowed-tools` 在加载时与实时 Tool Registry 做能力诊断，明确显示已匹配、MCP 提供或缺失；它不改变工具自身的权限要求，也不会自动执行工具。
- 新 Run 将请求与 Skill 元数据对称映射到同一中英文科研概念，执行常见英文词形归一化并排除明确否定的任务概念；未知领域可在 frontmatter 通过 `routing-aliases` 声明双语说法。当前消息、最近三条用户任务和同会话最近真实加载的 Skill 最多召回 20 项，再向主模型暴露 16 项重排；最近 Skill 只用于连续性召回，不自动加载。显式选择始终置顶并进入审计；首次生成的路由提示以 SHA256 绑定到 Run，工具续轮及审批恢复均复用同一不可变快照。
- 每个首次路由同时结构化保存候选分数、当前/最近分量、否定、连续性来源、显式选择、短名单、输入 SHA256 和候选不可变哈希；读取时关联实际 `run_dynamic_skills`，项目级指标可统计实际加载率和短名单加载率，数据库篡改会失败关闭。
- 320 条版本化路由评测覆盖中文、英文、中英混合、否定和无需 Skill；当前 Recall@16 为 `98.5%`、MRR 为 `0.834`，中文/英文/混合召回分别为 `99.1%`/`97.2%`/`100%`，无关请求误召回及否定误命中均为 `0%`。
- 管理页支持搜索、分类/来源/能力筛选、允许加载开关、全开/全关、刷新、User Skill CRUD 和 Git 安装/卸载。P7.3 v2 能力矩阵为原生可用 4、需要本地依赖 200、需要外部服务 87、当前不可用 20、未审计 0；Git 安装固定 commit SHA，执行路径/体积/链接/危险模式审查，警告必须针对同一 SHA 二次确认。
- 238 个执行型默认 Skill 明确要求“资源物化 → 用户/模型检查 → 正式 Python/Shell Tool → 正常审批”，并指向单独审批的项目依赖安装；同步转换会拒绝自动注入上游凭据、自动计费和不存在的托管 API，未知工具只披露为用户配置的 MCP/API 缺口。
- Default、Installed、User 与 Project Skill 均可查看当前有效包的完整目录树并逐文件浏览；UTF-8 文本显示完整只读源码，二进制或超过 2 MiB 的文件只显示路径、MIME 与大小。UI 只接收规范包内相对路径，不暴露磁盘绝对路径；磁盘包哈希漂移会拒绝返回混合内容并要求刷新目录。
- 旧 `internal/app/skill`、`internal/skillpkg` 与 `run_skill_contexts/run_skills` 仅用于历史 Run、旧项目归档和数据库兼容；新 Run 不再进入该选择路径，旧快照保持原字节。Artifact provenance 合并旧/新来源并用 `origin/dynamic` 区分。

### 附件与知识库

- 聊天附件默认只供当前对话读取，不会自动加入知识库；知识库窗口负责显式导入、移除、重试和重建。
- 本地文档解析支持 PDF、DOCX、XLSX、TXT、Markdown、CSV 和 TSV；聊天图片支持 JPEG、PNG 和 WebP，并按经过校验的真实内容确定 MIME 类型，因此 WebP 即使使用 `.jpg` 文件名也能安全导入；图片不进入文档文本解析、`builtin.document.*` 或知识库索引，文档工具误调用不会改变图片就绪状态。
- PDF 保留页码并清理碎片换行、断词和重复页眉页脚；DOCX 保留标题层级、章节路径、列表和表格行。
- 知识库默认使用项目级 FTS5/BM25；配置 `/v1/embeddings` 后构建独立向量影子索引并使用 BM25 + 向量 + RRF 混合检索，Embedding 失败自动回退 BM25。
- 查询向量缓存在当前项目的 `index-vN.db` 中，按索引版本隔离且不保存查询明文。
- 引用绑定 Run、IndexVersion、Chunk 和原文件 SHA256；最终回答只展示经过证据快照校验的可点击引用。

### 文献发现、书目与证据

- 内置 OpenAlex、Crossref、arXiv、PubMed、Europe PMC 和 Semantic Scholar 六个公共 Connector；这些主要使用公开、免 Key 或可选 Key API，不代表与文献提供商存在商业合作。
- 模型只看到固定的 `builtin.research.catalog/search/fetch` 工具。Connector 固定来源 Host，共享取消、30 秒超时、限速、有界重试、8 MiB 响应上限、缓存和来源错误分类；单一来源失败不会伪装成零结果。
- “文献发现”支持项目级多源查询、部分失败、保守去重、来源快照、纳入/排除/待定、排除原因和用户笔记。只有显式纳入后才下载开放全文或生成明确披露为元数据/摘要的 Markdown。
- 每个候选具有规范书目及作者顺序、年份、题名、载体、卷期页、出版社、DOI/PMID/PMCID/arXiv/OpenAlex ID、URL 等字段；每个观察值保留来源和 SHA256，冲突可选择来源，用户修改保留逐字段修订历史。
- 证据矩阵支持研究问题、方法、样本/数据集、主要结论、局限和笔记。研究事实必须绑定当前书目的本地知识 Chunk；只有用户笔记可无证据，全文与元数据/摘要以不同证据等级展示。
- 模型证据只能从 `pending` 开始，由用户核验、拒绝或重新置为待复核；审核只改变状态，正文、provenance、证据等级、Quote、SHA256、定位及历史来源快照受应用校验和 SQLite 触发器双重保护。

### 本地命令与界面

- 聊天框输入 `/` 可使用 `/mcp`、`/skill`、`/knowledge`、`/compact`、`/model`、`/reasoning`、`/permission`、`/status`、`/usage`、`/new` 和 `/help`。`/reasoning` 与 `/permission` 直接持久化当前会话设置；`/status` 只读汇总模型、上下文、checkpoint、MCP、Skill、知识库和识图兜底，不创建模型 Run。
- 本地命令不创建聊天消息或模型 Run；未知命令仍可作为普通文本发送。
- 命令面板支持多级键盘操作和点击外部关闭；`/compact` 显式显示执行中、成功、部分完成或失败。
- 流式文本按浏览器动画帧合并，历史消息隔离重绘，降低 WebView2 长对话掉帧。

### 科研产物

- 完整助手回答可显式保存为 Markdown Artifact，并快照 Run、Message、模型配置名称、模型、协议、所用 Skill 和实际采用的可信引用。
- Workspace 文件只在用户显式登记时进入产物库，可继续登记为现有 Artifact 的不可变新版本；不会自动扫描整个 Workspace。
- 成功 ToolResult 只有显式声明 `workspacePath` 才自动登记产物；现有 `ArtifactRef.ID` 附件/知识来源语义保持不变，启动恢复使用 `tool_call_id + ordinal` 幂等键。
- Tool 自动登记仍受原调用的 Workspace 权限资源范围约束；只有沿用 P5 Run/哈希/项目校验链的知识证据会显示为可信引用，外部 Tool 自报 Citation 不会自动取得可信状态。
- 项目级产物窗口支持列表、文本/图片有界预览、版本历史、来源摘要、完整性检查、下载、重命名、回收站和恢复。
- 删除 Conversation 会清除 Lineage 的活外键，但不会删除 Artifact；来源 ID、模型、协议、Skill 和引用内容的不可变快照继续保留。
- 每个 ArtifactVersion 可派生 DOCX 或 PDF；导出记录独立、不可更新，不覆盖原始对象也不推进当前版本。相同版本、格式、引用样式和生成器版本幂等复用，并对生成字节不一致明确报错。
- DOCX/PDF 从经过 SHA256 复核的临时快照生成，生成后重新打开校验，再写入内容寻址对象；下载目标已存在时拒绝覆盖。
- Markdown 通过 Goldmark GFM AST 转换标题、段落、列表、引用、代码和表格；PDF、DOCX、XLSX 和 CSV 复用本地结构解析器。图片和不可提取二进制不会伪装成研究文档。
- 无可读正文或超出正式解析/表格上限的源文件会在对象发布前拒绝导出。宽表按最多 8 列分段并在后续分段重复首列；DOCX 重复表头，PDF 对超高单元格分页切片且验证首尾文本没有丢失。
- 当前表格布局生成器为 `p6.2-v2`；旧 `p6.2-v1` 派生文件保持不可变并可继续下载，同一生成器版本内仍执行确定性字节校验。
- 正文只有与不可变 Citation snapshot 匹配的 `[K-...]` 标记才会按 GB/T 数字标号或 APA 形式渲染；伪造标记显示为未验证引用。新 Citation 同时快照当时的规范书目和证据等级，已知作者、年份、期刊和 DOI 会进入正式引文，缺失字段仍明确披露且不推断。
- PDF 内嵌固定版本的 Droid Sans Fallback 字体及 ToUnicode 映射，不依赖系统字体、Word、LibreOffice、Chrome 或联网服务；DOCX/PDF 对相同输入产生稳定字节。
- 科研产物窗口可为任一历史版本选择 DOCX/PDF 与 GB/T/APA，查看并下载该版本的历史导出；Markdown、PDF、DOCX、XLSX、CSV/TSV 以标题、段落、代码和有界表格块预览。

### 项目归档与恢复

- 左侧项目区可将项目导出为版本化 `.sciaide-project`，并从归档默认恢复为新 ID 的托管项目；原项目和归档文件不修改，同一归档可以多次独立恢复。
- 归档包含一致性项目 SQLite 快照、附件、文档缓存、知识索引、Artifact 原件/正式导出、研究候选、规范书目、证据和不可变历史关系；成功 ToolResult 已冻结但尚未完成 ArtifactVersion 登记的内容对象也会纳入 Manifest，恢复后可按同一 SHA256 补登记。所有文件由 Manifest 固化路径、类型、大小和 SHA256。
- 归档不包含 API Key、模型 Header、MCP Server/Secret、识图渠道、Embedding 凭据/查询向量、权限授权、待审批、Skill 包源码或可重建临时文件。历史模型仅恢复为禁用占位且 Conversation 回到 `Plan`；Skill 仅在本机存在完全匹配哈希时重新绑定。
- 恢复在隔离暂存中校验 ZIP 路径、碰撞、设备名、符号链接、条目数、大小、压缩比、Manifest、SHA256、SQLite 表/迁移/外键和项目关系，再重映射项目作用域 ID。Workspace 与导出文件使用操作系统原子 no-replace 发布，不覆盖竞态出现的目标。
- 数据库合并失败会回收已发布 Workspace；启动恢复会处理 marker、清除中断暂存并把未入库的孤立 Workspace 移入可恢复 trash，不暴露半恢复项目。

## 当前限制与未完成项

- 未内置 OCR；扫描型 PDF 只显示缺少文本层的诊断，需用户先用外部 OCR 处理。
- MCP `ImageContent` 仍只保留产物元数据和文本占位，不会作为模型原生图片内容块回传；当前图片输入仅来自聊天附件。
- 暂不支持旧版二进制 `.doc` 和 `.xls`；支持 `.docx` 与 `.xlsx`。
- 引用仅使用生成当时的不可变规范书目快照；用户自定义参考文献模板、完整 CSL 引擎和图表原位嵌入不属于 P6 范围。
- 扫描 PDF、图片和不可提取二进制不能直接转换为正式文档；需要先产生可验证文本或 OCR 结果。
- 当前项目 Python 仅支持 CPython 3 虚拟环境，尚无 R/Julia、Conda、容器或 GPU 资源调度；1 GiB Kernel 内存预算不是完整 CPU、文件系统或网络沙箱。
- 科研模式当前提供内置研究方案、阶段向导、数据分析任务表单和任务进度；尚未实现由对话自动生成研究方案、拖拽式节点画布，以及更多领域方案的结构化非编程参数表单。底层 Workflow 仍是显式可选能力，不会接管普通聊天或强制所有任务套用科研模板。
- P8 安装升级、代码签名、SBOM、跨平台发布和系统化压力测试尚未完成。

## 不可破坏的实现约束

- 前端只调用 `internal/transport/wails` Facade，不直接访问 SQLite、密钥或基础设施对象。
- 外部模型、MCP、Skill 和文献内容均视为不可信数据，只有结构化 ToolCall 能进入执行管道。
- 不得重新加入 Run 轮次、工具数量或总运行时长硬限制；复杂任务由用户停止、单次边界和上下文压缩控制。
- 不得因重试重复执行已有副作用的工具；网络重试仅覆盖可安全重试的模型请求阶段。
- 工具完整结果用于界面和审计，后续模型回放使用已持久化的有界不可变快照，不能动态改写旧缓存前缀。
- 聊天附件与知识库导入保持两条独立路径。
- 全局数据与项目大文件分离；删除项目派生缓存后应能由项目附件重建。
- 用户工作区可能包含未提交修改，不得使用 `git reset --hard` 或覆盖不属于当前任务的改动。

## 数据位置

```text
%USERPROFILE%/.sciaide/
├── data/sciaide.db                 # 全局 SQLite 元数据
├── data/workspaces/<project-id>/   # 默认托管 Workspace
├── data/python-envs/<project-id>/  # 仅兼容旧版全局托管 Python 环境
├── data/installed-skills/          # Git 安装的动态 Skill
├── data/user-skills/               # 用户创建的动态 Skill
├── data/skill-archives/            # 删除/卸载的可恢复归档
├── mcp/                            # 全局 MCP 配置/运行元数据
├── cache/project-archives/         # 归档导入/导出的中断暂存，启动时清理
├── backups/trash/                  # 移除项目及失败恢复 Workspace 的可恢复副本
└── logs/                           # 脱敏轮转日志

<Workspace>/.sciaide/
├── attachments/objects/            # 原始附件对象
├── cache/documents/                # 可重建的解析结果
├── cache/knowledge/index-vN.db     # Chunk、FTS5、向量和查询向量缓存
├── artifacts/objects/<prefix>/     # Artifact SHA256 内容寻址对象
└── tmp/                            # 同卷随机暂存；启动时清理中断文件
```

密钥不在上述目录中，以引用方式关联 Windows Credential Manager。

## 关键代码入口

- `main.go`：Wails 进程入口。
- `internal/bootstrap/application.go`：Composition Root、依赖注入和生命周期。
- `internal/transport/wails/`：前端可调用的 Facade 边界。
- `internal/app/chat/`：发送、停止、恢复与聊天用例。
- `internal/app/agent/`：Agent Loop、上下文、重试、推理摘要和压缩。
- `internal/model/{openai,responses,anthropic}/`：三种模型协议 Adapter。
- `internal/app/tool/` 与 `internal/tools/builtin/`：工具协议、权限执行和内置工具。
- `internal/app/mcpserver/` 与 `internal/mcp/`：MCP 配置、生命周期和传输。
- `internal/opensciskill/`：OpenScience 默认目录、来源发现、中英文两阶段语义路由、结构化正文/资源契约、Git/User 管理和动态 Run 快照；`internal/app/skill/` 与 `internal/skillpkg/` 是最小旧 Run/归档只读兼容层；`internal/app/multimodal/`：独立的宿主级识图兜底配置、路由与渠道管理。
- `internal/app/{attachment,knowledge,citation,artifact,research,projectarchive}/`：附件、知识检索、可信引用、科研产物、文献发现/证据和无密钥项目归档。
- `internal/research/{connectors,materializer,evidence}/`：公共数据源 Adapter、开放材料导入和本地证据校验。
- `internal/exporter/`：确定性 DOCX/PDF 结构生成、引用样式和固定字体资产。
- `internal/platform/filepublish/`：跨平台原子 no-replace 文件/目录发布。
- `internal/platform/localexec/`：本地进程环境、输出、取消、进程树和应用关闭生命周期。
- `internal/app/pythonenv/`、`internal/platform/{pythonruntime,pythonkernel}/`：项目环境、依赖锁、持久 Kernel、复现哈希和资源治理。
- `internal/app/workflow/`、`internal/app/researchworkflow/`：Workflow 编译、不可变版本、可恢复 Runtime 和参考科研闭环。
- `internal/storage/sqlite/`：Repository 与迁移；当前最新迁移为 `000062_p7_workflow_permissions.sql`。
- `frontend/src/App.tsx` 与 `frontend/src/styles.css`：当前主要 UI 和交互实现。

## 验证与构建基线

- 统一检查：`.\scripts\p0-check.ps1`
- Windows x64 发布构建：`.\scripts\build-release.ps1`
- 不要直接依赖当前机器的全局 Go 目标；脚本会显式使用 `windows/amd64` 和 `CGO_ENABLED=0`。
- 构建不依赖 `config.json`、临时 embed 文件或任何视觉渠道；没有配置识图模型的干净源码树也必须通过统一检查并产出可启动 EXE。
- 自动化测试覆盖三协议图片映射、真实 PNG/WebP 导入、图片拒绝分类、自定义视觉渠道优先级切换、密钥隔离、旧 Skill 迁移和同轮多请求记账。OpenAI 兼容视觉渠道使用非流式请求以兼容部分网关，普通对话模型继续流式输出。
- P6.2 自动化测试覆盖源/导出 SHA256、幂等复用、重命名稳定性、下载拒绝覆盖、无文本层 PDF 拒绝、64 列分段、DOCX 重复表头及 PDF 超长单元格跨页首尾完整性。
- P6.3～P6.5 测试覆盖六个 Connector Fixture、网络治理、保守去重、候选导入幂等、书目字段冲突/修订、证据不可变审核、完整 GB/T/APA 渲染，以及“候选 → 附件 → 索引 → Citation → Artifact → PDF”的端到端链。
- 动态 Skill 测试覆盖 311 个默认包解析/能力审计、1,624 文件 Manifest、四级来源覆盖、启停策略、320 条双语路由门禁、结构化 Run 路由审计/篡改检测/实际加载指标、多轮连续性、候选上限、显式选择、旧 Run 路由隔离、章节索引/分页/幂等正文快照、文本与二进制资源清单、管理页源码目录/文本/二进制/路径逃逸/哈希漂移边界、50 文件深层树/小屏布局、`allowed-tools` Registry 交集、Git 审查/固定 SHA、Artifact provenance 和项目归档 ID 重映射；P6.6 归档攻击面与失败原子性测试继续保留。
- P7.1 真实 Windows 测试覆盖 PowerShell/CMD/Python、中文 Workspace 与 UTF-8 输出、密钥环境排除、1 MiB 输出持续排空、64 KiB 截断、非零退出与 stderr、超时/取消/应用关闭、后台子进程清理、路径逃逸、`.sciaide`、旧内容冒充产物、执行审计生命周期及归档 ID 重映射。
- P7.2 测试覆盖 Workspace 内环境原子创建/重建/恢复、旧全局环境升级兼容、已有 venv 绑定与只解除绑定、取消解释器选择不改变状态、依赖锁、固定数据的路径无关复现哈希、Kernel 状态持续与重启、异常/缺包/超时/取消/内存超限后的输出回滚、普通输出与图片整批发布、外部目标抢占、同项目 Kernel/环境互斥、旧 staging 清理与近期 staging 保留，以及 Shell、一次性 Python 与 Kernel 默认联网。外部 venv 始终由用户管理，SciAide 不删除、不重建且不向其中安装依赖。真实 CSV/TSV Fixture 验证结构化目标、完整 SHA 输入快照、源文件只读、公式转义、五类 Artifact 和独立重放哈希；真实 XLSX Fixture 在两个干净项目中使用不同输入/Run 文件名，验证完整 SHA 快照、四类 Artifact 内容哈希一致、公式转义、SQLite Kernel 审计和生成脚本独立重放。恶意/超大 XLSX、快照篡改、4 KiB 后二进制 NUL、非法 Port 契约和旧内置方案兼容均有专项测试。
- P7.3 同步门禁覆盖 311 个 Skill、1,624 个文件、238 个执行边界、v2 能力矩阵和禁止宿主承诺扫描；P7.4/P7.5 覆盖合法图、循环、类型、Tool 变化、路径/密钥/体积、并发决策、幂等恢复、未知结果与副作用确认。
- P7.6 确定性端到端测试覆盖两次应用重启，以及候选筛选、Citation 冻结、项目环境、Kernel、CSV/SVG、Artifact、Markdown、DOCX/PDF 和来源链；ToolResult 字节冻结、声明 SHA 不匹配拒绝、待登记对象归档/还原/补登记也有专项测试。前端专项测试覆盖历史环境 Manifest、产物图谱、未完成 Run 不伪造证据、小屏布局，以及科研模式必须保持主工作区身份、四项项目级公共入口不被模式隔离和高级工程信息降级。
- OpenScience 同步检查：`.\scripts\sync-openscience-skills.ps1 -Check`；只有审核上游差异后才使用 `-Update`，脚本会校验来源版本、311/1,624 数量、许可证、逐文件哈希和安全路径。
- P7 正式 Windows x64 Release 已使用隔离 `SCIAIDE_HOME` 完成进程、数据库和可见窗口验收；本轮 Workspace Python 环境改动新增第 61 条迁移 `000061`，最新 Release 已复验 `integrity_check=ok`、`foreign_key_check` 0 行、主窗口可见且标准关闭退出码 0。
- 上一版 Release 已用 `%USERPROFILE%/.sciaide` 的完整隔离副本覆盖真实历史升级；迁移 `000061` 会把旧 Python 环境记录标记为 `legacy_managed` 并保留原路径，新建记录默认使用 `workspace_managed`。未知迁移校验仍拒绝启动，并由 Windows 错误弹窗直接显示原因。
- 当前本地 EXE：`build/bin/SciAide.exe`；构建时间 `2026-08-26 16:42:27`，大小 `50,508,288` 字节，PE Machine `0x8664`（Windows amd64），SHA256 `B49F6B7A4E6838DBC207F6CB8D96B2103B7B87A7093132CECFE4ED59B654DC6E`。

## 下一步建议

P7.1～P7.6 已通过专项测试、全量 Go 测试、vet、前端类型检查/测试/生产构建、P0 统一检查、默认 Skill 派生树校验、Windows x64 Release 和隔离数据根验收，P7 到此结束。下一阶段进入 P8 安装升级、代码签名、SBOM、跨平台发布和系统压力测试；所有能力继续复用 ToolExecutor、PolicyEngine、可信 Citation 和 Artifact，不允许 Workflow、脚本或 Skill 自行扩大权限。
