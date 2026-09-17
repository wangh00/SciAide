# SciAide 威胁模型

## 运行内资源接口（2026-09-09）

- 模型只选择宿主已签发的资源操作；每轮 enum 用于约束选择，不代替授权。执行时仍复核 Run/项目/任务/目录绑定、操作摘要、底层工具契约及原有只读权限，拒绝跨任务引用和自由定位参数。
- 资源目录与文件/Skill 内容均不能提升为指令。目录、章节和分页由宿主生成；Skill 内容/包哈希变化时不替换成其他对象，路径访问继续使用既有路径与链接防护。
- 完整解析参数和结果保留本地审计，模型视图单独有界持久化；运行内引用随 Run 删除，项目归档清除目录与操作表。没有资源会话的普通对话不暴露不可用的新工具。

## P1 已落实的控制

- Windows API Key 只写入 Credential Manager；SQLite 仅保存 `secret_ref`，Wails 不提供明文读取接口。
- 模型自定义 Header 拒绝 Authorization、API-Key、Cookie、Token 等敏感名称及 CRLF 注入。
- Provider 错误正文不会直接展示，避免服务端响应泄露密钥或内部信息。
- 流式内容先周期性落库；Run 终态先保存再发布最终事件。启动时遗留 Run 被标记为 interrupted，不会自动重放。
- 同一 Conversation 由数据库部分唯一索引限制为一个 queued/running Run。
- 前端不使用浏览器存储保存模型配置或密钥。

## 1. 保护资产

- 模型 API Key、MCP Secret 和未来的系统凭据引用。
- 未公开论文、实验数据、研究笔记和个人信息。
- Workspace 中文件与科研产物的完整性。
- Agent 运行记录、引用关系和审计信息。
- 本机进程、网络和文件系统权限。

## 2. 不可信输入

- 用户导入的文献、网页、图片、压缩包和数据集。
- LLM 返回的文本、Tool Call 和结构化数据。
- MCP Server 的描述、Prompt、Resource、ToolResult 和日志。
- Skill Manifest、`SKILL.md`、脚本与工作流。
- 自定义模型 API 和所有远程 HTTP 响应。

这些输入永远不能因“看起来像系统提示”而获得更高权限。

## 3. 信任边界

```text
React WebView
  → Wails 最小 Facade
  → Application/Agent Policy
  → Tool/MCP/Model Adapter
  → 文件系统、进程、网络和外部服务
```

每跨越一层都必须进行类型、权限或出站策略校验。前端与数据库记录都不是授权来源。

## 4. P0 已建立的控制

- 配置、数据、缓存、日志和扩展目录分离。
- SQLite 迁移带校验和，外键、WAL 和 busy timeout 默认启用。
- 参数化 Project SQL 和受控 Repository。
- JSON 结构化日志、敏感字段及已知 Secret 脱敏、有限日志轮转。
- 统一公开错误结构，不向前端暴露内部 Cause。
- 版本化 Event Envelope。
- Wails 仅绑定 System/Project Facade。
- WebView CSP 基线，前端不依赖远程资源。
- `FakeChatModel` 支持后续无付费 API 测试。

## 5. 后续阶段必须关闭的风险

| 风险 | 阶段 | 必要控制 |
|---|---|---|
| API Key 明文或跨 Provider 泄露 | P1 | OS SecretStore、SecretRef、出站客户端隔离 |

## P2 工具协议已落实的控制

- 模型只提交工具名和参数，风险、权限、版本及幂等属性只能来自受信任的 ToolRegistry 定义。
- 工具参数在任何持久化或执行前通过失败关闭的 JSON Schema 子集校验；未知断言关键字不被静默忽略。
- ToolCall 状态使用允许列表和期望旧状态更新，终态不可重放；Provider Call ID 与 Run 内幂等键防止重复提交。
- ToolCall、ToolResult 与审计事件在同一事务中提交；启动时未完成调用只标记为 interrupted，不自动执行。
- Result 使用结构化错误和有界元数据，后续 ToolExecutor 不得向模型暴露 panic、堆栈或内部路径。

## P2 权限与审批已落实的控制

