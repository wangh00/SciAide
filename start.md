# SciAide 科研智能体桌面平台——架构与开发实施基线

> 文档状态：开发基线（Baseline）
> 更新日期：2026-09-09
> 适用范围：SciAide 桌面端 MVP 至稳定版
> 本文记录架构与验收基线，不代表全部已实现；当前进度、限制和构建以 [当前状态](docs/CURRENT_STATE.md) 为准。

---

## 1. 文档目的

本文档不是单纯的模块清单，而是 SciAide 后续开发必须共同遵守的架构、协议、安全边界和阶段验收基线。其目标是：

1. 先打通可测试、可恢复的最小纵向闭环，再增加 MCP、Skill、知识库和复杂工作流。
2. 明确 Tool、MCP、Skill、Workflow 四种扩展能力的边界，避免相互混用。
3. 保证模型、工具和外部内容均不能绕过权限系统直接执行高风险操作。
4. 使数据库、模型供应商和桌面 UI 都可以替换，而不侵入 Agent 核心。
5. 每个开发阶段都有前置条件、交付物、测试和退出标准，未通过门禁不得进入下一阶段。

“没有漏洞”无法通过架构文档作绝对保证；本基线通过最小权限、显式授权、密钥隔离、可审计执行、故障恢复和安全测试降低风险。发布前仍必须完成依赖扫描、威胁建模、渗透测试和人工代码审查。

---

## 2. 产品定位

| 项目 | 定义 |
|---|---|
| 产品名称 | SciAide |
| 用户 | 科研新手、研究生、教师及科研工程人员 |
| 形态 | 本地优先的跨平台桌面 AI Agent |
| 技术栈 | Go + Wails + React + TypeScript |
| 核心能力 | 多模型、自定义 API/Key、原生工具调用、MCP、Skill、科研知识库、引用溯源、科研产物管理 |
| 数据策略 | 默认本地存储；仅在用户选择云模型或联网工具时发送必要数据 |
| 设计原则 | 新手友好、来源可追溯、执行可解释、默认安全、扩展可控、失败可恢复 |

### 2.1 核心用户流程

用户围绕“科研项目”而不是孤立的聊天会话工作：

```text
创建科研项目
  → 添加论文、笔记、网页或数据集
  → 选择模型与隐私策略
  → 用自然语言提出任务
  → 查看 Agent 计划、工具调用和授权请求
  → 获得带引用的回答或文件产物
  → 继续修改、导出并保留完整执行记录
```

### 2.2 MVP 必须实现

- 项目、会话和消息持久化。
- 多个模型配置 Profile，自定义 Base URL、Model、Header 和 API Key。
- API Key 安全存储，禁止明文进入配置、SQLite、日志和前端状态。
- 流式多轮对话、停止生成、错误重试。
- 模型原生 Tool Calling 与最小 Agent Loop。
- 工具权限确认、执行时间线和运行记录。
- MCP Server 的添加、连接、能力发现和工具调用。
- 本地 Skill 的安装、启用和显式激活。
- 文献导入、混合检索和页码级引用。

### 2.3 MVP 暂不实现

- 在线技能市场和自动更新第三方 Skill。
- 无监督的长时间自主执行。
- 将 Python 模块白名单宣传为“安全沙箱”。
- 默认执行任意 Shell 命令。
- 多设备云同步、多人协作和服务端账户系统。
- 每次普通对话都先生成 DAG。

---

## 3. 不可破坏的架构约束

以下约束优先级高于具体库或目录设计：

1. **领域与应用层不依赖 Wails、SQLite、HTTP SDK、操作系统 Keychain 或具体模型 SDK。**
2. **Port 接口由使用方定义，Adapter 实现接口，依赖在 Composition Root 注入。**
3. **命令和查询使用强类型直接调用；事件仅用于状态通知、审计和 UI 推送。** 不得用全局 EventBus 隐藏核心调用链。
4. **LLM、MCP 描述、Skill 内容、网页和文献都是不可信输入。** 只有结构化 Tool Call 能进入工具执行管道。
5. **任何工具执行都必须依次经过：名称解析 → Schema 校验 → 权限评估 → 必要时用户确认 → 超时执行 → 结果持久化。**
6. **密钥只存在于后端 SecretStore。** React 前端、普通配置、日志和导出包只能看到 `secret_ref` 或掩码。
7. **默认文件权限限制在当前 Workspace。** 越界访问、覆盖、删除和进程执行必须按策略处理；已获准执行的联网 Tool、Shell 与 Python 默认可以访问网络，不再追加逐域名授权或网络配置。
8. **先持久化关键状态，再向 UI 发布事件。** UI 不是事实来源，重启后必须能从数据库重建界面。
9. **所有长任务都接收 `context.Context`，支持取消、超时和进程树清理。**
10. **运行中断不得自动重放有副作用的工具调用。** 必须从安全检查点恢复或要求用户确认。
11. **所有外部协议和本地 Manifest 都必须版本化。** 包括数据库迁移、事件 Envelope、Skill Schema 和导入导出格式。
12. **禁止在业务代码中到处使用 `map[string]interface{}`。** 动态边界使用 `json.RawMessage`，进入核心前完成类型校验。

---

## 4. 总体架构

```text
┌──────────────────────────────────────────────────────────────┐
│ Presentation：React                                          │
│ Onboarding / Projects / Chat / Sources / Runs / Artifacts    │
│ Models / MCP / Skills / Permissions / Settings               │
└──────────────────────────┬───────────────────────────────────┘
                           │ 生成的 Wails Binding + 版本化事件
┌──────────────────────────▼───────────────────────────────────┐
│ Inbound Adapter：Wails Facade                                 │
│ 参数校验、DTO 转换、错误映射；不包含业务逻辑                    │
└──────────────────────────┬───────────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────────┐
│ Application Use Cases                                        │
│ Project / Conversation / Chat / Import / Model / MCP / Skill │
│ Approval / Artifact / Settings                               │
└──────────────┬───────────────────────────┬───────────────────┘
               │                           │
┌──────────────▼────────────────┐  ┌───────▼───────────────────┐
│ Agent Runtime                 │  │ Research Services          │
│ AgentLoop / ContextBuilder    │  │ Ingestion / Retrieval      │
│ ToolRegistry / PolicyEngine   │  │ Citation / Artifact        │
│ RunRecorder / Budget          │  │ Optional WorkflowEngine    │
└──────────────┬────────────────┘  └───────┬───────────────────┘
               │          Outbound Ports   │
┌──────────────▼────────────────────────────▼───────────────────┐
│ Infrastructure Adapters                                      │
│ Model Providers / MCP Client / Builtin Tools / SQLite        │
│ SecretStore / Workspace FS / HTTP / Process / Vector Index   │
└──────────────────────────────────────────────────────────────┘
```

### 4.1 各层职责

| 层 | 可以做 | 不可以做 |
|---|---|---|
| Presentation | 展示、输入、局部 UI 状态、订阅事件 | 保存 API Key、直接访问数据库、直接启动进程 |
| Wails Facade | DTO 校验与转换、调用 Use Case、映射错误 | 编排 Agent、直接写 SQL、保存全局业务状态 |
| Application | 事务边界、用例协调、权限用例、任务生命周期 | 依赖具体 Provider、Wails 或操作系统 API |
| Agent/Research | Agent 循环、上下文构建、检索、引用、策略判定 | 绕过 Port 访问外部资源 |
| Infrastructure | 实现数据库、模型、MCP、文件和进程适配器 | 反向依赖 UI 或包含产品决策 |
| Bootstrap | 创建对象、读取启动配置、依赖注入、生命周期管理 | 承载业务规则 |

### 4.2 同步调用和事件的边界

- 创建项目、发送消息、批准工具、修改配置：强类型 Use Case 调用。
- Token 增量、运行状态变化、索引进度、MCP 状态：事件通知。
- 后端 HTTP 流不得直接以 SSE 暴露给前端；Provider Adapter 解析后转换为内部事件，再通过 Wails 推送。
- 文本增量按 20～50ms 或一定字符数合并，避免每个 Token 跨 WebView 推送。
- UI 断开或丢事件后，以 `GetRunSnapshot(runID)` 重新同步，不依赖事件补齐全部状态。

---

## 5. 核心概念边界

| 概念 | 定义 | 是否直接执行 |
|---|---|---|
| Tool | 一个带 JSON Schema 输入和结构化输出的原子能力 | 是，必须经过 PolicyEngine |
| MCP | 外部进程或服务提供 Tool、Resource、Prompt 的标准协议 | MCP Tool 可执行；Resource/Prompt 仅作为内容读取 |
| Skill | 领域指令及随包参考资料/素材/脚本源码，由模型按任务语义动态加载 | Skill 本身和随包脚本都不自动执行；实际能力只能来自已注册 Tool |
| Workflow | 有状态、可检查点恢复的确定性步骤图 | 是，但每个执行节点仍经过工具权限管道 |
| Agent Loop | 模型在文本响应与 Tool Call 之间迭代的运行循环 | 只执行经过验证的 Tool Call |

### 5.1 为什么不以 DAG 作为第一核心

普通问答和多数工具调用不需要先进行意图分类和 DAG 规划。MVP 使用模型原生 Tool Calling 的 Agent Loop，减少规划幻觉和实现复杂度。DAG/WorkflowEngine 只在后续用于可重复、可恢复、依赖明确的科研流程。

---

## 6. Agent Runtime

### 6.1 Agent Loop 标准流程

```text
1. ValidateRequest
2. 保存用户消息并创建 Run(queued)
3. Run → running
4. ContextBuilder 组装上下文、Skill、检索结果和 Tool Definitions
5. 调用 ChatModel.Stream
6. 若模型返回文本：流式记录并最终保存 Assistant Message
7. 若模型返回 Tool Call：
   a. ToolRegistry 解析命名空间
   b. JSON Schema 校验并拒绝未知字段（按工具策略）
   c. PolicyEngine 评估权限
   d. 必要时 Run → waiting_approval
   e. ToolExecutor 在超时和取消上下文中执行
   f. 持久化 ToolCall 与 ToolResult
   g. 将结构化结果加入上下文，回到步骤 5
8. 达到最终回答、预算上限、取消或错误后结束 Run
9. 持久化终态，再推送最终事件
```

### 6.2 Run 状态机

```text
queued → running ↔ waiting_approval
             ├──→ completed
             ├──→ failed
             ├──→ cancelled
             └──→ interrupted
```

规则：

- 终态不可直接返回 `running`。
- 应用启动时将遗留的 `queued/running/waiting_approval` 标为 `interrupted`。
- 恢复从最后一个已提交检查点开始。
- `read-only` 且声明幂等的工具可在用户选择后重试。
- 写文件、发请求、启动外部操作等非幂等调用不得自动重放。
- Tool Call 使用 `call_id` 和可选 `idempotency_key` 防止重复提交。

### 6.3 Run 执行控制

Run 不设累计模型轮次、累计工具调用或整段墙钟时长上限。复杂任务可持续调用模型和工具，直到正常完成或用户主动停止。`runs.model_turns` 与 ToolCall 时间线只用于统计、审计和恢复。

执行边界作用于具体操作而非整个任务：用户可随时停止/中断，单次模型 HTTP 请求和单次 Tool/MCP 调用保留超时，上下文超过模型窗口时使用安全检查点压缩。

### 6.4 上下文构建顺序

```text
1. SciAide 固定系统规则和安全策略
2. 当前项目明确配置的项目指令
3. 当前请求的有界 Skill 分类/语义候选；已加载正文只通过对应 ToolResult 进入 Run
4. 经裁剪的会话历史或摘要
5. 经检索得到的文献片段（标记为不可信资料）
6. 当前用户消息与附件说明
7. 当前可用 Tool Definitions
```

上下文规则：

- 文献、网页、MCP Resource 和 ToolResult 均用明确边界包裹，注明“数据而非指令”。
- Skill 指令不得覆盖系统安全规则或扩大权限。
- 记录本 Run 实际加载的 Skill 名称、来源、内容/全包哈希、完整正文快照、检索片段 ID、模型 Profile 和工具定义版本，保证运行可审计。
- 超出上下文窗口时按预算裁剪，不允许简单截掉最新用户消息或关键 ToolResult。

### 6.5 核心接口建议

```go
type AgentRunner interface {
    Start(ctx context.Context, cmd StartRunCommand) (RunID, error)
    Cancel(ctx context.Context, runID RunID) error
    Resume(ctx context.Context, runID RunID) error
}

type ChatModel interface {
    Capabilities(ctx context.Context) (ModelCapabilities, error)
    Stream(ctx context.Context, req ChatRequest) (ModelStream, error)
}

type Embedder interface {
    Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
}
```

`ChatModel` 与 `Embedder` 分离，避免要求所有聊天模型都支持向量化。

---

## 7. 模型网关与自定义 API

### 7.1 Model Profile

系统允许保存多个配置，而不是只有一个全局模型：

