# SciAide 威胁模型（P1/P2）

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

- Codex 风格 `SKILL.md` 只作为导入源，在随机暂存目录中归一化为版本化 SciAide 包；不会把源目录或绝对路径交给模型。

- Run 仅为显式选择或确定性 trigger 命中的 Skill 读取完整正文，并在首次模型请求前保存正文、包路径、来源归档及哈希的不可变快照。
- catalog 有界且明确标记省略项；显式 Skill 未知、未启用或不可用时记录状态，不允许静默伪装为已加载。
- `builtin.skill.resource.read_text` 只接收当前 ToolCall 隐式 Run、已选 Skill ID 和规范包内相对路径；拒绝绝对路径、`..`、反斜杠、Windows 保留名/ADS 字符、符号链接、二进制和超限文件。
- 资源读取先复核当前安装包哈希；包被替换或卸载后仅从 Run 快照绑定、重新校验 SHA256 的原始来源归档读取，并复核归档内 `SKILL.md` 内容哈希。
- Skill 正文与资源均为 contextual user 数据。资源读取仍是普通 ToolCall：Plan 模式需要用户确认，Full Access 也不能绕过 Registry、Schema、超时、结果大小及取消边界；Skill Manifest 权限不参与授权。
- 内置科研 Skill 的规范原件只读嵌入二进制，但落地时仍经过普通包的暂存、校验、来源归档和原子安装；同版本用户包不会被启动流程覆盖，内置正文也不获得更高指令或工具权限。

## 上下文压缩加固

- `/v1/models` 的上下文元数据属于不可信可选声明；只接受有界正整数，缺失、畸形或超范围时使用显式 fallback，不通过大请求探测模型极限。
- checkpoint 请求不提供 Tool Definitions，历史消息以 JSON 不可信数据输入；摘要不能扩大权限、改变系统规则或证明历史内容真实。
- checkpoint 保存精确消息边界和 SHA256。加载时哈希不一致会中止请求，原始消息和 Tool/Provider 状态不会被 checkpoint 覆盖或删除。
- Provider 原生 Turn 仍按不可拆分组进入最近上下文；checkpoint 不保存 thinking、signature、encrypted reasoning 等隐藏协议载荷。

残余风险：模型生成的摘要是有损且可能遗漏细节，SHA256 只能证明本地摘要未被修改，不能证明摘要语义完整。超长会话和多次压缩仍可能降低回答准确性，关键科研数据与引用必须回到原始文献、Workspace 文件和聊天记录复核。

## P5.3 Embedding 与混合检索控制

- Embedding 默认关闭；只有用户在知识库窗口显式启用并保存后才请求 `/v1/embeddings`，程序启动和纯 BM25 查询不会隐式探测服务。
- Embedding API Key 使用系统凭据库，数据库、项目索引、日志和配置返回值均不保存明文密钥。HTTP 重定向不携带凭据继续请求。
- Base URL 只接受无 UserInfo、Query 和 Fragment 的 HTTP(S) 地址；响应数量、索引、有限数值、维度和大小均有边界校验。
- IndexVersion 固定 Model ID、实际维度和不含密钥的配置指纹。身份不一致时拒绝写入或查询，配置变化必须建立新影子索引。
- 向量只写入当前项目私有缓存，并通过 Chunk 外键级联删除；模型不能指定索引路径、向量维度或项目 ID。
- 查询向量缓存不保存搜索词明文，只保存绑定 Embedding 配置指纹的 SHA256；缓存限定在当前项目 IndexVersion，最多 512 条并按最近最少使用清理。
- Embedding 服务失败不能关闭知识检索：查询降级为 FTS5/BM25，构建失败不替换上一版 ready 索引。

| Prompt 注入诱导工具执行 | P2 | JSON Schema、PolicyEngine、Approval、预算 |
| 路径穿越和 junction/symlink | P2 | 已实现 PathGuard、`os.Root`、Workspace 根和安全回归测试；写路径仍在 P2.6 |
| SSRF 和 DNS rebinding | P2/P3 | NetworkClient、地址复查、域名/端口权限 |
| 恶意 MCP 子进程或远端服务 | P3 | 已实现首次信任、SecretEnv 隔离、最小环境、生命周期恢复和统一权限管道；Job Object/DNS rebinding 仍需发布加固 |
| Skill 供应链、上下文污染和自动脚本执行 | P4 | P4.1/P4.2 已实现严格 Manifest/内容/全包哈希、随机暂存、安全 ZIP 解压、显式替换、来源归档、引用保护和脚本不执行；P4.3 仅渐进读取选中正文，以 contextual user 优先级注入，并在模型请求前保存不可变 Run 快照；签名发布者与在线市场仍属后续生态能力 |
| 超长会话裁剪导致任务状态丢失 | P4 加固 | 分层上下文预算、完整 Run 组、无工具 checkpoint、消息边界、revision、SHA256 和失败关闭；模型摘要的语义损失仍需人工复核 |
| 恶意论文中的指令 | P5 | 数据边界、来源标记、系统规则优先级 |
| Python 逃逸和资源耗尽 | P7 | 明确非沙箱、进程树/资源限制、强授权 |
| 更新包替换 | P8 | 代码签名、更新签名、SBOM、回滚 |

## 6. P0 验证案例

- 日志字段名为 `api_key`、`authorization`、`cookie` 时值被替换。
- 日志消息或普通字段包含已知 Secret 时 Secret 不落盘。
- 数据库关闭再打开后 Project 保持存在，迁移不重复执行。
- 取消的 FakeModel Stream 返回 `context.Canceled`。
- 前端 CSP 不允许远程脚本。

本文件必须在每次引入新权限、网络能力、文件类型、执行器或更新机制时更新。