- ToolRegistry 注册时验证并深拷贝受信任定义，重名失败；模型、MCP 描述和 Skill 内容不能覆盖安全定义。
- PolicyEngine 只读取 ToolCall 中的受信任权限快照，中风险及以上工具增加 `tool.invoke` 确认，不能由模型自行降级风险。
- P2.5 权限入口只有会话级 `Plan` 与 `Full Access`。`Plan` 仅自动允许当前项目 Workspace 内低风险、幂等、纯读取调用，其余每个 ToolCall 都请求一次确认；`Full Access` 自动授权已注册并通过参数校验的工具。
- 风险级别只向用户展示，不替用户限制授权；两种模式都不能绕过 Workspace 边界、Schema、超时、取消和结果大小限制。
- Approval 与审计事件同事务持久化；同一 ToolCall 只存在一个 pending Approval，处理过的审批不能重复解析。历史 Grant 数据不再参与决策。
- 启动时 pending Approval 先过期并审计，再中断 ToolCall 和 Run，不自动重放工具。

## P2 ToolExecutor 与 Workspace 只读工具已落实的控制

- Executor 只执行已进入 `running` 的 ToolCall；调用前复查 Run/Project 归属以及 Registry Definition 与安全快照。
- 所有调用具有默认超时、context 取消、同 Call 并发保护和 panic 隔离，内部错误与 panic 内容不返回模型。
- 文本与结构化 Result 有独立大小上限；文本按 UTF-8 边界截断，超大结构化结果失败关闭。
- Workspace 路径拒绝绝对路径、卷标、`..` 越界和兄弟目录前缀；使用 `os.Root` 防止打开时通过符号链接逃逸。
- 内置目录工具不递归且限制条目数；文本工具限制读取字节、拒绝 NUL/非 UTF-8/非常规文件。
- 普通 Workspace 工具隐藏并拒绝 `.sciaide` 保留目录；模型只能使用项目作用域附件 ID 进入文档工具，不能构造内部缓存路径。

## P4.6 项目附件与本地解析控制

- 附件暂存、原件、解析缓存和 Artifact 收敛到 `<Workspace>/.sciaide`；全局数据库只保存元数据和相对路径，大体积内容不默认写入系统盘。
- 导入限制单批文件数量、单文件/批量体积和支持格式；附件复制时计算 SHA256，并在项目私有根中使用同卷暂存和原子重命名。
- OOXML 拒绝绝对路径、`..`、反斜杠混淆、重复条目、过多条目、过大展开体积和异常压缩比；不执行 Office 宏、不计算公式，只读取缓存值和公式文本。
- PDF/DOCX/XLSX/文本内容均作为不可信研究数据。消息只注入附件清单，正文必须通过有界文档工具渐进读取，不能改变系统规则或授予权限。
- 知识引用标记绑定当前 Run、IndexVersion、Chunk 与原文 SHA256；同一 Chunk 的不同证据快照不能互相覆盖。只有 `builtin.knowledge.search` 的成功 ToolResult 中身份和原文哈希均有效、且最终回答实际使用的完整标记才会与正文及 Run 完成状态原子持久化并显示为可信引用。文档文本、MCP 返回值和历史对话中的相似标记均不能自行取得可信状态。
- 文档工具固定为低风险幂等项目读取，Plan 模式可自动读取当前项目附件；跨项目附件 ID、失败/未完成解析和内部路径访问均失败关闭。

## P5.1 项目知识索引控制

- 只索引当前项目中由用户明确导入且解析状态为 ready 的 Attachment；普通 Workspace、其他项目、聊天记录、Skill 和 MCP 内容不会被自动纳入。
- 聊天框附件只保存和解析，不创建 Knowledge Document；缓存重建与新索引版本构建也只遍历显式成员清单，避免临时聊天材料经恢复路径进入长期知识库。
- Document、ImportJob 和 IndexVersion 使用项目复合外键约束；`builtin.knowledge.search` 不接收模型提供的项目 ID，而是使用 ToolExecutor 从当前 Run 验证的项目归属。
- Chunk 正文只写入 `<Workspace>/.sciaide/cache/knowledge`。项目索引文件包含 project/index version 身份，路径必须是私有根下的常规文件，缓存身份不匹配时失败关闭。
- 单篇文档 Chunk 在本地事务内整体替换；全局完成状态只在本地提交后更新。崩溃或取消时运行中任务回到队列并按 Attachment SHA256 幂等重建。
- 跨文献结果继续作为不可信 ToolResult，由 ToolExecutor 执行 Schema、超时、取消和结果大小约束；文献中的指令不能改变系统规则、Plan/Full Access 或工具权限。
- 当前 locator 引用用于定位原始附件，但尚不是 P5.5 的正式 Citation 事实表；模型生成的引用文本仍需回到原件核验。