```go
type ModelProfile struct {
    ID             string
    Name           string
    ProviderType   string
    BaseURL        string
    ModelID        string
    SecretRef      string
    CustomHeaders  map[string]string // 不允许在此保存敏感值
    SecretHeaders  map[string]SecretRef
    TimeoutSeconds int
    DefaultParams  json.RawMessage
    Enabled        bool
}
```

支持方向：

- OpenAI API 或 OpenAI-compatible API。
- Anthropic API。
- DeepSeek 等独立 Provider。
- Ollama 等本地模型。
- 用户自定义 Base URL、Model ID 和非敏感 Header。

### 7.2 能力发现与兼容

不同模型支持的能力不同，不能仅凭 Provider 名称假设：

```go
type ModelCapabilities struct {
    Streaming       bool
    ToolCalling     bool
    Vision          bool
    StructuredOutput bool
    Reasoning       bool
    MaxContextTokens int
}
```

- Profile 保存后执行“连接测试”，但不得把密钥写入错误消息。
- 不支持 Tool Calling 的模型只允许普通聊天，或明确启用受限兼容模式；不得伪装为可靠原生工具调用。
- Provider Adapter 将不同厂商的内容块、Tool Call、Usage 和 FinishReason 规范化。
- 重试只用于明确可重试错误，如限流、临时网络错误；采用指数退避和抖动。
- 已收到部分流式输出后不得盲目重试，避免重复内容或重复 Tool Call。

### 7.3 SecretStore

密钥存储优先级：

```text
Windows Credential Manager
macOS Keychain
Linux Secret Service
```

如系统 Secret Service 不可用，只能在用户明确同意后使用应用级加密存储，并说明安全差异。禁止自动回退到明文。

SecretStore 接口：

```go
type SecretStore interface {
    Put(ctx context.Context, ref SecretRef, value []byte) error
    Get(ctx context.Context, ref SecretRef) ([]byte, error)
    Delete(ctx context.Context, ref SecretRef) error
}
```

安全要求：

- Wails API 只接收“设置/替换密钥”命令，不提供读取明文接口。
- React 仅收到 `configured: true` 和掩码。
- 日志字段名命中 `authorization/api_key/token/secret/cookie` 时自动脱敏。
- MCP 环境变量中的密钥通过 `secret_ref` 注入子进程，且不得继承应用全部环境变量。
- 配置导出、支持包和崩溃报告默认排除密钥。

### 7.4 数据出站与隐私模式

本地优先不等于所有处理都在本地。调用云模型、联网 Tool 或远程 MCP 时，必须把发送边界做成产品能力：

- 项目隐私模式分为 `local_only`、`ask_before_send`、`allow_configured_services`。
- `local_only` 只允许本地模型、本地 MCP 和不联网工具；组件不得静默降级到云服务。
- 首次向某模型服务、域名或远程 MCP 发送项目数据时，展示目标、数据类型和用途。
- 权限确认应展示将发送的数据摘要；敏感原文不能只显示“调用搜索工具”。
- ContextBuilder 为每个内容块保留来源与敏感级别，出站前由 EgressPolicy 再检查一次。
- 网络请求默认不携带会话中无关的文献、文件内容、环境变量或其他 Provider 的密钥。
- UI 明确标识当前使用本地还是云端模型，以及本次 Run 是否发生过数据出站。
- 数据库存储默认依赖操作系统账户与磁盘保护；若后续提供应用级加密，必须单独设计密钥恢复和备份方案，不能宣称当前 SQLite 明文文件已加密。

---

## 8. Tool 系统与权限模型

### 8.1 Tool 统一接口

```go
type ToolDefinition struct {
    QualifiedName string
    Description   string
    InputSchema   json.RawMessage
    OutputSchema  json.RawMessage
    Risk          RiskLevel
    Permissions   []PermissionRequirement
    Idempotent    bool
    Version       string
}

type Tool interface {
    Definition(ctx context.Context) (ToolDefinition, error)
    Invoke(ctx context.Context, call ToolCall) (ToolResult, error)
}
```

命名空间示例：

```text
builtin.workspace.read_file
builtin.knowledge.search
zotero.search_items
filesystem.write_file
skill.data_analysis.run_script
```

### 8.2 ToolResult 内容类型

```go
type ToolResult struct {
    Status      ToolResultStatus
    Text        string
    Structured json.RawMessage
    Artifacts   []ArtifactRef
    Citations   []CitationRef
    Truncated   bool
    Meta        ToolResultMeta
}
```

- 工具异常以结构化错误返回，不把 Go panic 或内部堆栈交给模型。
- 输出有大小上限；超限内容落为 Artifact，只向模型提供摘要和引用。
- 图片、文件和表格不能强行塞进字符串。

### 8.3 权限分类

| 权限 | 默认策略 | 示例 |
|---|---|---|
| `workspace.read` | Plan 每次确认；Full Access 自动允许 | 读取论文、笔记 |
| `workspace.write` | 默认逐次确认，可允许指定目录 | 生成 Markdown、CSV |
| `filesystem.external` | 必须逐次确认 | 访问 Workspace 外路径 |
| `network.domain` | 默认允许并记录目标范围，不单独弹窗或要求白名单 | Crossref、PubMed、Shell/Python 请求 |
| `process.execute` | 默认逐次确认 | Python、R、命令行工具 |
| `destructive` | 永远展示影响范围并逐次确认 | 覆盖、删除 |
| `secret.use` | 只允许指定 Adapter 使用 | 模型/MCP 认证 |

权限授予作用域：

```text
仅本次调用 / 本次 Run / 当前项目 / 指定 Tool+资源范围
```

“始终允许”通常必须带 Tool、项目或资源范围，禁止全局无边界授权。网络访问是明确例外：获准执行的联网 Tool、Shell、一次性 Python 和项目 Kernel 默认可以发起请求，不再要求逐域名审批、白名单或额外联网配置。

### 8.4 文件系统安全

文件工具必须：

1. 使用 `filepath.Clean`、`Abs` 和受控根目录解析。
2. 比较卷标和路径组件，禁止使用字符串前缀判断目录包含关系。
3. 检查现有路径及父目录的符号链接、junction/reparse point，防止绕过 Workspace。
4. 新文件使用临时文件 + `fsync` + 原子替换。
5. 覆盖前展示目标路径；删除默认进入应用回收区而非永久删除。
6. 限制单次读取、目录遍历深度、文件数量和输出大小。
7. 不允许模型自行访问安装目录、用户凭据目录或应用 SecretStore。

### 8.5 进程与 Python

第一版 Python Runtime 是“需要授权的本机进程执行器”，不是安全沙箱：

- 默认关闭，使用时明确提示风险。
- 使用独立工作目录和最小环境变量。
- 设置执行时间、内存、CPU 和输出上限；Windows 使用 Job Object 管理进程树。
- 取消时终止整个子进程树。
- Shell、一次性 Python 和项目 Kernel 获准执行后默认允许网络，不增加逐域名审批、白名单或联网弹窗；仍不注入模型 API Key、MCP Secret 等应用密钥。`pip install` 等会改变项目环境的操作继续按环境变更单独确认。
- 脚本和参数分离传递，禁止拼接 Shell 字符串。
- 后续如需要不可信代码隔离，应引入容器、虚拟机或平台沙箱，并单独威胁建模。

### 8.6 网络工具安全

- 所有内置 HTTP Tool 通过统一 `NetworkClient`，执行域名权限、代理、超时、响应大小和重定向限制。
- 对网络 Tool 默认阻止回环、链路本地、私有网段和云元数据地址；用户明确配置本地服务时按精确主机和端口授权。
- 每次重定向和 DNS 解析后重新检查目标地址，防止开放重定向与 DNS rebinding 绕过。
- 限制重定向次数、下载大小、内容类型和压缩后展开大小。
- 禁止把任意 URL 响应直接当作系统指令；网页内容以不可信资料进入 ContextBuilder。
- 自定义模型 Base URL 和远程 MCP URL 属于用户配置的服务端点，但仍使用独立客户端、TLS 策略和密钥作用域，不能共享 Cookie 或 Authorization Header。

---

## 9. MCP 子系统

### 9.1 组件

```text
MCPManager
├── ServerRegistry
├── ConnectionManager
├── TransportFactory
│   ├── StdioTransport
│   └── StreamableHTTPTransport
├── CapabilityRegistry
├── MCPToolAdapter
├── MCPResourceAdapter
├── MCPPromptAdapter
└── HealthMonitor
```

MVP 支持 `stdio` 与 Streamable HTTP。旧式 SSE 仅作为确有需求的兼容适配器，不作为新配置默认选项。

### 9.2 MCP Server 配置

```go
type MCPServerConfig struct {
    ID          string
    Name        string
    Transport   string
    Command     string            // stdio
    Args        []string          // stdio，不拼接 Shell
    WorkingDir  string
    URL         string            // HTTP
    Env         map[string]string // 仅非敏感值
    SecretEnv   map[string]SecretRef
    Enabled     bool
    AutoStart   bool
    Trust       TrustLevel
}
```

### 9.3 生命周期

```text
disabled → disconnected → starting → initializing → ready
                                      ├→ degraded
                                      └→ failed
ready → reconnecting → ready/failed
ready → stopping → disconnected
```

要求：

- 启动后先完成 `initialize` 和能力协商，再允许调用。
- 维护 Server 级超时、健康状态、stderr 日志和最后错误。
- 处理工具、资源和 Prompt 列表变化通知。
- HTTP 断线使用有上限的退避重连；stdio 异常退出不得无限拉起。
- 应用退出时优雅关闭客户端和子进程，超时后终止进程树。
- 工具调用必须携带 `server_id`、工具原名和当前定义版本。

### 9.4 信任边界

- MCP Server 的 Tool 描述、Prompt、Resource 和错误消息都是不可信数据。
- MCP Tool 统一适配到 ToolRegistry，不能直接从 MCPManager 绕过 PolicyEngine 调用。
- Resource 和 Prompt 不自动注入上下文；必须由用户选择、Skill 显式引用或 Agent 经受控工具读取。
- 本地 `stdio` Server 本质上是本机程序，首次启用必须展示命令、参数、工作目录和权限。
- HTTP Server 默认要求 HTTPS；允许本机开发地址时显示清晰的安全提示。
- MCP Server 配置不能由模型或 Skill 静默修改。

---

## 10. Skill 系统

### 10.1 Skill 包结构与来源

```text
<skill-directory>/
└── literature-review/
    ├── SKILL.md
    ├── references/
    ├── assets/
    └── scripts/
```

`SKILL.md` 使用 YAML frontmatter 保存 `name`、`description`、可选 `category/tags/routing-aliases/entry/allowed-tools`，正文保存领域指令；references、assets 和 scripts 都是可选资料。`name` 使用稳定 ASCII 标识，`description/category/tags` 可完全使用中文；当领域术语不在内置概念词典中时，`routing-aliases` 可同时声明中英文任务说法。来源优先级固定为 `project > user > installed > default`。默认 OpenScience 目录随 EXE 只读嵌入；Project/User/Git Installed 来自受控磁盘目录。当前不扫描 `.claude/skills`。

### 10.2 `SKILL.md` 示例

```markdown
---
name: literature-review
description: Synthesize a traceable literature review from project evidence
category: research
tags: [literature, synthesis, citations]
routing-aliases: [文献证据综合, evidence synthesis]
entry: true
allowed-tools: [builtin.knowledge.search]
---

# Workflow

Search project evidence, compare methods and findings, and preserve citations.
```

### 10.3 发现、安装和动态加载规则

- Project 扫描 Workspace 内 `.openscience/`、`.synsc/` 和 OpenScience 配置中的相对 `skills.paths`；User 与 Git Installed 使用全局独立目录。
- 管理页按 Skill 名称控制是否允许模型加载；同名高优先级来源沿用该策略，开关不授予任何 Tool 权限。管理页同时展示能力审计等级并可筛选：`native`、`requires_dependency`、`requires_external_service`、`unavailable` 或第三方包的 `unreviewed`；版本化审计逐项记录所需 Tool、Python/CLI 依赖、外部服务和明确限制，并与当前 ToolRegistry 实时求交集，但不替代运行时权限或凭据检查。
- Git 安装使用随机暂存、文件/体积/链接/内容审查、固定 commit SHA 和原子发布；警告必须针对同一 SHA 显式二次确认，替换/卸载进入可恢复归档。
- 每个新 Run 只在首次模型请求注入分类摘要、固定科研路由和有界候选。请求与 Skill 元数据都会映射到同一中英文概念特征，并进行常见英文词形归一化和明确否定排除；`routing-aliases` 补充未知领域翻译。召回综合当前消息、最近三条用户任务及同会话最近真实加载 Skill，最多召回 20 项、向主模型暴露 16 项重排；最近项不自动继承，显式选择优先。
- 首次路由同时以事务保存候选顺序、总分/当前/最近分量、否定惩罚、连续性来源、显式选择、短名单状态、输入 SHA256 和候选不可变哈希；实际 `builtin.skill.load` 结果单独关联，因此可区分未召回、召回未加载和短名单外加载。路由输入不复制用户原文。
- 短正文完整返回；长 Markdown 先返回最多 256 个互不重叠的章节索引，再按稳定 `section-N` 读取；无标题长文按 rune 分页兜底。首次成功加载始终把完整正文和来源/哈希保存到 `run_dynamic_skills`，后续读取不因目录变化重写进行中的 Run。
- `builtin.skill.resource.list` 列出当前 Run 已加载 Skill 的 reference/asset/script、大小、媒体类型和文本可读性；`builtin.skill.resource.read_text` 只读哈希匹配包中的 UTF-8 文本。`allowed-tools` 只与 Registry 做能力诊断，不授予权限；脚本源码不能因安装、加载或读取自动运行。
- Skills 管理页可对当前解析后生效的四类来源包查看完整目录树，并逐文件打开只读视图。该用户界面浏览不要求先把 Skill 加载到 Run，但仍校验规范包内相对路径和当前全包 SHA256；UTF-8 文本最多读取 2 MiB，二进制或超大文件只显示类型与大小，不交给 WebView 主动渲染，也不提供执行入口。模型运行时资源读取继续遵守上一条“已加载 Run”边界，两者不能混用。
- OpenScience 默认目录以版本化逐文件 Manifest 固定来源仓库、版本、路径、大小和 SHA256；同步脚本默认只检查源树、311 个 Skill、1,624 个文件及 `LICENSE`/`NOTICE`，只有显式 `-Update` 才可从经过审查的上游源码通过暂存目录更新。

