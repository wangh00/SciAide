# SciAide 科研智能体桌面平台——架构与开发实施基线

> 文档状态：开发基线（Baseline）
> 更新日期：2026-08-12
> 适用范围：SciAide 桌面端 MVP 至稳定版
> 原稿备份：`artifacts/start.original.20260812.md`

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
7. **默认文件权限限制在当前 Workspace。** 越界访问、覆盖、删除、进程执行和联网必须按策略处理。
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
| Skill | 领域指令、参考资料、脚本、工具依赖和权限声明的可安装包 | Skill 本身不自动执行；其脚本注册为 Tool 后才可执行 |
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

### 6.3 运行预算

每个 Run 必须有明确预算，默认值可由用户调整：

```go
type RunBudget struct {
    MaxModelTurns int
    MaxToolCalls  int
    MaxDuration   time.Duration
    MaxInputTokens  int
    MaxOutputTokens int
    MaxCost       *decimal.Decimal // Provider 能提供价格时使用
}
```

任何一项达到上限都停止继续调用，并向用户解释已达到的限制。禁止无限 Agent Loop。

### 6.4 上下文构建顺序

```text
1. SciAide 固定系统规则和安全策略
2. 当前项目明确配置的项目指令
3. 用户显式启用的 Skill 指令
4. 经裁剪的会话历史或摘要
5. 经检索得到的文献片段（标记为不可信资料）
6. 当前用户消息与附件说明
7. 当前可用 Tool Definitions
```

上下文规则：

- 文献、网页、MCP Resource 和 ToolResult 均用明确边界包裹，注明“数据而非指令”。
- Skill 指令不得覆盖系统安全规则或扩大权限。
- 记录实际启用的 Skill、检索片段 ID、模型 Profile 和工具定义版本，保证运行可审计。
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
| `workspace.read` | 当前项目可允许一次或长期允许 | 读取论文、笔记 |
| `workspace.write` | 默认逐次确认，可允许指定目录 | 生成 Markdown、CSV |
| `filesystem.external` | 必须逐次确认 | 访问 Workspace 外路径 |
| `network.domain` | 按域名确认或配置白名单 | Crossref、PubMed |
| `process.execute` | 默认逐次确认 | Python、R、命令行工具 |
| `destructive` | 永远展示影响范围并逐次确认 | 覆盖、删除 |
| `secret.use` | 只允许指定 Adapter 使用 | 模型/MCP 认证 |

权限授予作用域：

```text
仅本次调用 / 本次 Run / 当前项目 / 指定 Tool+资源范围
```

“始终允许”也必须带 Tool、项目、目录或域名范围，禁止全局无边界授权。

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
- 默认不允许网络，不注入模型 API Key。
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

### 10.1 Skill 包结构

```text
literature-review/
├── skill.yaml
├── SKILL.md
├── references/
├── assets/
├── scripts/
└── workflows/
    └── review.yaml
```

`SKILL.md` 保存领域指令；`skill.yaml` 保存机器可验证的元数据、依赖和权限；脚本和 Workflow 都是可选内容。

### 10.2 Manifest 示例

```yaml
schema_version: 1
id: literature-review
name: 文献综述助手
version: 1.0.0
description: 根据项目文献生成带引用的综述
entry: SKILL.md

activation:
  mode: explicit
  triggers:
    - 文献综述
    - literature review

requires:
  tools:
    - builtin.knowledge.search
  optional_tools:
    - zotero.search_items

permissions:
  - workspace.read
  - workspace.write

compatibility:
  sciaide: ">=0.1.0 <1.0.0"
```

### 10.3 安装和激活规则

- MVP 仅支持本地文件夹或压缩包安装，不提供在线市场自动执行。
- 安装前校验路径穿越、压缩炸弹、文件数量、单文件大小和 Manifest Schema。
- 原始包、安装副本和生成缓存分离保存。
- 记录来源、包哈希、安装时间、版本和用户授予权限。
- Skill 默认不自动激活；用户可以按项目启用，触发建议必须可见且可撤销。
- Skill 脚本不能因安装或加载自动运行，只能注册为受权限控制的 Tool。
- 缺少所需 Tool、MCP Server 或版本不兼容时，Skill 进入 `unavailable`，不得部分静默运行。
- Skill 升级先在暂存目录校验，保留上一版本用于回滚。

### 10.4 指令冲突和上下文预算