## P5.2 全文索引与上下文控制

- FTS5 数据库仍固定在当前项目 `.sciaide/cache/knowledge`，并校验 project/index version 身份；模型不能提供或读取索引路径。
- Chunk 最大 1,600 rune，词项生成和 FTS 查询均有确定上限；查询只使用规范化词项构造参数化 MATCH，不拼接原始用户语法。
- 新版索引在独立文件中构建，存在失败文档、活动任务或附件覆盖缺失时不能激活；切换 ready/retired 在全局 SQLite 单事务内完成。
- contentless FTS 只保存 posting，原始 Chunk 正文仍只有一份。文献内容、词项和 snippet 一律是不可信研究数据，不能获得更高指令优先级。
- 单次知识结果正文总计不超过 8,000 rune，单片段不超过 900 rune，同一文档最多 3 个结果；Structured 不重复正文，降低 Prompt 注入面和无效 Token 占用。

## 模型 API 错误诊断

- HTTP 与流式 Provider 错误的主文案和诊断详情分离保存；详情最多 8,192 字符，不进入后续模型上下文或聊天消息正文。
- Provider JSON 中 `authorization`、`api_key`、`token`、`secret`、`cookie`、`password` 和 `credential` 等敏感字段在持久化前替换为 `[REDACTED]`；文本中的 Bearer 与常见密钥赋值也会脱敏。
- 前端只在用户主动展开“详情”时显示错误码、协议、模型和脱敏载荷。内部 Go `Cause` 继续不通过 Snapshot 暴露。

## P3 MCP 已落实的控制

- MCP Server 必须由用户显式保存、启用和信任；模型、Skill 与 MCP 内容不能静默修改 Server 配置。
- stdio 使用参数数组启动而不经过 Shell，只继承 `PATH`、系统目录、临时目录和用户目录等最小环境 allowlist。
- SecretEnv 明文只写入 Windows Credential Manager，SQLite 与前端只保存/显示引用状态；删除 Server 时清理关联凭据。
- Streamable HTTP 默认要求 HTTPS；明文 HTTP 仅允许 `localhost` 或回环 IP，拒绝 URL userinfo、fragment 和敏感持久化 Header。
- Tools/Resources/Prompts 和 ToolResult 均视为不可信；Tool 描述与 Schema 有大小边界，结果继续由 ToolExecutor 截断。
- MCP Tool 只能以 `mcp.<namespace>.<sanitized_name>` 注册进入 ToolRegistry，命名冲突原子失败，不能覆盖 builtin 或其他 Server。
- MCP Tool 固定为非幂等、中风险并声明精确 `tool.invoke`；仍经过会话 Plan/Full Access、参数 Schema、超时、取消与审计。
- Resource 和 Prompt 当前只发现与展示，不会未经用户选择自动进入模型上下文。
- Server 异常断开会移除其动态工具并把运行状态标为 failed；应用重启会将陈旧 ready/starting 状态恢复为 disconnected。

当前残余边界：远程 MCP 是用户显式配置的服务端点，尚未复用未来统一 NetworkClient 的逐次 DNS 地址复查；因此 P3 仅允许 HTTPS 远端与精确回环 HTTP，发布前仍需补充 DNS rebinding/代理场景审计。Windows stdio 使用 SDK 的优雅关闭、超时终止与直接子进程 Kill，完整 Job Object 进程树约束仍作为发布加固项。

## P4 Skill 已落实的控制