### 10.4 指令冲突和上下文预算

- 固定系统安全规则优先于项目指令和 Skill。
- 目录不批量注入正文；召回最多 20 个且模型可见候选最多 16 个，只含名称、截断描述和连续性标记，避免 311 个 Skill 挤占科研资料与最新用户消息。
- 模型可按任务需要加载一个或多个 Skill；每个正文最多 7,500 rune/页，长正文必须继续分页后再应用。
- 每次 Run 记录实际加载的 Skill 名称、来源、内容哈希、全包哈希和完整正文。工具循环与审批恢复复用该快照，不按当前目录重写。
- Skill 内容、目录元数据和 ToolResult 都是 contextual data，不能覆盖系统规则、扩大 Plan/Full Access、修改 MCP/模型配置或声明脚本已经执行。

---

## 11. 科研项目、知识库和引用

### 11.1 以 Project 为顶层聚合

```text
Project
├── Conversations
├── Sources
├── Knowledge Collections
├── Runs
├── Artifacts
├── Enabled Skills
└── Model/Privacy Policy
```

会话不能拥有全部项目数据；同一项目中的多个会话可以共享文献和产物。

### 11.2 文档导入流水线

```text
选择文件
  → 计算内容哈希和 MIME 检测
  → 保存 Source 元数据
  → 后台提取文本/OCR（可选）
  → 规范化并保留页码、段落和字符偏移
  → 分块
  → FTS 索引
  → Embedding
  → 索引版本提交
```

要求：

- 扩展名不作为唯一文件类型依据。
- 同内容哈希可检测重复导入。
- 分块保留 `source_id/page/section/start_offset/end_offset`。
- 记录解析器版本、分块策略版本、Embedding Model ID、维度和索引版本。
- 更换 Embedding 模型或维度时创建新索引版本，不在旧向量中混用。
- 导入失败保留可重试状态和错误原因，不生成“半完成但可检索”的文档。
- PDF 提取质量不足时提示 OCR，而不是输出无依据内容。

### 11.3 检索策略

MVP 采用混合检索：

```text
SQLite FTS5/BM25
  + 向量相似度
  + 元数据过滤
  → 分数融合
  → 可选 Reranker
  → 去重和上下文预算裁剪
```

初期向量可存 SQLite BLOB，并在 Go 中做适合小型知识库的相似度计算；通过 `VectorIndex` Port 保留替换空间。不要在 MVP 强依赖 Chroma/Python 服务。数据规模增长后再评估 `sqlite-vec`、LanceDB 或独立向量服务。

### 11.4 引用模型

每条引用至少保存：

```text
source_id
chunk_id
page/section
quote
start_offset/end_offset
retrieval_score
```

- UI 点击引用必须能定位原文。
- 最终回答中的引用编号关联结构化 `Citation`，不能只依赖模型生成的 `[1]` 字符串。
- 模型生成的 DOI、作者和年份不能直接视为事实；从 Source 元数据或外部学术 API 校验。
- 导出时引用样式与引用数据分离，便于后续支持 GB/T 7714、APA 等格式。

---

## 12. 数据模型与持久化

### 12.1 核心实体

```text
Project
Conversation
Message
MessagePart
Run
RunEvent
ToolCall
Approval
ModelProfile
MCPServer
DynamicSkillPolicy
RunDynamicSkill
Source
DocumentChunk
EmbeddingRecord
Citation
Artifact
PermissionGrant（历史兼容，不参与 P2.5 运行时决策）
```

### 12.2 Message 使用内容块

```go
type Message struct {
    ID             string
    ConversationID string
    Role           MessageRole // user | assistant | tool | system
    Parts          []ContentPart
    CreatedAt      time.Time
}

type ContentPart struct {
    Type       ContentPartType // text | image | file | tool_call | tool_result | citation
    Text       string
    Payload    json.RawMessage
}
```

工具调用、引用和附件不能塞进不受约束的 `metadata`。

### 12.3 建议数据库表

```text
projects
conversations
messages
message_parts

runs
run_events
tool_calls
approvals
permission_grants

model_profiles
model_profile_models
mcp_servers
skill_policies
run_dynamic_skills

sources
document_chunks
embedding_records
citations
artifacts

settings
schema_migrations
```

### 12.4 SQLite 规则

```sql
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
```

- 所有 Schema 变化使用版本化迁移，禁止运行时“如果缺列就 ALTER”的散落逻辑。
- 破坏性迁移前自动备份，并测试升级与回滚/恢复路径。
- Repository 不做业务决策，只负责持久化映射。
- 所有 SQL 使用参数化查询；动态排序、列名和过滤字段只能从代码白名单映射，不能拼接用户或模型输入。
- 关键写入使用事务：用户消息 + Run 创建、ToolCall 状态 + RunEvent、最终消息 + Run 终态。
- SQLite 连接和并发策略在 Storage Adapter 内统一管理，避免多个模块自行打开数据库。
- 定期执行完整性检查；备份使用 SQLite Backup API 或一致性快照，不直接复制正在写入的 DB 文件。

### 12.5 本地目录

运行数据不能写入安装目录或源码中的 `data/`：

```text
~/.sciaide/
├── config/        # 非敏感配置
├── data/          # SQLite 与默认托管 Workspace
│   └── workspaces/<project-id>/
├── cache/         # 仅跨项目的小型可重建缓存
│   └── project-archives/ # 项目归档随机暂存，启动时清理
├── logs/          # 脱敏日志
├── data/installed-skills/ # Git 安装的动态 Skill
├── data/user-skills/      # 用户创建的动态 Skill
├── data/skill-archives/   # 替换、卸载和删除的可恢复副本
├── mcp/           # MCP 运行元数据，不存明文密钥
└── backups/
    ├── trash/     # 被移除的托管 Workspace

用户选择的 Workspace/
├── sources/
└── .sciaide/              # SciAide 唯一保留的项目私有目录
    ├── project.json
    ├── attachments/       # SHA256 去重的持久附件原件
    ├── cache/             # 解析、OCR、索引和 Run 临时派生数据
    ├── artifacts/         # 可追溯的正式科研产物
    └── tmp/               # 同卷原子写暂存，结束后清理
```

全局配置与项目大体积数据必须分开。清理项目 `cache` 不能删除附件原件或 Artifact；普通 Workspace 工具不能直接遍历 `.sciaide`，只能通过项目作用域的附件/Artifact 服务访问。

---

## 13. 并发、取消与故障恢复

### 13.1 并发原则

- 每个 Run 有唯一根 `context.Context` 和 `cancel`。
- 子任务从根 Context 派生，不使用无主 Goroutine。
- 应用关闭时：停止接收新任务 → 取消 Runs → 关闭模型流 → 停止 MCP → 刷新存储 → 关闭数据库。
- Tool、Provider 和 MCP 调用必须设置独立超时。
- 使用有界队列和背压，禁止无界 Channel。
- 同一 Conversation 默认只允许一个写入型 Run；并发只读 Run 需明确设计消息合并规则。

### 13.2 事件 Envelope

```go
type EventEnvelope struct {
    Version       int
    EventID       string
    AggregateID   string
    AggregateType string
    Sequence      int64
    Type          string
    Timestamp     time.Time
    Payload       json.RawMessage
}
```

- `AggregateID + Sequence` 用于排序和去重。
- 关键事件写入 `run_events` 后再发给 UI。
- 高频文本 Delta 可批量保存，最终消息必须独立落库。
- UI 发现序列缺口时调用 Snapshot API。

### 13.3 崩溃恢复

- 启动时检测未完成 Run 并标记 `interrupted`。
- 展示最后成功步骤、未完成 ToolCall 和可恢复性。
- 同一 Run 若支持恢复，只能复用其不可变 Skill 快照；模型 Profile、MCP 工具定义和权限变化需要单独重新确认，不能用当前项目 Skill 覆盖历史快照。
- 已完成且有副作用的调用只复用其持久化结果，不再次执行。
- 临时文件使用 Run ID 命名，启动时清理过期且无引用的临时目录。

---

## 14. 错误、日志和审计

### 14.1 统一错误

```go
type AppError struct {
    Code        string
    UserMessage string
    Retryable   bool
    CorrelationID string
    Cause       error // 不序列化到前端
}
```

错误码示例：

```text
MODEL_AUTH_FAILED
MODEL_RATE_LIMITED
MCP_SERVER_UNAVAILABLE
TOOL_PERMISSION_DENIED
TOOL_INPUT_INVALID
WORKSPACE_PATH_DENIED
RUN_BUDGET_EXCEEDED
KNOWLEDGE_INDEX_STALE
```

### 14.2 日志

- 结构化日志字段包含时间、级别、模块、CorrelationID、RunID。
- 日志不记录完整 Prompt、论文正文、API Key、Authorization Header 和未经处理的 ToolResult。
- Debug 级 HTTP Body 必须显式启用且先脱敏，默认关闭。
- 日志轮转并限制总空间。
- “导出诊断包”先展示内容清单，默认只含版本、脱敏日志和健康状态。
- 产品遥测默认关闭；启用必须明确同意，且不上传研究内容。

### 14.3 审计

每次高风险动作记录：

```text
谁触发（用户/模型/Workflow）
Run 与 ToolCall
工具及版本
权限评估结果
用户批准范围
目标资源摘要
开始/结束时间
结果状态
```

审计记录不能保存密钥或无上限的大型输出。

---

## 15. 前端信息架构与新手体验

### 15.1 主导航

```text
项目
├── 对话
├── 资料库
├── 运行记录
├── 产物
└── 项目设置

全局设置
├── 模型
├── MCP
├── Skills
├── 权限
├── 数据与隐私
└── 诊断
```

### 15.2 新手友好要求

- 首次启动向导：选择 Workspace → 配置模型 → 测试连接 → 选择隐私模式 → 创建首个项目。
- 所有 API 错误给出可操作建议，而不只显示原始状态码。
- 工具确认框用自然语言说明“将做什么、访问哪里、可能产生什么影响”。
- Agent 时间线显示：思考阶段状态、工具名、输入摘要、授权、结果和引用；不展示或声称暴露模型私有思维链。
- 提供安全模式：仅聊天、只读工具、标准、开发者模式。
- 引用可点击定位原文；产物明确显示生成模型、时间和来源。
- 运行失败后提供“重试安全步骤”“复制错误编号”“打开诊断”的明确入口。

### 15.3 前端状态

- 服务端实体以查询结果为准；Zustand 只保存 UI 状态、缓存和乐观状态。
- Wails 生成的 TypeScript Binding 是调用入口，不手写重复 API 类型。
- 事件订阅必须在组件卸载时解除。
- API Key 不进入 Zustand、localStorage、错误上报或浏览器控制台。

### 15.4 WebView 内容安全

- 模型、文献、MCP 和 Tool 输出全部按不可信内容渲染。
- Markdown 默认禁用原始 HTML；如确需支持，必须经过严格 allowlist sanitizer。
- 代码高亮、数学公式、Mermaid 和 SVG 渲染器固定版本并设置输入/资源上限。
- 外链不在 WebView 内直接导航，交给受控系统浏览器打开，并限制 `file:`、`javascript:`、`data:` 等危险协议。
- 设置严格 CSP，禁止远程脚本、内联脚本和任意 WebSocket；开发配置不得进入生产包。
- Wails 只绑定最小 Facade 方法，禁止把 FileManager、ProcessRunner、SecretStore 等基础设施对象直接暴露给前端。
- 拖拽、剪贴板和文件选择得到的路径仍必须经过后端 Workspace/权限校验。

---

## 16. 推荐项目目录

采用“按能力聚合 + Port/Adapter”结构，避免巨大 `core`、万能 `utils` 和层层空目录：