- 固定系统安全规则优先于项目指令和 Skill。
- Skill 之间按用户启用顺序和显式优先级组合；发生同名资源或互斥声明时要求用户选择。
- 每个 Skill 声明或计算最大上下文预算，防止大量说明挤掉科研资料和用户消息。
- 记录每次 Run 实际加载的 Skill ID、版本和内容哈希。

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
InstalledSkill
Source
DocumentChunk
EmbeddingRecord
Citation
Artifact
PermissionGrant
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
installed_skills
project_skills

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
├── cache/         # 可重建缓存和模型临时数据
├── logs/          # 脱敏日志
├── skills/        # 已安装 Skill
├── mcp/           # MCP 运行元数据，不存明文密钥
└── backups/
    └── trash/     # 被移除的托管 Workspace

用户选择的 Workspace/
├── sources/
├── artifacts/
└── .sciaide-workspace.json
```

配置目录、数据目录、缓存目录和 Workspace 必须分开，以支持清缓存而不丢数据。

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
- 恢复前重新确认模型 Profile、Skill 版本、MCP 工具定义和权限是否变化。
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
- 内置 Prompt 和 Skill 作为只读资源打包，用户修改时复制到用户目录，不直接改安装资源。

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

> 实施状态（2026-08-13）：P2.1 工具协议与持久化、P2.2 注册/权限/审批后端、P2.3 有界 ToolExecutor 与 Workspace 只读工具、P2.4 Provider Tool Calling、AgentLoop、ContextBuilder、运行预算及审批暂停/恢复后端均已完成。下一步进入 P2.5 工具调用与审批 UI、时间线和交互收口。

**目标：** 打通一条可审计的模型工具调用闭环。

交付物：

- AgentLoop、ContextBuilder、RunBudget。
- ToolRegistry、JSON Schema 校验、ToolExecutor。
- PolicyEngine、Approval、PermissionGrant。
- 只读工具：列出 Workspace、读取文本、知识搜索占位工具。
- 写文件工具使用原子写和明确确认。
- ToolCall 时间线、取消、超时、结果截断和 Artifact 引用。

退出标准：

- FakeModel 可以发起 Tool Call，工具结果回填后得到最终回答。
- 非法参数、未知工具、越界路径、权限拒绝不会执行工具。
- 达到 Turn/Tool/Time 预算后可靠停止。
- 崩溃恢复不会重复执行非幂等 Tool Call。
- Windows 路径穿越、junction 和符号链接测试通过。

### P3：MCP 接入（2 周）

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

### P4：Skill 系统（2 周）

**目标：** Skill 能安全安装、按项目启用并影响 Agent 行为。

交付物：

- `skill.schema.json`、Manifest 解析和兼容性检查。
- 安装、卸载、启用、禁用、版本回滚。
- `SKILL.md` 加载、上下文预算和内容哈希记录。
- Tool/MCP 依赖解析和不可用状态。
- 压缩包安全校验；脚本仅注册 Tool，不自动运行。
- 至少两个内置科研 Skill：文献阅读、学术写作辅助。

退出标准：

- 恶意路径、超大压缩包、缺失依赖和版本冲突均被阻止或清晰提示。
- Run 可追溯到具体 Skill 版本和哈希。
- 禁用 Skill 后不会残留指令或注册工具。
- Skill 不能扩大用户未授予的权限。

### P5：科研知识库与可信引用（3 周）

**目标：** 基于项目文献回答并定位引用来源。

交付物：

- PDF/TXT/Markdown 导入队列、哈希去重、解析状态。
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

### P6：科研产物与导出（2 周）

**目标：** 把回答转化为可管理、可复现的科研产物。

交付物：

- Artifact 模型、版本和来源关系。
- Markdown、DOCX 等至少两种稳定导出格式。
- 表格、图片、代码和数据文件预览。
- 引用样式渲染与结构化引用分离。
- 项目备份、导入和数据完整性校验。

退出标准：

- 产物能追溯到 Run、模型、Skill、ToolCall 和 Sources。
- 导出失败不覆盖用户已有文件。
- 项目备份不包含 API Key，恢复后引用关系完整。

### P7：Workflow/DAG 与数据分析运行时（3 周）

**目标：** 支持确定性、可恢复的复杂科研流程。

前置条件：P2～P6 均稳定，不得提前以 Workflow 替代 Agent Loop。

交付物：

- Workflow Schema、状态机、依赖校验、循环检测。
- 检查点、并行只读步骤、重试和人工确认节点。
- 所有步骤复用 ToolExecutor 与 PolicyEngine。
- 可选 Python 数据分析工具及明确风险提示。
- “检索 → 分析 → 生成带引用报告”参考 Workflow。

退出标准：

- 应用重启后能从检查点恢复。
- 非幂等步骤不会自动重放。
- DAG 循环、缺失依赖和输出类型不匹配在执行前被拒绝。
- Python 超时/取消能清理完整进程树，且无默认密钥和全盘权限。

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