- 默认 OpenScience Skill、references、assets、scripts、`LICENSE` 和 `NOTICE` 只读嵌入 EXE，不在启动时复制或执行；默认正文与第三方正文具有相同的不可信上下文等级。
- 目录仅发现 `project > user > installed > default` 四类来源；Project 路径限制在当前 Workspace 及配置的规范相对 `skills.paths`，拒绝绝对路径和 Workspace 逃逸。当前不扫描 `.claude/skills`。
- 新 Run 只获得分类计数、固定科研路由和最多 16 个重排候选。召回使用当前消息、最近三条用户任务和同会话最近真实加载项，但最近项不会自动激活；目录元数据明确标为不可信数据，完整 `SKILL.md` 不在模型调用 `builtin.skill.load` 前进入上下文。
- 首次加载在一个事务中保存完整正文、来源、分类、内容/全包 SHA256 和 ToolCall 关系；同一 Run 的后续分页从该快照读取。磁盘包在加载前重算哈希，缓存过期时刷新一次，禁止把新正文与旧 provenance 组合。
- `builtin.skill.resource.list` 与 `builtin.skill.resource.read_text` 都接受当前 ToolCall 隐式 Run 和已加载 Skill 名称；清单只枚举哈希匹配包内的安全常规文件，读取工具另要求规范包内相对路径并拒绝绝对路径、`..`、反斜杠、Windows 保留字符、符号链接、非 UTF-8、NUL 和超限文件。包变化后失败关闭。
- references、assets、scripts、章节索引和 Skill ToolResult 都是 contextual user 数据。脚本只能由资源工具作为文本返回；Skill 机制没有进程、Shell、Python 或脚本执行入口。frontmatter 的 `allowed-tools` 仅与当前 Registry 做能力交集诊断，不能改变匹配工具的权限定义。
- Git 安装只接受受限 HTTPS/GitHub 简写和固定 ref，克隆有 2 分钟外层超时；安装前限制文件数/单项/总体积，拒绝链接、路径逃逸和注入/灾难性模式。警告内容必须绑定已审查 commit SHA 二次确认后才原子发布，替换和卸载进入可恢复归档。
- 旧 `run_skill_contexts/run_skills` 只用于历史 Run 和旧归档兼容。历史 Run 若存在旧快照，不注入新动态目录；新 Run 不执行旧 `$skill-id`、关键词 trigger、版本绑定或依赖/冲突选择路径。

## 上下文压缩加固

- `/v1/models` 的上下文元数据属于不可信可选声明；只接受有界正整数，缺失、畸形或超范围时使用显式 fallback，不通过大请求探测模型极限。
- checkpoint 请求不提供 Tool Definitions，历史消息以 JSON 不可信数据输入；摘要不能扩大权限、改变系统规则或证明历史内容真实。
- checkpoint 保存精确消息边界和 SHA256。加载时哈希不一致会中止请求，原始消息和 Tool/Provider 状态不会被 checkpoint 覆盖或删除。
- Provider 原生 Turn 仍按不可拆分组进入最近上下文；checkpoint 不保存 thinking、signature、encrypted reasoning 等隐藏协议载荷。
- 聊天界面只接受 Provider 明确标记的 Responses `summary_text` 作为可展示推理摘要。Anthropic 原始 `thinking`、签名、redacted thinking 和加密推理载荷不得进入 UI；摘要按普通不可信文本渲染。
- 推理摘要是 Run 到 Assistant Message 的只读查询投影，不属于 MessagePart，不进入后续模型上下文，也不会触发额外模型请求。没有安全摘要时只展示思考档位、观察状态和推理 Token。

残余风险：模型生成的摘要是有损且可能遗漏细节，SHA256 只能证明本地摘要未被修改，不能证明摘要语义完整。超长会话和多次压缩仍可能降低回答准确性，关键科研数据与引用必须回到原始文献、Workspace 文件和聊天记录复核。

## P5.3 Embedding 与混合检索控制