```text
sciaide/
├── cmd/sciaide/
│   └── main.go
├── internal/
│   ├── bootstrap/                 # Composition Root、生命周期
│   ├── transport/wails/           # Wails Facade、DTO、事件桥
│   ├── app/                       # Application Use Cases
│   │   ├── project/
│   │   ├── conversation/
│   │   ├── chat/
│   │   ├── approval/
│   │   └── settings/
│   ├── agent/                     # AgentLoop、Context、Budget、Run
│   ├── model/                     # 模型 Port、规范化消息协议
│   │   └── adapters/
│   ├── tools/                     # ToolRegistry、Executor、内置工具
│   │   └── builtin/
│   ├── security/                  # PolicyEngine、SecretStore Port、脱敏
│   ├── mcp/                       # MCP Manager 与 Adapter
│   ├── skills/                    # Manifest、安装、激活、版本
│   ├── knowledge/                 # 导入、切分、检索、引用
│   ├── workflow/                  # 后期引入的确定性工作流
│   ├── artifacts/                 # 产物服务与导出
│   ├── storage/                   # Repository Port 和 SQLite Adapter
│   │   ├── sqlite/
│   │   └── migrations/
│   └── platform/                  # Keychain、文件、进程、OS 目录
├── frontend/
│   ├── src/
│   │   ├── features/
│   │   ├── components/
│   │   ├── stores/
│   │   ├── bindings/              # Wails 生成，禁止手改
│   │   └── App.tsx
│   └── package.json
├── contracts/
│   ├── skill.schema.json
│   ├── event.schema.json
│   └── export.schema.json
├── resources/
│   ├── prompts/
│   └── skills/builtin/
├── tests/
│   ├── integration/
│   ├── e2e/
│   ├── fixtures/
│   └── security/
├── docs/
│   ├── adr/
│   ├── threat-model.md
│   └── development.md
├── wails.json
├── go.mod
└── README.md
```

规则：

- 接口靠近使用它的包定义，避免创建装满所有接口的公共包。
- 只有确实跨三个以上模块且语义稳定的代码才进入共享包。
- 禁止创建含杂项的 `utils`；按语义命名，如 `pathguard`、`redact`。
- 内置 Prompt 和 OpenScience 默认 Skill 作为只读资源打包；用户定制创建独立 User/Project Skill，不修改安装资源。

---

## 17. 技术选型原则

| 类别 | 基线 | 说明 |
|---|---|---|
| Go | 项目启动时的受支持稳定版本 | `go.mod` 和 CI 固定版本 |
| 桌面 | Wails v2 稳定版 | 通过 Facade 隔离，避免业务依赖框架 |
| 前端 | React + TypeScript + Vite | 开启严格类型检查 |
| UI 状态 | Zustand | 仅 UI/缓存状态 |
| 数据库 | SQLite | 优先减少外部服务依赖 |
| SQLite Driver | 在纯 Go 可移植性与扩展能力间做一次 ADR | 选定后用仓储契约测试约束 |
| 全文检索 | SQLite FTS5 | MVP 默认检索能力 |
| 向量检索 | `VectorIndex` Port + SQLite/Go MVP 实现 | 不强依赖 Chroma |
| HTTP | 标准库或轻量封装 | 统一超时、重试、代理和脱敏 |
| 日志 | `slog` 或 zerolog，二选一 | 通过项目 Logger 接口使用 |
| 配置 | 小型强类型配置加载器 | 不把密钥交给 Viper 等普通配置层 |
| PDF | 先做文本质量验证再选库 | 商业库许可证必须审查 |
| Schema | JSON Schema | Tool、Skill 和导入导出统一校验 |

依赖选择流程：

1. 检查许可证、维护状态、平台支持和二进制体积。
2. 写最小 Spike 验证 Windows/macOS/Linux 打包。
3. 记录 ADR，再进入核心代码。
4. 依赖版本由 lockfile、`go.sum` 和 CI 固定，不在架构文档硬编码易过期的小版本。

---

## 18. 测试策略与质量门禁

### 18.1 测试分层

| 层级 | 必测内容 |
|---|---|
| 单元测试 | 状态机、预算、Schema 校验、路径守卫、权限规则、上下文裁剪 |
| 契约测试 | 所有 Model Adapter、Tool、SecretStore、Repository、VectorIndex |
| 集成测试 | SQLite 迁移、MCP stdio/HTTP、流式中断、文档索引、Keychain |
| E2E | 首次启动、配置模型、聊天、工具授权、MCP 调用、引用、导出 |
| 故障测试 | 网络断开、限流、应用崩溃、MCP 退出、磁盘满、数据库锁、取消进程 |
| 安全测试 | 路径穿越、junction/symlink、Schema 注入、压缩炸弹、Prompt 注入、SSRF、XSS/CSP、日志泄密、数据出站策略 |
| 打包测试 | Windows/macOS/Linux 干净环境安装、升级、卸载和数据保留 |

### 18.2 Fake 组件

从第一阶段提供：

- `FakeChatModel`：脚本化输出文本、Tool Call、限流和断流。
- `FakeMCPServer`：覆盖初始化、工具变化、超时和异常退出。
- `FakeSecretStore`：仅测试使用，严禁生产注册。
- `FakeTool`：覆盖成功、拒绝、超时、超大输出和非幂等调用。

测试不得依赖真实付费 API 才能通过。

### 18.3 每阶段统一 Definition of Done

功能只有同时满足以下条件才算完成：

- 接口、状态和错误码已定义。
- 正常路径、失败路径、取消路径均有测试。
- 无 API Key、Token、研究正文泄露到日志或前端。
- 数据迁移和重启恢复已验证。
- UI 有加载、空状态、失败和重试反馈。
- 新权限已进入威胁模型与审批 UI。
- 文档和 ADR 已更新。
- `go test ./...`、前端类型检查、Lint、构建和安全扫描通过。

---

## 19. 重构后的开发阶段

开发顺序是依赖关系，不建议颠倒。周期为单人全职的粗略估算，应根据 Spike 结果调整。

### P0：工程基线与架构护栏（1 周）

**目标：** 建立后续不会反复推倒的工程骨架。

交付物：

- Wails + React + TypeScript 可构建空壳。
- 上述目录和 Composition Root。
- CI：Go 测试、前端类型检查、Lint、桌面构建。
- SQLite 初始化和迁移框架。
- AppData/Cache/Workspace 目录解析。
- 统一错误、日志脱敏、事件 Envelope。
- `FakeChatModel`、测试夹具和 ADR 模板。
- `docs/threat-model.md` 初版。

退出标准：

- 干净环境可启动、迁移数据库并正常退出。
- 第二次启动读取同一数据目录，不产生重复迁移。
- 日志轮转、脱敏和 CorrelationID 测试通过。
- Bootstrap 之外没有具体基础设施的全局单例。

### P1：模型配置与流式聊天闭环（2 周）

> 实施状态（2026-08-13）：P1 收尾完成；进入真实兼容服务联调与退出标准验收。

交互补充：OpenAI-compatible Profile 优先通过 `{base_url}/models` 获取模型列表并支持搜索、多选；若服务未实现该端点，必须允许用户手动添加多个 Model ID，不能阻塞配置。同一 Profile 的模型共用 Base URL 与 Key，聊天时选择具体模型，Run 保存实际 `model_id` 快照。

**目标：** 用户能安全配置自定义模型并完成稳定多轮对话。

交付物：

- Project、Conversation、Message/MessagePart。
- Model Profile CRUD、多模型关联、连接测试和能力显示。
- 默认/外部 Workspace，以及项目与会话的安全移除。
- OS SecretStore，前端仅显示密钥状态。
- 至少一个 OpenAI-compatible Adapter；其他 Provider 后续按契约增加。
- 流式回答、停止生成、Usage、FinishReason。
- Run/RunEvent 最小持久化和 Snapshot 恢复。
- 首次启动向导。

退出标准：

- 自定义 Base URL、Model 和 Key 可用。
- 密钥不出现在 SQLite、配置文件、日志、导出数据和前端 Store。
- 断网、认证失败、限流、流中断和用户取消均有可理解反馈。
- 重启后可恢复历史消息，遗留 Run 正确标为 `interrupted`。

### P2：Agent Loop、内置工具与权限（2～3 周）

> 实施状态（2026-08-13）：P2.1 工具协议与持久化、P2.2 注册/权限/审批后端、P2.3 有界 ToolExecutor 与 Workspace 只读工具、P2.4 Provider Tool Calling 与 AgentLoop，以及 P2.5 Plan / Full Access、审批卡片、ToolCall 时间线、Snapshot 恢复、停止和中断后继续交互均已完成。`GetRunSnapshot` 提供 Run、Messages、ToolCalls 与 pending Approvals 作为恢复事实源；下一阶段进入 P3 MCP。

P2.5 范围：`Plan` / `Full Access` 两档会话权限、审批卡片、ToolCall 时间线、Snapshot 轮询兜底、等待审批/执行中的统一取消、错误与结果的安全摘要，以及丢事件、重启和重复点击场景的 UI/E2E 验收。`Plan` 每个 ToolCall 均由用户 Accept/Reject，`Full Access` 自动放行已注册且通过工程边界校验的工具；风险等级仅提示，不替用户限制授权。拒绝以普通 ToolResult 交还模型，程序不改写、补写或强制模型生成替代回答。P2.5 不新增 MCP 或 Skill 执行能力；它们分别属于 P3、P4。

中断语义：停止会保留当前 Assistant 的部分内容并将 Run 置为 cancelled；运行期间发送新消息采用单执行所有权的 cancel-then-start，先等待旧 goroutine 清理，再创建新 Run，不并行驱动同一会话。切换会话后，前端只接受 `snapshot.run.conversationId` 与当前会话一致的恢复结果，避免迟到轮询污染界面。

**目标：** 打通一条可审计的模型工具调用闭环。

交付物：

- AgentLoop、ContextBuilder 与可取消的 Run 生命周期。
- ToolRegistry、JSON Schema 校验、ToolExecutor。
- Plan / Full Access Policy、Approval；历史 PermissionGrant 仅兼容旧数据。
- 只读工具：列出 Workspace、读取文本、知识搜索占位工具。
- 写文件工具使用原子写和明确确认。
- ToolCall 时间线、取消、超时、结果截断和 Artifact 引用。

退出标准：

- FakeModel 可以发起 Tool Call，工具结果回填后得到最终回答。
- 非法参数、未知工具、越界路径、权限拒绝不会执行工具。
- 超过旧版 Turn/Tool 累计上限后仍能继续执行并生成最终回答。
- 崩溃恢复不会重复执行非幂等 Tool Call。
- Windows 路径穿越、junction 和符号链接测试通过。

### P3：MCP 接入（2 周）

> 实施状态（2026-08-14）：P3 已通过退出验收。核心纵向闭环包括配置/状态 UI、兼容主流 `mcpServers` JSON 的批量导入、stdio 与 Streamable HTTP、initialize 与 Tools/Resources/Prompts 能力发现、MCP Tool 动态注册、统一权限管道、SecretEnv 原生凭据隔离、能力列表变更刷新、stderr 有界脱敏日志，以及异常断开/重启状态恢复。真实 stdio 子进程和 Streamable HTTP E2E 已覆盖发现与调用、稳定命名空间、Resource/Prompt 不注册为 Tool、正常断开注销、异常退出离线与注销；MCP Tool 固定带 `tool.invoke` 权限并进入 AgentLoop 的统一审批链。导入配置默认不受信任且不自动连接；P3 不默认自动启动或无限重连，远程重连由 SDK 进行有界重试。Windows Job Object 进程树托管与统一 NetworkClient 的 DNS rebinding 防护仍列入发布加固，不能将当前实现表述为强沙箱。

**目标：** 用户能添加和可靠使用 MCP Server。

交付物：

- MCP 配置与状态 UI。
- stdio、Streamable HTTP Transport。
- 初始化、能力协商、Tools/Resources/Prompts 发现。
- MCP Tool → ToolRegistry Adapter。
- 进程生命周期、超时、退避重连、stderr 脱敏日志。
- SecretEnv 引用和 Server 信任提示。

退出标准：

- 至少一个测试 stdio Server 和一个测试 HTTP Server 通过 E2E。
- Server 异常退出不会造成僵尸进程、无限重启或 UI 假在线。
- MCP Tool 不能绕过权限系统。
- Tool 同名时使用稳定命名空间且不会错误路由。
- Resource/Prompt 不会未经选择自动注入上下文。

### P3.5：模型协议完整性（插入阶段）

