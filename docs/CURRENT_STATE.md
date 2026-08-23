# SciAide 当前开发状态

> 更新日期：2026-08-23
> 代码基线：`v0.4.0` 发布源码
> 用途：供新会话和开发者快速恢复上下文；长期架构与阶段门禁仍以 [`start.md`](../start.md) 为准。

## 如何使用本文档

新会话不需要先遍历整个仓库。建议按以下顺序接手：

1. 阅读本文档，确认当前阶段、约束和未完成范围。
2. 运行 `git status --short` 与 `git diff --stat`，确认本文档之后是否又有改动。
3. 阅读 [`start.md`](../start.md) 中与当前任务相关的章节。
4. 阅读 [`CHANGELOG.md`](../CHANGELOG.md) 的 `Unreleased` 和对应 [`docs/adr`](adr) 决策。
5. 最后使用 `rg` 定向定位代码和测试，不要无目的遍历整个仓库。

当文档与程序行为冲突时，可信度顺序为：当前运行行为、自动化测试、当前工作区代码、本文档、ADR、`start.md` 路线图。路线图描述目标，不代表功能已经实现。

## 当前阶段

- P0～P4.6 已完成：工程基线、聊天与 Agent Loop、权限和工具、MCP、多协议模型、Skill、项目附件与本地文档读取。
- P5 已完成：项目知识库、FTS5/BM25、可选 Embedding 混合检索、可信引用、任务运维、解析诊断和固定语料评测。
- P6.0 已完成：斜杠命令及运行时二级面板、手动压缩、聊天界面与流式渲染加固。
- 当前正在收敛 P6 前的对话稳定性、模型协议连续性、缓存前缀和请求级用量诊断。
- P6 正式内容尚未开始：科研 Artifact、版本和来源关系、DOCX/Markdown 导出、引用样式、项目备份与恢复。
- P7～P9 尚未开始。

## 已实现能力

### 应用与数据

- Go + Wails + React + TypeScript 桌面架构，使用 Application / Port / Adapter 分层。
- Project、Conversation、Message、Run、模型配置、MCP、Skill 和审计状态持久化到版本化 SQLite。
- API Key 存入 Windows Credential Manager，前端和普通配置只保留掩码或引用。
- 默认数据根为 `%USERPROFILE%\.sciaide`；用户可以让项目使用任意外部 Workspace。
- 项目附件、解析缓存和知识索引跟随 Workspace，避免大体积科研数据固定占用系统盘。

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

### Skill

- 支持版本化 Skill 包、完整性校验、项目固定版本、启停、优先级、回滚、引用保护卸载和 Codex 风格 `SKILL.md` 导入。
- Skills 列表用绿色/灰色/红色区分项目已启用、未启用和不可用，并支持一键启用全部可用 Skill。
- Skill 只提供上下文和工具需求声明，不授予权限，也不会直接执行脚本。
- 仅显式 `$skill-id` 或确定性触发规则命中的 Skill 才为当前 Run 加载完整正文。
- 内置 Skill 仅包括 `literature-reading` 与 `academic-writing`。旧 `hello-multimodal` 项目链接和 catalog 记录由迁移清除，原内置包移入可恢复备份；历史 Run 的 Skill 快照不删除。

### 附件与知识库

- 聊天附件默认只供当前对话读取，不会自动加入知识库；知识库窗口负责显式导入、移除、重试和重建。
- 本地文档解析支持 PDF、DOCX、XLSX、TXT、Markdown、CSV 和 TSV；聊天图片支持 JPEG、PNG 和 WebP，并按经过校验的真实内容确定 MIME 类型，因此 WebP 即使使用 `.jpg` 文件名也能安全导入；图片不进入文档文本解析、`builtin.document.*` 或知识库索引，文档工具误调用不会改变图片就绪状态。
- PDF 保留页码并清理碎片换行、断词和重复页眉页脚；DOCX 保留标题层级、章节路径、列表和表格行。
- 知识库默认使用项目级 FTS5/BM25；配置 `/v1/embeddings` 后构建独立向量影子索引并使用 BM25 + 向量 + RRF 混合检索，Embedding 失败自动回退 BM25。
- 查询向量缓存在当前项目的 `index-vN.db` 中，按索引版本隔离且不保存查询明文。
- 引用绑定 Run、IndexVersion、Chunk 和原文件 SHA256；最终回答只展示经过证据快照校验的可点击引用。