- Embedding 默认关闭；只有用户在知识库窗口显式启用并保存后才请求 `/v1/embeddings`，程序启动和纯 BM25 查询不会隐式探测服务。
- Embedding API Key 使用系统凭据库，数据库、项目索引、日志和配置返回值均不保存明文密钥。HTTP 重定向不携带凭据继续请求。
- Base URL 只接受无 UserInfo、Query 和 Fragment 的 HTTP(S) 地址；响应数量、索引、有限数值、维度和大小均有边界校验。
- IndexVersion 固定 Model ID、实际维度和不含密钥的配置指纹。身份不一致时拒绝写入或查询，配置变化必须建立新影子索引。
- 向量只写入当前项目私有缓存，并通过 Chunk 外键级联删除；模型不能指定索引路径、向量维度或项目 ID。
- 查询向量缓存不保存搜索词明文，只保存绑定 Embedding 配置指纹的 SHA256；缓存限定在当前项目 IndexVersion，最多 512 条并按最近最少使用清理。
- Embedding 服务失败不能关闭知识检索：查询降级为 FTS5/BM25，构建失败不替换上一版 ready 索引。

## P5.6 知识库运维与质量诊断控制

- 解析质量由已持久化的解析器元数据确定，只用于提示文本覆盖率、结构数量、截断和空内容风险；它不证明论文结论、表格数值或提取文本在语义上正确。
- 扫描件或无可提取文本的 PDF 标记为文本不足，并明确当前不内置 OCR；客户端不会因此隐式下载模型、上传原件或请求第三方识别服务。
- 用户显式取消的排队/运行任务进入 `cancelled`，文档保持待处理且不会被普通搜索或项目刷新自动重新排队；只有显式重试或新的 IndexVersion 迁移可以重新提交。
- 应用退出导致的上下文取消与用户取消分离：前者重新排队以便启动恢复，后者保持取消。任务提交索引事务后使用独立有界上下文完成元数据，避免“索引已替换但任务被记为取消”。
- 单文档重建在项目本地 SQLite 事务中删除旧 Chunk 并插入新 Chunk；提交前上一份 ready 索引继续可查询，失败或取消不能暴露半成品。
- 固定语料评测不接触用户文档或外部网络；语料、查询和确定性向量均在仓库内，分别约束 BM25 与混合检索的命中、召回、排序和可定位性。

## P6.0 斜杠命令与手动压缩控制

- 只有输入框完整内容精确匹配已注册 `/command` 时才执行本地动作；未知名称、带空格参数、路径和普通斜杠文本不被解释为特权命令。
- 本地命令不创建 Message、Run 或 ToolCall，不进入模型上下文，也不接受模型、文档、Skill 或 MCP 返回内容动态注册命令。
- `/mcp` 二级面板只读取脱敏后的 Server 与 CapabilitySnapshot；开启状态来自 `ready/degraded` 运行态而不是配置文本，Tools/Resources/Prompts 仍视为不可信显示数据，面板不读取 SecretEnv 明文。
- MCP 启动/关闭继续调用既有 Service 生命周期接口，不能绕过 enabled、trust、SecretStore、Transport 或 ToolRegistry 边界。Skill 二级列表只显示 `entry=true` 且允许加载的动态 Skill，选择后插入 `Use the <name> skill:`；它不能暗中启用 Skill 或直接加载正文。
- `/compact` 仅允许最新 Run 处于终态时执行，固定该 Run 的消息边界并复核操作期间会话未切换；已有 checkpoint 必须先通过 SHA256 完整性校验。
- 手动压缩继续使用无 Tool Definition 的科研摘要请求，历史以不可信 JSON 数据输入。每轮保存独立 revision 和精确边界，原始消息、Provider Turn、ToolCall 与 Citation 均不删除。
- 前端只接收 revision、边界和来源计数，不接收 checkpoint 摘要正文；模型调用 Token 继续计入最近 Run 的用量统计。

## P6 科研发现、证据与产物控制