> 实施状态（2026-08-14）：P3.5.1～P3.5.3 的代码与自动化 Provider Fixture 已完成；Responses-compatible 真实接口 E2E 已通过，实测 3 个连续模型轮次中 `reasoning → message → function_call → function_call_output` 可持久化并继续调用 MCP，reasoning token 和折叠证据 UI 正常，工具拒绝后也能完成本轮。该接口返回了 reasoning summary，但 `encrypted_content` 为空字符串，因此非空 encrypted payload 仍由自动化 Fixture 覆盖；真实 Anthropic E2E 仍待配置服务后验收。内部协议新增不对前端暴露的 Provider Turn/Item；Anthropic `thinking_delta`、`signature_delta`、`redacted_thinking`、`text` 与 `tool_use` 按原 content block index 累积；OpenAI Responses 请求显式包含 `reasoning.encrypted_content`，并按 output index 保存 `reasoning`、assistant `message` 与 `function_call` 完成项。两种协议都在 ToolCall 进入审批/执行前不可变持久化，并在 ToolResult/function_call_output 前按原顺序回放。SQLite 已加入 `provider_turn_items` 与运行级推理证据；OpenAI Chat/Responses 的 reasoning token 可独立统计，且不会重复计入总 Token。界面严格区分“参数已接受”和“已观察到思考”，只显示默认折叠的证据状态，不暴露原始 thinking/signature/encrypted content。上下文压缩会先计入 system、工具定义和最新消息，再仅保留最新的完整 Provider Turn 后缀，不拆分推理、工具调用与工具结果协议组。

> 推理摘要展示（2026-08-20）：助手消息在正文前立即显示所选思考强度，并在 Provider 返回明确的 Responses `summary_text` 后原位更新；Responses 请求显式使用 `reasoning.summary: "auto"`，兼容端点明确拒绝时仅撤掉该可选字段并保持原 effort 重试。没有安全摘要时只展示实际档位、观察状态和 reasoning token。展示不增加正常请求次数、不阻塞正文流式输出，摘要作为 Run 的消息只读投影持久化但不属于 MessagePart，因此不会进入后续模型上下文。Anthropic 原始 thinking/signature/redacted_thinking 与加密推理状态继续仅用于协议连续性，不向 UI 公开。

**目标：** 文本、推理状态和工具调用在多协议、暂停审批、程序恢复与上下文裁剪后仍保持服务端要求的原始关系。

交付物：

- Provider 原生 Turn/Item 内部协议和不可变持久化。
- Anthropic thinking/signature/redacted_thinking 严格回放。
- OpenAI Responses reasoning/encrypted item 严格回放。
- 参数接受、实际思考块、签名和 reasoning token 的分级证据。
- Provider-safe compaction 和默认折叠的思考状态 UI。

退出标准：

- Anthropic 的“思考 → 工具 → ToolResult → 再思考”真实 E2E 不丢 block、signature 或顺序。
- Responses 工具轮次不会丢失服务端要求的 reasoning item/encrypted state。
- 审批暂停、拒绝、取消和程序恢复不会重复或修改 Provider Item。
- 原始协议状态不进入聊天 Snapshot、日志和默认前端渲染。
- 上下文压缩不会拆分仍需回放的推理/工具协议组。

### P4：Skill 系统（2 周，历史方案已退役）

> 历史实施记录（2026-08-19，2026-08-24 完成收缩）：P4.1～P4.5 曾实现 `skill.yaml`、全局版本化包、项目版本绑定、文件夹/ZIP 安装、`$skill-id`/suggest 预选、`run_skill_contexts` 和两个 SciAide 内置科研 Skill。该执行路径已被第 10 节及 ADR-0034 的 OpenScience 动态 Skill 机制替代；新 Run 不再安装、选择或注入这些旧包。兼容代码现只保留历史 DTO、严格解码/哈希校验、上下文渲染、只读加载、归档恢复的幂等写入和一次性退休包归档；旧安装、选择、trigger、协调、回滚与 PackageStore 已删除。历史表和迁移不改写。P4.6 的附件、文档读取和上下文加固仍是当前能力。

**当时目标：** Skill 能安全安装、按项目启用并影响 Agent 行为。

历史交付物（不代表当前安装与路由机制）：

- `skill.schema.json`、Manifest 解析和兼容性检查。
- 安装、卸载、启用、禁用、版本回滚。
- `SKILL.md` 加载、上下文预算和内容哈希记录。
- Tool/MCP 依赖解析和不可用状态。
- 压缩包安全校验；脚本仅注册 Tool，不自动运行。
- 两个旧 SciAide 内置科研 Skill：文献阅读、学术写作辅助。

退出标准：

- 恶意路径、超大压缩包、缺失依赖和版本冲突均被阻止或清晰提示。
- Run 可追溯到具体 Skill 版本和哈希。
- 禁用 Skill 后不会残留指令或注册工具。
- Skill 不能扩大用户未授予的权限。

### P4.6：项目附件与本地文档读取基线

已实现文件选择/拖放、项目本地 SHA256 去重、消息附件恢复、PDF 分页、DOCX 段落/表格、XLSX Sheet/行/公式以及 UTF-8 文本解析。模型通过统一的附件列表、检查、读取和搜索工具获取有界内容；解析缓存删除后从项目附件原件重建。扫描件 OCR 不内置到当前客户端；跨文档 FTS/Embedding 和引用持久化由 P5 完成。

### P5：科研知识库与可信引用（3 周）

> P5.1 实施状态（2026-08-19）：已建立项目作用域的 Document、ImportJob 和 IndexVersion 元数据、可恢复队列、既有附件补建、缓存重建及 `builtin.knowledge.search` 跨文献基线。
>
> P5.2 实施状态（2026-08-19）：已实现 `bounded-unit-v2`、中英文确定性词项、项目本地 contentless FTS5/BM25、标题/短语加权、跨文档结果配额和 8,000 rune 模型输出预算。迁移 25 保留 P5.1 数据，v2 以独立索引影子构建，全部显式 Knowledge Document 完成并校验后才原子替换 v1；Structured 不再重复 snippet。聊天附件与知识库现已拆分，顶部知识库窗口支持显式导入、状态查看和移出索引。当前仍不包含 OCR、Embedding/向量检索和正式 Citation 持久化。
>
> P5.3 实施状态（2026-08-19）：已增加默认关闭的 OpenAI 兼容 `/v1/embeddings` 配置；启用时先验证实际维度，再以独立 IndexVersion 影子构建项目本地 float32 向量。查询使用 BM25 + 余弦相似度 RRF，支持 Document ID/格式过滤、跨结果重叠去重和既有上下文预算；Embedding 查询失败自动回退 BM25。相同查询向量按项目和 IndexVersion 缓存到 `index-vN.db`，只保存查询 SHA256，并以 512 条 LRU 控制空间。当前不内置本地模型运行时，仍不包含 OCR 和正式 Citation 持久化。
>
> P5.4 实施状态（2026-08-19）：`builtin.knowledge.search@3` 为检索片段生成绑定 Run、IndexVersion、Chunk 与原文 SHA256 的稳定 `[K-...]` 标记。同一 Chunk 的不同检索片段使用不同标记，回答完成时只接受当前 Run 成功知识工具结果中存在且证据快照有效的实际使用标记。Assistant Message 正文、不可变 Citation 快照与 Run 完成状态在同一事务提交。前端将已验证引用显示为可点击编号，可查看来源、页码/段落/Sheet 定位、原文和哈希；伪造、变形、跨 Run 或冲突标记不会显示为可信引用。历史引用不依赖可重建索引继续存在。当前仍不包含 OCR、原生 PDF 页跳转和固定语料召回评测。
>
> P5.5 实施状态（2026-08-19）：文档解析缓存升级为 schema v2。PDF 在稳定页码边界内保守合并碎片行、恢复英文断词、识别章节，并跨页清除重复页眉页脚及页码；分析文本受 8.25M rune 上限约束。DOCX 解析真实段落样式、大纲标题、章节路径、列表、表格行和核心 OpenXML 元数据，DOCX/XLSX 共用元数据读取。旧缓存从附件 SHA256 原件懒重建，知识库按 ParserSchemaVersion 构建影子索引后切换。同页多章节对模型保留标题上下文，但 Citation locator 去重。当前决策是不在 SciAide 内置 OCR 运行时。
>
> P5.6 实施状态（2026-08-20）：知识库窗口已展示导入任务的等待、读取、分块/向量化、提交、完成、失败和取消阶段，并根据解析元数据给出 PDF 文本页覆盖率、空页/截断提示、DOCX 标题/表格和 XLSX Sheet 诊断。用户可取消排队或运行中的任务、显式重试失败/取消任务、重建单篇文档；取消不会被普通搜索自动撤销，应用关闭导致的中断仍可恢复。已有 ready 索引时，重建不会阻塞检索，单文档内容在本地事务提交后整体替换。固定中英文科研语料为 BM25 和确定性 Embedding + RRF 建立 Hit Rate、Mean Recall、MRR 与来源定位率回归门槛。解析诊断不证明内容语义正确，扫描件仍只提示当前未内置 OCR。

**目标：** 基于项目文献回答并定位引用来源。

交付物：

- 在 P4.6 附件与解析基线上增加异步导入队列、结构解析质量检查和失败重试；OCR 仅保留未来可选外部插件接口，不作为内置依赖。
- 文本分块、FTS5、Embedding 和索引版本。
- 混合检索、元数据过滤、上下文裁剪。
- Citation 结构化保存和原文定位。
- 索引重建、取消、失败重试和进度 UI。

退出标准：

- 引用可定位到文档页码/章节和原文片段。
- 更换 Embedding 模型不会混用不同维度的向量。
- 导入中断不会产生可检索的半成品索引。
- Prompt 注入型文档不能绕过 Tool 权限或系统规则。
- 在固定测试语料上建立检索召回基线，回归测试通过。

### P6：科研发现、证据与产物闭环

> P6.0 实施状态（2026-08-20，Skill 入口于 2026-08-24 更新）：聊天输入框新增统一斜杠命令面板，输入 `/` 后支持过滤、鼠标、上下方向键、Tab、Enter 和 Esc。首批本地命令为 `/mcp`、`/skill`、`/knowledge`、`/compact`、`/model`、`/usage`、`/new` 与 `/help`；只有整段文本精确匹配已注册命令时才本地执行，未知命令或带参数文本继续作为普通消息。参考 Codex 运行时 inventory 与 Claude Code `/mcp` 交互，`/mcp` 进入 Server 二级列表，展示真实启动/关闭状态及 Tools/Resources/Prompts，并可直接启动或关闭；`/skill` 展示当前动态目录并插入 `Use the <name> skill:` 显式加载提示，`/model` 直接选择模型，`/knowledge` 和 `/usage` 展示运行时摘要，配置窗口仅作为显式管理入口。`/compact` 不创建聊天 Message，而是复用无工具科研 checkpoint 协议，校验旧摘要哈希、固定最新终态 Run 的消息边界、最多渐进执行三轮并记录模型用量；运行中会话拒绝手动压缩。命令面板和所有二级层级支持点击外部关闭；主界面与管理窗口统一中性 Apple-like 材质和 compositor-only 动效，移除全屏实时模糊以降低 WebView2 重绘开销。
>
> P6.0 流式性能加固（2026-08-20）：`content.delta` 不再逐分片触发全页更新，而是按浏览器动画帧合并；终态正文仍以 `content.completed` 和 SQLite Snapshot 为准，避免缓冲导致丢字或重复。历史消息保持稳定引用并使用 memo 隔离，流式自动跟随使用即时滚动，用户离开底部后不再强制拉回。Windows 发布必须通过 `build-release.ps1` 生成并验证 amd64 PE，禁止把当前机器默认的 386 目标误作为发布版本。
>
> P6.1 实施状态（2026-08-23）：建立项目级 Artifact、不可变 ArtifactVersion、SHA256 Blob、Lineage 和 Citation snapshot。SQLite 是元数据唯一事实源，对象按内容地址保存在 `<Workspace>/.sciaide/artifacts/objects`；保存采用同卷暂存、对象发布和 SQLite 事务，启动清理中断暂存/无引用对象并幂等恢复 Tool 产物。首批入口为完整助手回答保存、Workspace 文件登记，以及 ToolResult 显式 `workspacePath` 自动登记；普通附件引用不会误变为产物。产物可预览、查看历史/来源/引用、校验、下载、重命名和回收恢复；删除 Conversation 不删除产物及来源快照。正式 DOCX/PDF 导出、复杂格式预览和项目备份留给 P6.2 之后。
>
> P6.2 实施状态（2026-08-23）：在不可变 ArtifactVersion 之上增加独立 ArtifactExport；DOCX/PDF 生成不会覆盖原件或推进版本，相同源、格式、引用样式和生成器版本幂等复用，并以源/导出 SHA256 和重新打开校验阻止损坏或非确定性输出。Markdown 使用 Goldmark GFM AST 转换，PDF/DOCX/XLSX/CSV 复用结构化解析器；产物窗口显示有界结构预览、GB/T 7714-2015/APA 7 选择和历史导出下载。无可读正文或解析截断结果不会发布正式导出；宽表按固定列数分段，DOCX 重复表头，PDF 对超高单元格分页且保留完整文本。可信 Citation snapshot 是唯一正式引用来源，缺失书目字段明确披露。
>
> P6.3～P6 收尾方向（2026-08-24 修订）：P6 后半段不以项目备份为唯一主线。参考 OpenScience 的公开科研数据库 Connector、固定工具面、按需 Skill 和受控复核机制，先完成在线文献发现到本地可信证据的闭环；项目备份与恢复作为 P6.6 收尾可靠性能力。不得把在线搜索命中直接标记为本地可信引用，也不得提前以完整 Workflow/DAG 替代现有 Agent Loop。
>
> P6.3～P6.4 实施状态（2026-08-24）：已接入 OpenAlex、Crossref、arXiv、PubMed、Europe PMC 和 Semantic Scholar 六个公开 Connector，并通过固定 `builtin.research.catalog/search/fetch` 工具进入既有 Registry、Policy、Executor 与审计链。共享网络层统一执行固定 Host、取消、30 秒超时、限速、`Retry-After`、有界重试、8 MiB 响应上限、缓存和错误分类。项目级“文献发现”已支持查询持久化、多源部分失败、保守去重、来源快照、纳入/排除/笔记，以及显式纳入后下载校验过的开放 PDF或生成明确披露“元数据/摘要而非全文”的 Markdown；随后复用 Attachment 和 Knowledge 队列。端到端测试覆盖“候选纳入 → 附件 → 索引 → 本地检索”及重复导入幂等，在线候选不会直接取得可信 Citation 身份。
>
> P6.5 实施状态（2026-08-24）：已为每个候选建立规范书目、多来源字段快照、冲突选择和逐字段用户修订历史；规范 DOI/PMID/PMCID/arXiv/OpenAlex ID 不从模型文本猜测。证据矩阵覆盖研究问题、方法、样本/数据集、主要结论、局限和笔记，研究事实必须绑定当前书目的本地 Chunk。模型证据只能以待复核状态创建，核验/拒绝只改变审核状态，正文、provenance、证据等级、Quote、SHA256 与定位快照由迁移 `000049` 的触发器保护。Message/Artifact Citation 固化规范书目和证据等级，完整已知字段可按 GB/T 7714-2015 与 APA 7 渲染。
>
> P6.6 实施状态（2026-08-24，Skill 部分随后重构）：项目可导出为版本化 `.sciaide-project` 无密钥归档，包含一致性项目数据库及附件、知识索引、Artifact 和研究对象，明确排除凭据、MCP 配置、权限和 Skill 包。恢复在隔离目录校验 ZIP/Manifest/SHA256/SQLite/外键后重映射 ID，默认原子发布为新托管项目；启动能回收中断暂存和未提交 Workspace。端到端测试覆盖干净数据根“归档 → 恢复 → 再归档”关系/哈希等价以及篡改、缺失 Blob、`ENOSPC`、合并失败、中文/长路径和目标竞态。同期完成的 schema-2 依赖/冲突预协调器现只用于旧 Run 快照兼容；新 Run 已改用 ADR-0034 的模型按需加载机制。