### 本地命令与界面

- 聊天框输入 `/` 可使用 `/mcp`、`/skill`、`/knowledge`、`/compact`、`/model`、`/reasoning`、`/permission`、`/status`、`/usage`、`/new` 和 `/help`。`/reasoning` 与 `/permission` 直接持久化当前会话设置；`/status` 只读汇总模型、上下文、checkpoint、MCP、Skill、知识库和识图兜底，不创建模型 Run。
- 本地命令不创建聊天消息或模型 Run；未知命令仍可作为普通文本发送。
- 命令面板支持多级键盘操作和点击外部关闭；`/compact` 显式显示执行中、成功、部分完成或失败。
- 流式文本按浏览器动画帧合并，历史消息隔离重绘，降低 WebView2 长对话掉帧。

## 当前限制与未完成项

- 未内置 OCR；扫描型 PDF 只显示缺少文本层的诊断，需用户先用外部 OCR 处理。
- MCP `ImageContent` 仍只保留产物元数据和文本占位，不会作为模型原生图片内容块回传；当前图片输入仅来自聊天附件。
- 暂不支持旧版二进制 `.doc` 和 `.xls`；支持 `.docx` 与 `.xlsx`。
- P6 Artifact 与正式导出尚未实现，现有聊天回答和附件不等于版本化科研产物。
- 项目备份、导入恢复和完整性演练尚未实现。
- P7 Workflow/DAG、Python 数据分析运行时尚未实现。
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
├── skills/                         # 全局 Skill 包
├── mcp/                            # 全局 MCP 配置/运行元数据
└── logs/                           # 脱敏轮转日志

<Workspace>/.sciaide/
├── attachments/objects/            # 原始附件对象
├── cache/documents/                # 可重建的解析结果
├── cache/knowledge/index-vN.db     # Chunk、FTS5、向量和查询向量缓存
└── artifacts/                      # 预留项目科研产物目录
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
- `internal/app/skill/` 与 `internal/skillpkg/`：Skill 运行时和包管理；`internal/app/multimodal/`：独立的宿主级识图兜底配置、路由与渠道管理。
- `internal/app/{attachment,knowledge,citation}/`：附件、知识检索和可信引用。
- `internal/storage/sqlite/`：Repository 与迁移；当前最新迁移为 `000043_vision_fallback_channels.sql`。
- `frontend/src/App.tsx` 与 `frontend/src/styles.css`：当前主要 UI 和交互实现。

## 验证与构建基线

- 统一检查：`.\scripts\p0-check.ps1`
- Windows x64 发布构建：`.\scripts\build-release.ps1`
- 不要直接依赖当前机器的全局 Go 目标；脚本会显式使用 `windows/amd64` 和 `CGO_ENABLED=0`。
- 构建不依赖 `config.json`、临时 embed 文件或任何视觉渠道；没有配置识图模型的干净源码树也必须通过统一检查并产出可启动 EXE。
- 自动化测试覆盖三协议图片映射、真实 PNG/WebP 导入、图片拒绝分类、自定义视觉渠道优先级切换、密钥隔离、旧 Skill 迁移和同轮多请求记账。OpenAI 兼容视觉渠道使用非流式请求以兼容部分网关，普通对话模型继续流式输出。
- 当前本地 EXE：`build/bin/SciAide.exe`
- 构建时间：`2026-08-23 16:59:21`
- 文件大小：`21,229,568` 字节
- SHA256：`DFAC4D4B70BAE117054765BFFF3BC5DCAC3E281B4BD376D5A57EAB8B15B27A00`

## 下一步建议

进入 P6.1 前先完成一轮对话稳定性验收：三协议的普通回答、推理、工具续轮、MCP 卡住后停止、网络重试、自动/手动压缩、请求用量状态和重启恢复。验收通过后，按 `start.md` 开始 Artifact 数据模型、来源关系和版本化持久化，随后实现 Markdown/DOCX 导出与项目备份。