- 公共科研 Connector 只允许注册来源的固定 Host 和结构化查询，模型不能传入任意 URL；取消、30 秒外层超时、按 Host 限速、有界重试、响应上限和缓存集中在共享网络层。
- 在线命中、来源元数据、摘要和开放全文均是不可信研究数据。候选只有经过用户显式纳入、项目 Attachment/Knowledge 索引及 Run/Chunk/Quote SHA256 校验后才能取得可信 Citation 身份。
- 去重优先使用规范强标识符并保留每条来源记录；冲突字段不会被静默覆盖。用户修订保存历史，模型不得补猜缺失书目信息。
- 证据矩阵中的研究事实必须绑定当前书目的本地 Chunk；元数据/摘要与全文使用不同证据等级。模型条目必须先处于 `pending`，审核只能改变状态，正文、来源、Quote、哈希和定位快照不可更新。
- ArtifactVersion、ArtifactExport、Lineage、Citation、书目和证据快照在生成后不可变；下载使用 SHA256 校验后的原子 no-replace 发布，失败或竞态不会覆盖已有用户文件。

## P6.6 项目归档与动态 Skill 快照控制

- 动态 Skill 保持单一用户可见 Research Agent。普通对话最多召回 20 个、模型最多看到 16 个候选；科研启动最多暴露 8 个候选并只允许冻结 4 个核心 Skill；正式 Workflow 阶段只暴露宿主冻结且明确绑定到该阶段的 Skill，独立审查至多复核整条路线的 4 个核心 Skill。这些目录都不会提前批量注入正文，模型逐个加载的每个 Skill 都形成独立、不可变且有序的 `run_dynamic_skills` 快照。
- `.sciaide-project` 是不可信 ZIP 输入。导入限制条目数、单项/总大小和压缩比，拒绝路径穿越、重复/大小写碰撞、控制字符、Windows 设备名、尾随点/空格、超长组件、链接、未知和缺失条目，并逐项验证 Manifest 与 SHA256。
- 归档数据库必须通过 SQLite 头、必需 Table、迁移名称/checksum、项目身份和外键检查；View 不能冒充 Table。项目及索引 ID 只在隔离暂存中重映射，历史证据和 Artifact 快照保持原语义。
- 归档不包含 API Key、模型 Header、MCP 配置/Secret、识图或 Embedding 凭据、权限 Grant、pending Approval、第三方 Skill 包和临时缓存。历史模型恢复为禁用占位、会话恢复为 `Plan`；旧 Skill 绑定仅按本机完全匹配的哈希重新绑定，动态 Run Skill 正文/provenance 快照则随 Run、Project 和 ToolCall 一起重映射。
- 恢复默认创建新项目。文件、数据库和索引全部通过后才原子 no-replace 发布 Workspace；全局数据库合并失败会移走 Workspace。启动 marker 恢复清理中断暂存并隔离未提交目录，不暴露半恢复项目。

## P7 本地执行、Python 与 Workflow 控制