**目标：** 从公开科研数据库发现资料，经用户可审计的筛选和本地知识化形成可信证据，再生成可管理、可复现、可交付和可恢复的科研产物。

#### P6.1～P6.2：可信产物与确定性导出（已完成）

- Artifact 模型、版本和来源关系。
- Markdown、DOCX 等至少两种稳定导出格式。
- 表格、图片、代码和数据文件预览。
- 引用样式渲染与结构化引用分离。

#### P6.3：公共科研数据库 Connector（已完成）

- 建立独立于具体数据源的 `Catalog / Search / Fetch` Connector 契约；每个 Connector 只负责一个来源的请求和规范化。
- 首批接入 OpenAlex、Crossref、arXiv、PubMed、Europe PMC 和 Semantic Scholar 的公开接口；没有 API Key 时也保留可用的公开限额路径，不宣称与文献商存在商业合作。
- 模型只看到固定数量的 `builtin.research.catalog`、`builtin.research.search` 和 `builtin.research.fetch` 工具，Connector 增长不增加 Tool Schema 数量。
- 统一执行域名 allowlist、调用方取消、外层超时、按 Host 限速、`Retry-After`/429/5xx 有界重试、响应大小限制、短期缓存和来源错误分类。
- 搜索结果统一为来源 ID、标题、摘要、作者、年份、载体、DOI/PMID/arXiv ID、落地页、开放全文链接和来源原始字段的有界快照；所有外部字段仍是不可信研究数据。

P6.3 退出标准：

- 固定 Fixture 覆盖六个 Connector 的解析、取消、超时、限速、错误降级和响应上限；任何来源失败不伪装成“零结果”。
- Tool 调用仍经过 Registry、PolicyEngine、ToolExecutor 和审计持久化；模型不能提供任意 URL 让 Connector 越过固定来源域名。
- 同一检索可从缓存复用，但缓存内容不取得可信引用身份，且可按来源和规范版本失效。

#### P6.4：文献发现、去重与知识库导入（已完成）

- 提供项目级“文献发现”窗口，支持单源/多源搜索、分页、筛选、排序、来源状态和明确的部分失败提示。
- 用 DOI、PMID、arXiv ID、OpenAlex ID 与规范化标题/年份建立保守去重；保留每个来源记录，不因聚合静默丢弃冲突字段。
- 将候选文献、来源快照、查询、纳入/排除/待定状态、排除原因和用户笔记持久化；相同来源记录和相同查询幂等复用。
- 用户可将候选显式加入项目知识库：优先下载经过 MIME、大小和 SHA256 校验的开放全文；没有可用全文时生成带来源披露的元数据/摘要 Markdown 附件，不伪装成全文。
- 在线下载先进入项目同卷随机暂存，只有 Connector 返回的受信任固定来源 URL 能进入下载器；成功后复用 Attachment 和 Knowledge 队列，失败不留下半成品 Document。

P6.4 退出标准：

- “搜索 → 去重 → 纳入/排除 → 导入 → 索引 → 本地检索”可在重启后继续，重复点击不会复制候选、附件或索引任务。
- URL 重定向、MIME 欺骗、超大响应、无文本扫描件和来源离线均有明确失败状态，不污染已有知识索引。
- 在线候选只有导入并经现有 Chunk/Run/Quote SHA256 链验证后，才能成为回答中的可信 `[K-...]` 引用。

#### P6.5：书目元数据、证据矩阵与引用增强（已完成）

- 建立项目级规范书目记录与多来源字段快照，保存作者顺序、年份、题名、期刊/会议、卷期页、出版社、DOI、PMID、arXiv ID、URL 和数据来源。
- 明确字段来源和冲突，不根据模型文本猜测缺失书目信息；允许用户选择来源或显式修订，并保留修订历史。
- 将书目记录绑定到候选、Attachment、Knowledge Document、可信 Citation 和 Artifact Citation snapshot，形成“查询 → 来源记录 → 本地原件 → Chunk → Run → Message → Artifact”的可追溯链。
- 增加综述用证据矩阵：研究问题、方法、样本/数据集、主要结论、局限、用户笔记和证据定位；字段可以为空，模型生成内容必须标记来源和待复核状态。
- GB/T 7714-2015 与 APA 7 从不可变书目快照渲染完整字段；历史 ArtifactExport 不重写，增强后的引用只作用于新 ArtifactVersion/新生成器版本。

P6.5 退出标准：

- DOI/PMID/arXiv 等标识符规范化、跨来源合并、冲突保留和引用渲染有固定测试语料。
- 删除在线候选、移出知识库或删除 Conversation 不会破坏既有 Artifact 的书目和证据快照。
- 证据矩阵中的结论能回到本地原文定位；只有元数据/摘要的候选明确标识证据等级，不能显示为全文结论。

#### P6.6：项目归档与动态 Skill 迁移边界（已完成）

- 继续使用单一用户可见 Research Agent；Skill 只提供任务指导，不拥有独立权限，也不自动执行脚本。
- 新 Run 只接收有界目录元数据和最多 16 个重排候选；模型在任务需要时调用 `builtin.skill.load`，不再由启动前协调器按 `$skill-id`、依赖闭包或冲突组预选正文。
- Skill 第一次成功加载时将来源、内容/包 SHA256、完整指令正文和 ToolCall 固化到 `run_dynamic_skills`；同一 Run 后续加载必须复用一致快照。旧 Run 只恢复原 `run_skill_contexts/run_skills`，不会混入动态目录。
- 可增加只读 Explore/Review 复核步骤，但不得形成递归 Agent、未授权并发或完整 DAG；确定性 Workflow 与 Python/R 运行时仍属于 P7。
- 实现版本化 `.sciaide-project` 备份与安全恢复：一致性 SQLite 快照、项目附件、知识索引、Artifact 原件/导出、研究候选和书目 Manifest；排除 API Key、MCP Secret、视觉渠道密钥和可重建临时文件。
- 导入在临时目录和临时数据库完成版本、路径穿越、压缩炸弹、大小、SHA256、外键和关系预检，默认创建新项目并在全部校验通过后原子发布；恢复后密钥由用户重新绑定。

P6 总退出标准：

- 产物能追溯到 Run、模型、Skill、ToolCall 和 Sources。
- 导出失败不覆盖用户已有文件。
- 公开数据库搜索到本地可信引用及正式 Artifact 导出的端到端流程可复现，来源离线时明确降级而不伪造结果。
- 多 Skill 不会因全部正文注入挤占研究证据，冲突/预算/依赖决定均可审计。
- 项目备份不包含 API Key，恢复后引用关系完整。
- 在干净数据根执行“备份 → 恢复 → 再备份”关系等价测试；中断、磁盘不足、篡改、缺失 Blob、中文路径和长文件名均不产生半恢复项目。

> P6 退出结论（2026-08-24）：以上标准均有当前实现和自动化证据。`internal/bootstrap/research_integration_test.go` 覆盖公共候选到本地 Citation、Artifact、PDF、归档、全新数据根恢复和再次归档；`internal/opensciskill` 覆盖中英文多轮召回、结构化正文、资源清单、能力诊断和动态 Run 快照，`internal/app/skill/legacy_context_test.go` 覆盖旧快照只读兼容与篡改拒绝；`internal/app/projectarchive` 与 `internal/storage/sqlite/project_archive_repository_test.go` 覆盖归档攻击面、失败原子性、凭据排除和关系重映射。P6 到此结束，后续复杂流程不得继续塞入 P6。

### P7：本地执行、数据分析与 Workflow（4 周以上）

**目标：** 先补齐可审计的本地执行能力，使科研 Skill 能真正完成计算和文件产出；再建立确定性、可恢复的复杂科研流程。

前置条件：P2～P6 均稳定。普通对话继续使用 Agent Loop；本地执行、Skill 和 Workflow 都只能调用注册 Tool，不能自行扩大权限。Codex 与 OpenScience 源码仅用于核对进程、权限、取消、输出和运行时机制，生产实现必须落在 SciAide 自有的 `ToolRegistry → PolicyEngine → Approval → ToolExecutor → Artifact` 管道中。

#### P7.1：本地进程执行基础

- 建立可扩展的平台进程适配与版本化执行契约，当前正式交付 Windows x64，首批注册 `builtin.shell.execute`、`builtin.python.execute`；Shell 接收显式命令文本，Python 优先以解释器加参数数组执行内联代码或 Workspace 脚本，不把 Python 参数拼接为 Shell 字符串。
- 默认工作目录限定为当前项目 Workspace；使用现有路径防护验证工作目录、脚本和产物，拒绝符号链接、junction/reparse point 逃逸。P7.1 不开放任意外部工作目录，不自动覆盖或删除文件。
- 每次调用声明 `process.execute`，Shell 同时声明写权限并在审批卡完整展示命令、解释器、工作目录和时间限制。`Full Access` 只跳过审批，不跳过 Registry、Schema、Workspace、输出和进程边界。
- 子进程只继承最小无密钥环境；不得继承模型 API Key、MCP Secret、视觉渠道密钥、Credential Manager 内容或用户 Shell Profile。Python 解释器必须通过受信任探测取得，不接受模型提供任意解释器路径。
- 支持墙钟超时、用户停止、应用退出清理和整个进程树终止；Windows 使用 Job Object 的 kill-on-close 语义，其他平台使用独立进程组。启动失败、退出非零、超时、取消和结果持久化失败必须具有不同状态。
- stdout/stderr 分流、有界捕获和 UTF-8 安全截断；完整命令、脚本 SHA256、解释器版本、工作目录、开始/结束时间、退出码、终止原因及输出哈希进入结构化审计。模型只接收有界结果，不接收无限日志。
- P7.1 是“经授权的本机进程执行器”，不是强安全沙箱。网络、CPU/内存强隔离、持久 Kernel、包安装和外部路径授权不在本小节承诺范围内。

P7.1 退出标准：真实 PowerShell/CMD、Python 与子进程树 Fixture 覆盖成功、非零退出、缺少解释器、中文 Workspace、命令校验、环境密钥排除、输出超限、超时、用户取消、应用退出和路径逃逸；审批拒绝时不启动进程，停止后无遗留子进程，成功生成的声明产物进入 Artifact，正式 Windows x64 EXE 可隔离启动。

