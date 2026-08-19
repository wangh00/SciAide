# SciAide

面向科研工作者的本地优先桌面 AI Agent，支持自定义模型、工具调用、MCP、Skill、科研知识库和可信引用。

当前阶段：**P5.5 PDF/DOCX 结构解析加固完成**。

## 当前能力

- Wails + React + TypeScript
- Application / Port / Adapter 边界
- SQLite 版本化迁移与 Project / Conversation / Message / Run 持久化
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
- P3 MCP：stdio / Streamable HTTP 配置、显式信任、initialize 与 Tools/Resources/Prompts 能力发现
- 兼容 Claude Desktop、Cursor、Codex 常见的 `mcpServers` JSON，可一次粘贴并导入多个 Server
- MCP Tool 使用稳定的 `mcp.<namespace>.<tool>` 名称进入统一 ToolRegistry、Plan/Full Access 审批和 ToolExecutor
- MCP stdio 仅继承最小环境；SecretEnv 明文保存在 Windows Credential Manager；Resource/Prompt 不自动注入上下文
- Anthropic thinking/signature/redacted_thinking 与 Responses reasoning/encrypted content 按 Provider Turn 不可变持久化，并在工具结果后严格回放；原始协议状态不进入聊天 Snapshot
- 推理证据区分“参数已接受”和“已观察到思考”，支持 reasoning token 汇总、默认折叠的安全状态卡，以及不拆分原生推理/工具协议组的上下文压缩
- 每个模型独立保存上下文窗口及 `provider/manual/builtin/fallback` 来源；运行时使用 95% 有效预算和不高于 90% 的自动压缩阈值，普通 `/v1/models` 缺少元数据时明确回退 200K
- 超长会话先生成无工具的结构化科研 checkpoint，再以“已校验摘要 + 最近完整对话组”继续；checkpoint 带消息边界、revision 和 SHA256，原始聊天记录不删除
- P4.1 Skill 基线：严格解析 `skill.yaml` 与非空 UTF-8 `SKILL.md`，按 `~/.sciaide/skills/<id>/<version>/` 扫描版本化包
- Skill Manifest、入口内容和全包 SHA256 持久化；包被修改、缺失或校验失败时进入不可用状态，启动扫描不会静默信任已安装包的新内容
- Skill 可按项目固定启用具体版本和优先级；必需 Tool 缺失或 SciAide 版本不兼容时禁止启用，可选 Tool 缺失仅作为状态提示
- P4.2 支持本地文件夹/ZIP 经随机暂存和完整校验后原子安装；拒绝路径穿越、符号链接、压缩炸弹、Windows 危险路径和半安装状态
- 原始包归档、安装副本和暂存缓存相互分离；同版本不同内容必须显式确认替换，卸载默认进入可恢复备份，被项目引用时默认拒绝
- 多版本可并存，项目可以显式回滚到已安装且可用的更低 Skill 版本；安装、替换、来源哈希和归档状态均持久化
- P4.3 按 Run 构建有界 Skill catalog；仅 `$skill-id` 显式选择或确定性 suggest trigger 命中的 Skill 才完整加载正文，不跨用户 Run 自动沿用
- 首次模型请求前原子保存不可变 Skill 上下文与 `run_skills` 审计；工具循环和审批恢复复用同一快照，项目改动或卸载不会改变进行中的 Run
- 兼容导入 Codex 风格 `SKILL.md` 并归一化为版本化 SciAide 包；正文引用的 `references/` 文本通过 Run 绑定的 `builtin.skill.resource.read_text` 按需读取，不暴露主机路径、不自动执行脚本
- 未知、未启用或不可用的显式 Skill 会产生可审计状态提示；选中正文按当前 Turn 时序放在历史之后、本轮问题之前
- Skill catalog/正文以 contextual user 内容注入；Manifest 权限不参与授权，所有工具仍经过 Plan/Full Access、Workspace 边界和 ToolExecutor
- P4.4 Skill 管理页按 Skill ID 聚合多版本，展示完整性、可用性、来源哈希、激活规则以及必需/可选 Tool 缺失状态
- 支持原生选择本地文件夹或 ZIP 安装、显式同版本替换确认、目录刷新、引用保护卸载，以及当前项目的版本固定、启停、优先级和低版本回滚
- P4.5 随程序提供 `literature-reading` 文献阅读和 `academic-writing` 学术写作两个版本化科研 Skill；`literature-reading@1.1.0` 已接入本地文档工具
- 内置原件只读嵌入二进制，启动补装仍经过暂存、校验、来源归档和原子安装；同版本用户包优先且不会被升级静默覆盖
- 可脚本化 `FakeChatModel`、Provider Fixture 测试、威胁模型、ADR 和 CI

## 开发

依赖安装和命令见 [`docs/development.md`](docs/development.md)。完整架构与阶段门禁见 [`start.md`](start.md)。