- `builtin.shell.execute` 与 `builtin.python.execute` 是 Registry 中固定的高风险、非幂等工具；模型不能注册解释器、修改权限定义或绕过 JSON Schema、PolicyEngine、审批、ToolExecutor 和 ToolCall 持久化。Plan 模式审批卡默认展示完整参数、运行时、Workspace 工作目录与超时。
- 工作目录、Python 脚本和声明产物必须是当前 Workspace 下的规范相对路径，拒绝 `.sciaide`、绝对路径、symlink、junction 和 reparse point。Shell/Python 获批后仍拥有当前用户权限，路径校验不能阻止命令主动读取 Workspace 外绝对路径或联网，因此该执行器不是强安全沙箱，不能运行不可信代码。
- 子进程环境从核心 allowlist 构造，不继承完整应用环境，名称包含 `KEY`、`SECRET` 或 `TOKEN` 的变量被排除；PowerShell 使用 `-NoProfile`，不注入模型、MCP、视觉渠道或 Credential Manager 密钥。残余风险是普通允许变量及用户目录本身仍可能包含敏感信息。
- Windows 在进程挂起时先纳入带 kill-on-close 的 Job Object 再恢复，启动/纳管与应用关闭串行化；超时、取消、应用退出和根进程正常结束后都终止整个进程树。`CTRL_BREAK` 仅为 best effort，最终由 Job Object 强制清理。
- stdout/stderr 分流并持续排空，分别只向模型保留前 64 KiB；总字节数、SHA256 和截断状态进入审计，结束后管道另有 2 秒 drain timeout。输出内容仍是不可信 Tool 数据，不能扩大权限。
- 解释器路径、版本与文件 SHA256，脚本路径/SHA256或内联命令 SHA256、工作目录、timeout、环境变量名称、PID、退出码、终止原因及输出摘要进入 `process_execution_audits`。审计不额外保存命令正文；遗留活动记录在启动时标为 `app_shutdown`。
- 只有成功退出、显式声明且内容在本轮新增或改变的 Workspace 常规文件才能进入 Artifact；失败、取消、超时、旧内容、未声明文件和 `.sciaide` 路径均不登记。
- 产品策略规定网络默认允许：Shell、一次性 Python 和项目 Kernel 调用获准后可直接联网，不追加 `network.domain` 弹窗、域名白名单或代理配置；已知目标仍可进入权限快照和执行审计。该策略不代表远端可信，也不阻止获准脚本上传其可读取的数据；Plan 模式依靠完整调用确认降低风险，Full Access 下该风险由用户显式选择承担。应用密钥仍不进入子进程环境。
- 项目虚拟环境位于托管项目私有目录，环境记录固定基础解释器 SHA256、版本、架构、`pip freeze` 和锁定包。创建、重建和安装使用同卷暂存及原子替换；`pip install` 只能通过独立高风险 Tool 审批，Skill 和 Workflow 不能把依赖安装藏在普通 Kernel 步骤中。
- Kernel 按项目串行执行并运行在 Windows Job Object 内，默认限制完整进程树为 1 GiB，取消、超时、空闲、`MemoryError` 和应用关闭后淘汰进程。该限制不约束 CPU、文件系统或网络，不构成不可信代码沙箱。
- Kernel 声明输入必须是 Workspace 常规文件，执行期间设为只读并在结束后复核 SHA256；输出只允许新建于 `analysis-output/`，失败时清理声明文件和本轮图片。只读文件属性不是对恶意当前用户进程的强隔离，仍依赖用户只批准可信分析代码。
- Kernel 结果记录代码、结构化输入、文件输入、环境、输出和异常快照哈希。总 `reproductionSha256` 按声明顺序使用内容摘要而忽略易变路径名，路径到 SHA256 的完整映射仍保存在独立审计/provenance 中；该哈希证明这些记录一致，不证明科学结论正确，也不证明远程响应可重现。
- Workflow 定义、模板和输入均是不可信数据。编译器拒绝循环、类型错误、未知或变化的 Tool、路径逃逸、嵌套密钥和超大图，并冻结 Tool Schema、风险、权限、版本和幂等属性；执行时仍重新进入统一 Tool 权限管道。
- Workflow AI 阶段通过普通 Chat Run 驱动 Agent Loop 时，不能按普通聊天作用域执行工具。执行器使用持久的 `workflow_ai_chat_runs → workflow_ai_executions → workflow_runs` 绑定恢复 `researchTaskId`，并把 Workspace、Python、Shell 和 Kernel 根限定到 `<Workspace>/.sciaide/tasks/<taskID>/`；普通 Chat Run 无该绑定时才使用项目 Workspace。绑定缺失或查询失败不得静默降级，否则 `workspace.list` 可能暴露项目根目录中的旧任务、历史未归属或已移出知识库但仍保留的文件。
- 知识库“移出”只撤销知识文档、索引和检索资格，不物理删除附件、Workspace 原文件或历史消息；科研产物“删除”是可恢复软删除。保留的内容寻址对象和根目录文件不授予任何新任务读取权限，新任务只接受当前任务及用户明确设为 `project_shared` 的资源。
- Workflow 检查点只在步骤结果和事件事务提交后推进。重启不重放已提交步骤；未提交的幂等步骤可恢复，非幂等活动步骤进入 `outcome_unknown`，用户明确确认潜在副作用前不能重试。人工决策与审批使用条件提交防止双击或并发请求重复推进。