> P7.1 完成结论（2026-08-24）：Windows x64 正式目标已经注册 `builtin.shell.execute` 与 `builtin.python.execute`，并接入现有 Registry、Schema、Policy、Approval、ToolExecutor、Artifact 和应用关闭生命周期。实现借鉴 Codex 的核心环境 allowlist、敏感变量排除、输出持续排空与独立 drain timeout、终止原因分类，以及 Windows“挂起启动 → Job Object 纳管 → 恢复”和 `KILL_ON_JOB_CLOSE`；没有移植 Codex 沙箱，也不宣称 Workspace 是文件系统强隔离。真实测试覆盖 PowerShell/CMD/Python、中文路径与输出、1 MiB 输出排空、截断、非零退出、超时、取消、应用关闭、后台子进程清理、密钥环境排除、私有目录/路径逃逸、声明产物和旧文件冒充；迁移 `000053` 保存解释器、脚本/命令、输出和终止审计，项目归档会重映射该审计。P7.1 到此结束，持久 Kernel、依赖环境、内存治理与分析结果结构化属于 P7.2。

#### P7.2：项目级 Python 科研环境

- 在一次性执行稳定后建立项目级 Python 环境，探测解释器版本并生成环境指纹；依赖安装必须由用户明确批准，记录锁定版本和 `pip freeze`，Skill 不得静默安装包。
- 增加项目/会话级持久 Python Kernel、串行执行队列、重启/停止和空闲回收；结构化返回 stdout、stderr、异常、JSON、表格和 Matplotlib 图片，变量与导入只在明确的 Kernel 生命周期内持续。
- 固定输入文件清单和独立输出目录；CSV/XLSX 清洗、描述统计、绘图和可复现脚本作为首批能力。输入原件只读，脚本、环境、数据和输出哈希进入 Provenance 与 Artifact。
- 增加墙钟、输出、进程树和可用内存的限制与诊断；Shell、一次性 Python 与项目 Kernel 默认允许联网，不增加逐域名授权、白名单或额外弹窗。已知目标仍进入 `network.domain`/执行审计，网络响应继续作为不可信数据，密钥不会因默认联网而自动下传。

P7.2 退出标准：同一固定数据集可在干净项目环境复现结果哈希；Kernel 重启后状态清空，取消/超时不污染下一次执行；缺包、越界路径、内存/输出超限均失败关闭且不留下半产物；联网不要求额外用户配置，网络失败按普通可诊断执行错误处理。

> P7.2 完成结论（2026-08-26 更新）：已实现 Python 3 探测/选择；新建项目环境固定在 `<Workspace>/.sciaide/python/venv`，同时允许直接绑定已有 venv。外部环境由用户持有，SciAide 只登记、验证和运行，不删除、不重建且不安装依赖；旧版 `%USERPROFILE%/.sciaide/data/python-envs/<project-id>` 继续兼容但不再用于新建。取消解释器选择保持原状态。锁定包与 `pip freeze`、环境指纹、暂存重建和启动恢复均已实现；`builtin.python.environment.install` 仅修改 SciAide 托管环境并保持独立审批。项目 Kernel 串行保持状态，返回 stdout/stderr、JSON、表格、异常和 Matplotlib PNG，默认使用 1 GiB Windows Job Object 进程树预算并在 15 分钟空闲后回收。输入只读复核、输出限定 `analysis-output/`、失败回滚和成功/异常复现哈希均已落地；总复现哈希使用按声明顺序排列的内容摘要，不因项目或 Run 文件名变化而漂移，完整路径映射继续保存在 provenance。内置 XLSX 模板用 Python 标准库完成首张 Sheet 清洗、描述统计、SVG 和可重放脚本，双干净项目 Fixture 验证四类 Artifact 内容哈希一致且输入原件不变。Shell、一次性 Python 与 Kernel 获准后默认联网且不注入应用密钥。

#### P7.3：默认 Skill 原生化与能力重审

- 为默认 Skill 建立一次性迁移清单，把 `bash/python`、上游 CLI、会话命令、用量上报和云端同步逐项映射到 SciAide Tool、Run、Context、Artifact 或本地审计能力；先证明宿主能力存在，再修改 Skill 正文。
- 在保持科研方法和安全约束的前提下直接改写默认 Skill 源码，使其调用 `builtin.shell.execute`、`builtin.python.execute` 和 SciAide 原生工具；删除运行时 OpenScience 命令、Dashboard、目录和云同步依赖，不保留长期字符串替换或兼容 Prompt。
- Git 安装/卸载、`status/compact/context/stop/sources` 映射到已有 SciAide 服务；`checkpoint/resume/handoff/goal/plan/review/verify/reproduce` 随对应宿主能力落地。无法替代的第三方云后端只保留为明确的可选外部服务，不伪造本地等价能力。
- 能力矩阵由 Skill 声明需求和 ToolRegistry 实际能力共同计算，不再用上游品牌字符串决定整个 Skill 是否可用。修改后的内置树生成新的逐文件 Manifest；上游许可证、NOTICE 和迁移来源保留在发布合规记录中，不作为产品运行概念展示。

P7.3 退出标准：311 个默认 Skill 重新审计且无未解释的上游运行命令；每个“可执行”结论都有真实 Tool/Fixture，“部分可用”明确列出缺失依赖；路由评测、源码浏览、Manifest 和历史 Run 快照继续通过。

> P7.3 完成结论（2026-08-25）：默认树转换和能力审计升级为 `p7.3-v2`。238 个执行型 Skill 均增加“物化资源 → 检查 → 正式 Python/Shell Tool → 审批”的 SciAide 执行边界；上游自动凭据、自动计费和虚假宿主 API 已移除，未知 `generate_image` 等能力明确降级为用户配置的 MCP/API。311 项当前分类为原生 4、需本地依赖 200、需外部服务 87、不可用 20、未审计 0；1,624 文件 Manifest、许可证、NOTICE、provenance、路由和历史快照继续校验。

#### P7.4：Workflow Schema、编辑与静态校验

- 定义版本化 Workflow、Node、Edge、输入/输出和权限需求 Schema；先支持内置 Tool、MCP Tool、Shell/Python、人工确认和只读引用选择节点。
- 实现 DAG 循环、缺失依赖、类型不兼容、悬空输出和权限需求校验；UI 在执行前展示确定性顺序、数据流、命令摘要和风险。
- Workflow 模板和外部导入一律是不可信数据，不能携带密钥、隐式启用 Skill/MCP、动态改变解释器或绕过 ToolRegistry。

P7.4 退出标准：固定 Fixture 覆盖合法图、循环、类型错误、未知 Tool、版本升级、路径逃逸和恶意超大图；未通过静态校验的 Workflow 不能创建 Run。

> P7.4 完成结论（2026-08-25）：已建立 schema 1 的 Workflow/Node/Edge/Port、不可变版本和编译快照，支持 Tool/MCP、Shell、项目 Python、人工确认、候选与 Citation 选择节点。Port 以显式 `fileKind`、`control`、`minItems/maxItems` 描述文件和向导契约，不按字段名猜测自定义方案；默认值同样执行契约校验。Studio 可由模板或 JSON 编辑、静态校验、预览顺序/数据流/权限并保存版本；编译门禁已覆盖循环、类型、未知或变化 Tool、路径逃逸、嵌套密钥和超大图。

#### P7.5：可检查点恢复的 Workflow Runtime

- 建立 WorkflowRun/Step 状态机、输入快照、幂等键和逐步事件；每个执行节点继续复用 ToolExecutor、PolicyEngine、Approval、LocalExecutionService 和项目边界。
- 先实现串行执行，再只对声明为只读、无共享写集合且预算允许的节点并行；副作用步骤重试前必须证明幂等或重新确认。
- 检查点在事务提交后推进，重启只恢复安全步骤，不自动重放未知状态的副作用；人工确认节点可暂停、取消和继续。

P7.5 退出标准：应用重启可从最后提交步骤恢复；取消、超时、崩溃和失败重试不会重复已成功副作用，进程执行及 Workflow 事件可以从 SQLite 重建。

> P7.5 完成结论（2026-08-25）：当前 Runtime 采用确定性串行执行，Run/Step、输入输出、ToolCall、审批和事件均持久化；支持暂停、继续、取消、人工决策和逐步审批。文件输入在 Run 创建前复制成完整 SHA256 内容寻址快照，源文件保持不变，工具执行前再次验证摘要与字节；旧内置分析方案用精确定义哈希只读兼容，旧未冻结 Run 失败关闭并提示重新发起。重启不重放已提交步骤，只恢复未提交幂等步骤；非幂等活动步骤进入 `outcome_unknown`，用户确认潜在副作用后才可重试，并发审批/决策只有一个条件提交成功。成功 ToolResult 在落库前冻结声明产物的实际字节和 SHA256，后续 Artifact 登记与恢复不再采用可变 Workspace 文件；并行节点没有提前开放。

#### P7.6：参考科研 Workflow 与阶段验收

- 交付“公共数据库检索 → 候选筛选 → 本地证据 → Python 数据分析 → 图表与结果文件 → 带引用报告 → 正式导出”的参考 Workflow，并复用 P6 的书目、证据等级、Citation 与 Artifact。
- 提供执行预览、步骤时间线、失败定位、从检查点继续、环境 Manifest 和产物图谱；不以递归多 Agent 替代确定性步骤。

> P7.6 完成结论（2026-08-25）：内置参考模板已实现“公共数据库检索 → 人工候选筛选 → 材料导入 → 知识索引 → 本地证据检索 → 人工 Citation 选择 → 项目 Python 分析 → CSV/SVG Artifact → Markdown 报告 → DOCX/PDF 导出”。科研模式同时提供“数据探索与可复现分析”连续向导：选择 CSV/TSV、填写目标/方法、Python 前检、授权、进度和五类 Artifact；XLSX 模板继续提供四类可重放产物。两类数据模板均限制输入规模并转义公式型单元格。确定性集成测试覆盖两次应用重启、Citation 冻结、环境/Kernel、Artifact 和完整来源链；Kernel 普通输出与图片由私有 staging 整批无覆盖发布，同项目环境变更互斥，外部抢占不会被覆盖或误删。Run 详情从已提交 Step Output 展示冻结环境 Manifest、Kernel 复现摘要，以及 Citation、分析文件、Markdown 和 DOCX/PDF 的产物图谱，未完成步骤不伪造证据。项目归档同时携带已登记 Artifact 和成功 ToolResult 中暂待登记的冻结内容对象，恢复后可按原 SHA256 补登记。普通聊天仍走 Agent Loop，Workflow 只在用户显式打开和运行时生效。

P7 总退出标准：Shell/Python 执行可停止、可审计且不泄露应用密钥；默认 Skill 不依赖上游产品运行命令；Workflow 可验证、可暂停、可恢复且不扩大权限；数据分析结果可由版本化环境和输入哈希复现；所有正式输出进入统一 Artifact，应用重启和失败重试不破坏 P6 的不可变来源链。

> P7 退出结论（2026-08-25）：P7.1～P7.6 全部达到上述退出标准。全量 `go test ./...`、`go vet ./...`、前端 typecheck/test/build、`p0-check.ps1` 和 `sync-openscience-skills.ps1 -Check` 通过；正式 `windows/amd64` EXE 在隔离 `SCIAIDE_HOME` 下应用 60 条迁移，SQLite 完整性和外键检查通过，可见主界面非空，`WM_CLOSE` 正常退出且无残留进程。普通聊天仍使用 Agent Loop，Workflow 保持显式可选；联网 Tool、Shell、一次性 Python 和项目 Kernel 获准后继续默认联网，只有依赖安装另行审批且应用密钥不下传。P7 到此结束，后续发布工程进入 P8。

> P7 后续科研模式协作增强（2026-08-27）：新 Workflow Run 与一对一科研 Conversation 在同一事务创建，旧 Run 保持只读兼容；科研会话复用普通聊天的 Agent Loop、流式消息、Reasoning 摘要、工具时间线、审批、停止和上下文压缩。Workflow 显式支持 `ai_analysis` 与 `agent_stage` 节点，冻结模型、Prompt、Schema、工具白名单、输入输出哈希、Token 和 attempt；内置 Agent Stage 默认 `reviewPolicy:auto`，结构化结果通过 Schema 后自动推进，旧节点缺少字段时仍进入人工复核。阶段可用 `skillRouting` 动态召回并冻结只读 Skill 正文/资源快照，但不能由 Skill 增加工具权限。Agent Stage 只能冻结幂等且仅需 Workspace 读取或联网权限的观察工具，写入、进程、依赖安装、密钥、外部路径和非幂等行为必须建成显式 Workflow 节点；编译器与 Runtime 均失败关闭。每个模型回合注入有总预算的 Workflow system 上下文，ToolCall 落地前再次刷新阶段并校验。非终态 Workflow 对科研 Conversation 保持单一执行所有权：普通 Chat Run 和 `Steer` 均不能在确定性步骤、AI 自动阶段或结构化检查点间抢占会话；仅旧方案明确等待 Agent Stage 人工复核时开放补充对话，任务终态后恢复自由追问。后续若实现执行中的旁路提问，必须使用独立持久队列并与当前 Step 输出隔离。科研权限与 Run 冻结值一致，终态任务、Workflow、项目删除和项目归档覆盖完整 AI/Chat/Conversation 图；活动执行阻止删除。运行态 UI 为左路线、中 AI、右证据，普通 Conversation 不受阶段约束。科研模式以流程和 AI 自主协作为主，对话用于解释、纠错和少量高影响决策，不能暗中提交或绕过 Workflow Step。