| Prompt 注入诱导工具执行 | P2 | JSON Schema、PolicyEngine、Approval、预算 |
| 路径穿越和 junction/symlink | P2 | 已实现 PathGuard、`os.Root`、Workspace 根和安全回归测试；写路径仍在 P2.6 |
| SSRF 和 DNS rebinding | P2/P3 | NetworkClient、地址复查、域名/端口权限 |
| 恶意 MCP 子进程或远端服务 | P3 | 已实现首次信任、SecretEnv 隔离、最小环境、生命周期恢复和统一权限管道；Job Object/DNS rebinding 仍需发布加固 |
| Skill 供应链、上下文污染和自动脚本执行 | P4/动态 Skill | OpenScience 默认资源只读嵌入；Project/User/Git 来源受路径、大小、链接、内容审查和 SHA256 约束；正文按需加载并快照，资源只读 UTF-8 文本，脚本无执行入口；签名发布者与在线市场仍属后续生态能力 |
| 超长会话裁剪导致任务状态丢失 | P4 加固 | 分层上下文预算、完整 Run 组、无工具 checkpoint、消息边界、revision、SHA256 和失败关闭；模型摘要的语义损失仍需人工复核 |
| 恶意论文中的指令 | P5 | 数据边界、来源标记、系统规则优先级 |
| 公共数据库恶意元数据、SSRF 或来源降级 | P6.3/P6.4 | 固定 Host、结构化 Connector、网络上限、部分失败、显式纳入、本地 Citation 校验 |
| 模型伪造书目或篡改证据审核 | P6.5 | 字段来源、修订历史、本地 Chunk、证据等级、pending 起点和不可变数据库触发器 |
| 多 Skill 上下文挤占或错误路由 | 动态 Skill | 当前任务+最近三条用户任务+同会话最近加载项、中英文 token/n-gram/科研别名召回最多 20 项，模型可见最多 16 项并最终重排；长正文按章节加载且完整快照，最近项不自动继承；模型仍可能漏选或错选，用户可用 `/skill` 显式指定 |
| 恶意项目归档、凭据泄露和半恢复 | P6.6 | 无密钥快照、隔离预检、ZIP/SQLite/SHA256 校验、ID 重映射、原子发布和启动恢复 |
| Shell/Python 越界访问、联网外传、凭据泄露和子进程残留 | P7.1 | 工具调用授权、Workspace 输入路径校验、最小无密钥环境、Windows Job Object、取消/超时/关闭清理、输出与执行审计；联网按产品策略默认允许，仍是当前用户权限而非强沙箱 |
| Python 依赖漂移、Kernel 污染、内存耗尽和半产物 | P7.2 | 独立虚拟环境、解释器/冻结锁/环境指纹、安装单独审批、项目串行 Kernel、1 GiB 进程树预算、输入复核、失败输出回滚、路径无关复现哈希、真实 XLSX 双项目重放和停止/重启/空闲回收 |
| 上游 Skill 虚假宿主能力、隐式脚本或凭据承诺 | P7.3 | 派生转换审计、资源先物化检查、正式 Tool 审批、禁止自动凭据/计费/托管 API、未知能力降级为用户配置的外部服务 |
| 恶意 Workflow、恢复重放和未知副作用 | P7.4/P7.5 | 版本化 Schema、静态图/类型/路径/密钥/体积校验、冻结 Tool 契约、事务检查点、幂等恢复、`outcome_unknown`、副作用确认和并发条件提交 |
| 科研流程把在线候选或模型文本提升为可信证据 | P7.6 | 人工候选和 Citation 选择、P6 本地 Chunk/证据哈希复核、不可变 Artifact lineage、确定性 DOCX/PDF 导出和重启闭环测试 |
| 更新包替换 | P8 | 代码签名、更新签名、SBOM、回滚 |

## 6. P0 验证案例

- 日志字段名为 `api_key`、`authorization`、`cookie` 时值被替换。
- 日志消息或普通字段包含已知 Secret 时 Secret 不落盘。
- 数据库关闭再打开后 Project 保持存在，迁移不重复执行。
- 取消的 FakeModel Stream 返回 `context.Canceled`。
- 前端 CSP 不允许远程脚本。

本文件必须在每次引入新权限、网络能力、文件类型、执行器或更新机制时更新。