> 科研模式对象与入口约束（2026-08-27）：Workflow 是可复用、可版本化的研究方案，Workflow Run 是某次独立科研任务。左侧只保留“科研任务 / 研究方案”两个一级标签；前者跨方案展示已启动 Run 并直接恢复科研会话和三栏工作台，后者再明确拆成只读起点“方案模板”和项目持久定义“我的方案”。方案只负责预览、配置、版本管理与新任务发起，不得重新引入“方案 → 任务进度 → 历史任务”的嵌套导航。

> 研究方案身份约束（2026-08-27）：同项目内的“我的方案”按规范化完整 Workflow 定义 SHA256 去重，不按显示名称或模板 ID 去重。完全相同的定义重复保存必须复用已有方案；任一用户可见定义内容发生变化时必须允许创建独立方案，跨项目定义互不合并。历史数据库中的同内容重复项在列表折叠为一项，后端删除必须在单事务中对等价方案整组执行活动 Run/AI 门禁和清理；Workflow Run 不参与折叠，每次启动始终产生独立科研任务。

> 科研模式入口规则（2026-08-27）：进入项目科研模式时先完成项目级 Run 查询；零任务直接进入“研究方案”，存在任务进入“科研任务”。查询期间使用中性准备状态，不能先渲染错误的空任务页；用户手动切换优先于异步默认值，并在当前应用会话内按项目记忆，但不存在任务时不得恢复到无内容的任务入口。

> 科研任务工作台职责约束（2026-08-27）：左栏是唯一研究路线和当前位置来源，中栏是唯一用户决策入口，右栏只承载核验、成果和折叠技术审计，不得再次复制完整阶段记录。候选文献、Citation 选择、Workflow 授权、人工确认和失败重试必须以状态机直接控制的交互卡片进入科研会话流，不能复制一份 UI 状态到右栏。成果来源链按“文献来源与可信证据 → 分析与中间产物 → 研究成果 → 发布版本”展示；发现阶段选中的候选文献只能标为来源，只有本地知识检索产生并经人工选择的不可变 Citation 快照才能连接到最终报告作为核验证据。

> AI 动态研究路线与交付约束（2026-08-29）：用户只有一句模糊研究想法时，不要求其先理解 Workflow 或选择模板。程序确定性冻结项目附件、知识文档、Workspace 表格摘要和可信阶段目录，再启动可取消、可恢复、可审计的系统级 `research_starter` Workflow。科研模型从最多 8 个语义候选中重排并实际加载 1～4 个不可替代且互补的核心 Skill，综合其理论、方法、适用条件、冲突和局限，生成 1～3 条、3～5 层的课题专属路线；不再从四套固定拓扑中四选一。路线只能从宿主审计的阶段目录选择合法子集，证据链与数据链必须成组完整，阶段遵循可信因果顺序，并形成研究设计、真实 Python 结果，或由完整证据链支撑的报告交付稿；宿主而非模型编译正式 Workflow。规划 `selectedSkills` 必须与 Chat Run 实际加载快照一一对应且最多 4 个，Skill 适用阶段只由路线 `stages[].skillNames` 声明，宿主据此派生冻结绑定，避免模型维护两套冲突事实；采纳后冻结内容/包 SHA256、作用阶段与局限。规划文档工具只接受本轮附件枚举返回的真实 ID，假 ID 返回可恢复错误。路线选择以纵向摘要卡展示，完整层次、阶段、方法、Skill、输入输出和检查点进入独立详情窗口。正式任务的每个 AI 阶段只暴露并加载明确绑定到该阶段的冻结 Skill，无绑定阶段移除 Skill 读取工具；独立审查重新读取整条路线至多 4 个核心 Skill 的相关原始章节。长 Skill 只按当前阶段读取相关章节并完成相关截断分页，避免批量正文挤占研究上下文。缺失或漂移失败关闭。数据方法由受 Schema 约束的 AI 阶段生成依赖、参数和 Python 代码，再经显式环境准备与 Kernel 节点执行，Skill 不能扩大工具或执行权限。所有交付经过独立结构化 AI 二审和宿主确定性门禁；独立审查可使用 Skill 核验方法边界，但不能把 Skill 文本冒充证据。系统启动器不进入“我的方案”，规划 Run 与执行 Run 仍通过稳定科研任务身份显示为一个任务；无数据的数据路线只保存为阻塞方案。旧四类路线、旧 routeId、旧 Schema 测试夹具和前端兼容回填已经删除，科研启动只接受 `dynamic-v2`；独立方案模板继续保留，不属于旧启动器路线。

> 文献 AI 筛选与证据覆盖门禁（2026-09-04）：动态证据路线在人工确认前新增两个受闭合 Schema 约束的无工具 AI Analysis。首先将冻结课题拆分为 2～4 个互补检索式，`builtin.research.workflow.search@2` 在六个固定 Connector 上逐式查询、分别持久化来源，并按宿主候选 ID 合并去重；再对所有候选逐项给出核心/补充/排除、匹配度和理由，推荐 ID 不得超出检索返回集。用户可一键采用 AI 推荐或手动调整；确认后才进入任务级导入、索引和 Citation 签发。第二次 AI 审核对全部宿主签发摘录按独立文献归并，区分全文/摘要/元数据，检查人群、暴露或干预、结局、情境和方法覆盖，推荐标记不得超出当前签发集。当审核判定覆盖不足或用户遗漏 AI 推荐摘录时，Runtime 要求明示勾选“以有限证据继续”，冻结 `limited_citations_accepted` 与选择审计；该决定仅允许范围受限的低置信初稿，不得被方法、设计、报告或独立复核解释为证据充足。交付 Prompt 同时禁止用未计算的样本量/功效承诺、量表构念替换、重复测量层级错配、时间窗矛盾和面向用户交付稿中的 SciAide 内部实现术语；独立复核发现此类问题必须进入 issue 并阻止交付，不得仅移入 limitations 放行。

> 宿主最小证据下限不依赖模型自评：本次选择少于两项独立研究，或未观察到任何非元数据型全文摘录时，即使 AI 返回“覆盖充足”也必须进入有限证据确认；用户的接受与独立文献数、全文观察、遗漏推荐一起写入冻结审计。

> 科研任务资源与工具作用域约束（2026-09-01）：科研资源只有 `task`、`project_shared`、`conversation`、`legacy_project` 四类持久边界。新科研任务只能检索“当前任务 + 项目共享”，不得读取其他任务、普通会话或历史未归属资料；删除知识索引、删除任务记录与物理删除附件/Workspace 文件是三种不同操作，任何保留字节都不能自动提升为新任务输入。每个任务的 Workspace、冻结输入、Python、Shell、Kernel 和生成文件统一位于 `<Workspace>/.sciaide/tasks/<taskID>/`。Workflow AI 阶段虽然复用普通 Chat/Agent Loop 记账，但执行器必须通过 `workflow_ai_chat_runs → workflow_ai_executions → workflow_runs` 恢复 `researchTaskId` 和任务私有目录；缺少或漂移绑定时失败关闭，禁止静默退回项目根目录。资源管理器只为确有资源的任务显示入口，任务视图中的项目共享资源只读；科研产物批量删除是可恢复的软删除，知识库批量移除只删除索引并保留原附件和历史聊天。

> 工具错误诊断与 Workspace 契约（2026-09-04）：工具故障必须先按模型参数/路径、SciAide 作用域与工具契约、外部文件或 Python/Shell 内容三层定位。宿主提供的 `workflow_state.inputs.input_paths` 和 `builtin.workspace.list` 返回值是唯一可信路径来源；不存在的文件/目录返回可恢复提示，不再只报“工具执行失败”。`builtin.workspace.read_text` 支持有界 UTF-8 分页，`offset` 与 `maxBytes` 只能传 JSON 数字。Workflow 工具快照与当前 Registry 必须完全一致；不再放宽旧快照 Schema，任何定义变化都要求重新创建方案。

> 动态分析实现输出协议（2026-09-04）：`method_implementation` 使用 `dynamic-implementation-v4`，输出为“小型 JSON 元数据 + 独立 fenced Python 源码”，宿主合并后再做完整 Schema、路径与语义校验。源码不再塞进需要多层转义的单个 JSON 字符串；`v1/v2/v3` 解析、半截 JSON 补全和旧字段推断已移出生产路径，旧方案或旧任务明确要求重新创建。

> 资源操作边界（2026-09-09）：新动态科研阶段使用运行内资源目录，将真实对象解析为宿主签发的操作。模型只选择当前 enum 中的 actionId，不能自行拼接路径、附件 ID、Skill 名称或分页位置；宿主预加载同样使用该接口。引用绑定运行、任务、阶段和冻结工具契约，执行时重新核验作用域与 Skill 哈希，不能扩大底层只读权限。原始适配器参数/结果与有界模型视图分离保存，引用随 Run 删除且不进入项目归档；具体覆盖范围见 `docs/CURRENT_STATE.md`。

> 科研提交边界（2026-09-09）：冻结 Schema 允许等价表示规范化，但不得猜测语义、删除未知字段或拼接历史候选。真实阶段绑定、结构与业务校验通过后才能完成 Chat Run；内容错误在原会话内有界纠正，预算独立于网络重试，已有工具结果继续复用。原文必须先可靠持久化，绑定或存储失败不能伪装成模型内容错误；宿主最终复核 Skill、引用和结果来源。当前限额与验证结果统一见 `docs/CURRENT_STATE.md`。

### P8：发布加固（2 周以上）

**目标：** 达到可分发的稳定桌面软件质量。

交付物：

- 三平台安装、升级、数据迁移和卸载策略。
- 代码签名、依赖/SBOM、许可证清单。
- 完整威胁模型、依赖扫描、模糊测试、渗透测试和人工审计。
- 性能、内存、长会话、超大项目和磁盘压力测试。
- 隐私政策、诊断包说明、备份恢复文档。

退出标准：

- 所有发布阻断级缺陷关闭。
- 从上一稳定版本升级成功，并有备份恢复演练。
- 安全检查不含未处理的高危问题。
- 在干净系统完成安装到首个科研任务的全流程验收。

### P9：生态能力（稳定版以后）

- Skill/MCP 目录或市场。
- 包签名、发布者身份、审核、撤回和安全公告。
- 自动更新的签名验证与回滚。
- 云同步和团队协作需单独设计账户、加密和权限模型。

---

## 20. 第一轮实施清单

开始编码时按以下顺序提交小而可验证的变更：

1. 初始化 Wails/React 工程和 CI，不引入业务模块。
2. 实现 OS 目录解析、启动/关闭生命周期和脱敏日志。
3. 建立 SQLite、迁移、Project Repository 和集成测试。
4. 定义内部消息协议、Run 状态机、事件 Envelope 和 FakeModel。
5. 完成 Project/Conversation 的最小 Wails Use Case。
6. 实现 SecretStore 契约与当前操作系统 Adapter。
7. 实现 ModelProfile 与 OpenAI-compatible 流式 Adapter。
8. 打通“发送消息 → 持久化 Run → 流式事件 → 最终消息 → 重启恢复”。
9. 完成 P1 故障用例后，再进入 ToolRegistry 和 AgentLoop。

每次提交只改变一个明确边界；涉及 Schema、权限或外部协议时同时提交测试和 ADR。

---

## 21. 必须维护的 ADR

至少建立以下架构决策记录：

```text
ADR-001：Wails v2 与前后端边界
ADR-002：SQLite Driver 与迁移方案
ADR-003：OS SecretStore 和不可用时的行为
ADR-004：内部消息/流式事件协议
ADR-005：Tool JSON Schema 与权限模型
ADR-006：MCP Transport 和生命周期
ADR-007：Skill 包格式与信任模型
ADR-008：向量索引 MVP 实现与替换条件
ADR-009：Python/进程执行风险模型
ADR-010：项目备份、导入和版本兼容
```

ADR 必须记录：上下文、候选方案、决定、理由、负面影响和重新评估条件。

---

## 22. 最终架构结论

SciAide 的核心不是一个带聊天框的工具集合，而是一套以科研项目为载体、以 Agent Loop 为执行核心、以权限和审计为安全边界、以 MCP 和 Skill 为扩展机制、以来源和产物为科研闭环的桌面平台。

最终依赖顺序必须保持：

```text
工程与数据基线
  → 安全的模型聊天
  → 可审计的 Agent Tool Loop
  → MCP
  → Skill
  → 知识库与引用
  → 科研产物
  → Workflow/DAG 与代码运行时
  → 发布加固与生态
```

只要每个阶段严格通过退出标准，后续能力就可以在不破坏既有核心的情况下逐步增加；若某阶段无法满足取消、恢复、权限或测试要求，应返回该阶段修复，而不是继续向上堆叠功能。
