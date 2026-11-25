# 开发指南

更新：2026-11-04。当前能力和验收进度见 [开发进度](PROGRESS.md)，模块关系见 [系统架构](ARCHITECTURE.md)。本文只维护可复用的开发操作。

## 环境与依赖

正式构建目标为 Windows x64，运行需要 WebView2 Runtime。

| 组件 | 当前基线 |
| --- | --- |
| Go | `go.mod` 声明 1.25.0，`GOARCH=amd64`、`CGO_ENABLED=0` |
| Wails | v2.14.0，CLI 与模块版本保持一致 |
| Node.js | 22（与 CI 对齐） |
| 前端 | React 18.3.1 / TypeScript 5.7.2 / Vite 5.4.14，以锁文件为准 |
| SQLite | `modernc.org/sqlite`，不需要 GCC |
| Python | 分析与部分集成测试按需使用，不打包解释器进 EXE |

首次准备，在仓库根执行：

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go install github.com/wailsapp/wails/v2/cmd/wails@v2.14.0
go mod download
go mod verify
Push-Location frontend
try { npm ci } finally { Pop-Location }
```

确保 `go`、`node`、`npm`、`wails` 在 PATH 中。常规安装复用已提交的 `go.sum` 和 `frontend/package-lock.json`，不为安装依赖随意重写锁文件。只有主动调整依赖时才使用 `go mod tidy` / `npm install`，同时审阅依赖、许可证和锁文件差异。若锁文件意外缺失，应先恢复版本库文件，不把重新生成当作普通启动步骤。

前端不能导入被忽略的 `frontend/wailsjs`；使用 [wailsRuntime.ts](../frontend/src/lib/wailsRuntime.ts) 与 `window.go` Facade 边界。干净检出必须能独立完成 TypeScript 和 Vite 构建。

## 开发启动与数据隔离

```powershell
$env:SCIAIDE_HOME = Join-Path $PWD '.sciaide-dev'
.\scripts\dev.ps1
```

- 默认全局数据根是用户目录下的 `.sciaide`，数据库在 `data/sciaide.db`，托管项目在 `data/workspaces/<project-id>`。
- 大文件跟随项目：附件、文档缓存、知识索引、产物和任务执行文件位于 `<Workspace>/.sciaide/`。
- 显式 `SCIAIDE_HOME` 同时隔离 WebView2 资料到 `cache/webview2`；未设置时不迁移既有浏览器资料。
- 测试使用临时数据根、临时 SQLite 和独立 Workspace；不要直接修改正式任务状态来“通过验收”。读取正式事故现场需要只读捕获，原件与派生文件分开保存。
- 模型、Embedding、视觉渠道和 MCP 密钥只使用系统凭据存储，不写入源码、构建变量、日志或项目归档。无模型配置也应能构建和运行自动化夹具。

## 科研流程调试入口

用途：开发代理自行创建案例、读取原科研状态、回答程序问题、显式批准已核对的工具或采纳路线，再定位并修复错误，减少用户反复代点。它通过原 Application / Wails Facade 调用同一 Chat、Workflow、Agent 和校验链，不是假成功接口，也不替换科学审查。

### 启动与预算

仅独立开发命令启用；正常 `SciAide.exe` 不监听调试 HTTP。需要本机已准备的 Go/Python，不安装额外依赖。以下是可复用步骤，不是对任意真实任务的运行授权；实际执行结果见 [PROGRESS](PROGRESS.md)。

```powershell
# 独立终端 A；root 必须为空或由该入口创建，绝不能是正式应用根。
$root = Join-Path $PWD '.tmp/research-debug/my-case'
$source = Join-Path $HOME '.sciaide'
go run ./scripts/research-debug --root $root `
  --inherit-models-from $source --allow-model-calls `
  --max-requests 12 --max-duration 15m
```

科研调试案例与正式科研入口一致：服务器显式加 `--allow-full-access` 时，新案例和省略权限的路线启动默认使用 `full_access`；不加该开关仍默认 `plan`。已有 `plan` 隔离根不能悄悄改策略；在确认根已停止、来源不变且确实要继续同一案例时，必须同时使用 `--allow-full-access --upgrade-full-access`。升级会把旧/新策略与来源哈希写入根内的 `research-debug-policy-upgrades.jsonl`，失败或缺少显式升级参数都拒绝，不手改 owner 标记。

- 默认不允许模型外发；`--allow-model-calls` 才启用可能计费的请求。`--inherit-models-from` 只读指定根的模型配置，使用其既有凭据引用；不拷贝任务、资料、原始 URL、请求头或密钥到调试库。模型配置只在内存使用，本地仅存脱敏的 FK 占位配置。URL 自带 userinfo/query/fragment 的来源配置不可用于调试、列表不展示；不会让无关旧配置阻断安全模型。
- 自编的原 `WorkflowFacade` 定向组件客户端也须在创建或显式重试流程前，经 `ModelFacade.ListModelProfiles` 核验所选 profile / model 可用，让原只读 overlay 准备脱敏 FK 占位；不能直接拿来源 ID 启动流程、手工补库或关闭外键约束。直接保存本地模型配置的夹具不覆盖继承路径，须另用临时来源库验证修前失败、准备后执行及来源不变；续验还须核对原任务 / Run、冻结输入及编译哈希，显式 Retry 失败阶段，不为重试另建任务。原 `/cases` 已有该准入准备，不要求用户先点击模型列表。
- 上述占位保证针对本入口新建的空根；不追溯清洗旧实验工具已复制的设置、引用或日志。已有 profile 的仓储更新不会替换旧 secret_ref，不能声称重开后全部旧占位已转换。来源不明或旧实验根应保留原记录并改用新的空隔离根，不把“目录可打开”当成已清理历史敏感数据。
- 调试源与默认正式根、`SCIAIDE_HOME` 不能与 root 重叠；已有 root 绑定同一来源和 full-access 策略。启动前拒绝外部目录联接/符号链接及多硬链接文件。用操作系统持有的独占锁拒绝并发打开，进程异常退出不需要手工删锁文件。
- `--max-requests` 默认 12（1–1000），对内置三协议模型 HTTP 外发准入预先保存计数，覆盖协议协商、宿主恢复重试和 HTTP 重定向；重启不退回 Used。模型客户端使用标准 TLS / HTTP/1.1 新连接单次传输，避免下层透明重放绕过 Gate；不是失败后换路，具体契约见[架构](ARCHITECTURE.md#3-三种模型协议与恢复分层)，旧版漏计复现与修复验证见[进度](PROGRESS.md#模型-http-四项缺陷修复2026-11-06)。此处计算模型接口请求，不含代理 CONNECT / DNS；也不覆盖文献、工具、未知注入传输或其他服务费用。预记可能因崩溃或请求未到服务而偏多，不是 token / 金额限额，不能推算账单，不自动加预算绕过失败。
- `--max-duration` 默认 1h，最多 1h；这是调试授权窗口，不是科研阶段或单个模型请求的超时。可另传 `--deadline 'RFC3339时间'` 固定绝对截止（必须在初始窗口内），跨重启复用原值，不用重启偷换新的时长。无效窗口、端口或调用上限在打开工作区前拒绝，服务启动时再次核验窗口。启动或重启不自动重跑案例/阶段。
- 时间同时受墙钟截止和不可扩大的单调时钟硬上界约束；首次观察到期即锁存、禁止模型外发并取消实例，不因回拨而复活。每次 HTTP/Gate 检查，定时器也至多每秒复查墙钟变化；实际调度及正常退出排空仍可能延迟。到时、Ctrl-C 或 stop 接口取消应用、关闭连接、等待处理器和后台工作退出，最后释放根锁、移除描述符。写盘跨截止时不外发；已保守计入 Used 的次数不退款。
- 规划同一 Run 的正常工具审批恢复保留已经花费的进度/内容/Skill 纠错额度及原候选；恢复后仍重查当前 Skill 快照，不自动接受。旧非空 Run 没有检查点、模型请求尚无可续接决策、执行已不再活动或保存失败时明确停止，不从 journal 猜零额度。此时显式重试当前阶段即可，不需新建任务或重传资料；新尝试不自动继承旧拒绝稿/纠错额度。应用关闭后的中断仍沿原 Workflow/Chat 恢复策略，不把旧审批、`resume` 与阶段 `retry` 当成可互换操作，不自动恢复正式任务。
- `--capture-provider-errors` 默认关闭，仅供定位未知 Responses 服务错误。显式开启后，实例级观察点覆盖初始/回答恢复/Workflow 请求；在独立根最多追加 1 MiB 的 `provider-error-capture.jsonl`，每条原因最多 2048 个 Unicode 字符。不记录请求、原生输出项或工具参数；按实际 key/自定义 header 值及支持的 URL 编码形式尽力脱敏，不能保证过滤服务回显的部分值或其他变形，文件仍按敏感资料保管，不提交/分享。同步写盘可能增加本地延迟，不改变错误分类或自动重试策略；stream-event 公共诊断仍只含安全元数据，HTTP 非 2xx 仍沿既有脱敏规则。
- `--capture-http-responses` 默认关闭，**仅覆盖 Responses**，与上述错误正文及下述坏帧开关独立。显式开启后，在独立根的 `http-response-capture.jsonl` 最多追加 1 MiB；记录每次实际收到的 HTTP 响应状态、协议数字、到响应头前的耗时、声明长度，以及允许列表内的响应头（请求关联 ID、Retry-After、Content-Type、Server / Via 等，具体列表见[实现](../internal/model/responses/debug_http_response.go)）。不记录请求 URL / 请求头 / 正文，不读取、缓存或改写响应正文；没有收到响应的传输失败不伪造一条响应，继续使用既有网络诊断。重定向逐跳记录，但仍逐次受原 Gate 限额，不追加任何探针或请求。
  - 响应头先按实际 key / 自定义请求头值及受支持的编码形式脱敏，再屏蔽 URL、常见认证 / 秘密标记、压平空白，最多 256 个 Unicode 字符；每个字段合并前的值总计若超过 1024 字节则整字段省略并标记，避免先截断凭据而失配。多值以逗号合并，记录是规范化、脱敏的诊断元数据，**不是原始响应头字节**。不收集 Cookie / Set-Cookie、Authorization、Location 或其他非允许字段；Retry-After 保留有界脱敏文本，不按宿主重试等待上限改写，不意味着已实施新的冷却 / 重试策略。
  - 请求 ID、Server / Via 等仍可能带有私有信息，无法保证过滤未知或变形秘密；须在已确认限制访问的隔离根使用，按敏感资料保管、不提交 / 分享，也不放入公开快照 / 错误。捕获器不设置 Windows ACL，`0600` 不是 Windows 私有保证或 OS 沙箱。同步写盘可能增加延迟；不改代理、超时、请求格式、错误分类或恢复预算。观察器在创建共享客户端前配置，覆盖原初始讨论、回答恢复和 Workflow；正常 EXE 默认不开启，也不开放新的监听接口。
- `--capture-stream-faults` 是另一个默认关闭的**敏感故障取证**开关，仅在 Responses 事件 JSON 解码失败时记录该事件与同一响应的原始 SSE 尾段，用来区分服务原始字节与客户端分帧。每份最多 32 KiB、文件 `stream-fault-capture.jsonl` 总共最多 1 MiB；开启时每个响应暂存有界 128 KiB 尾窗，成功流不落盘。已知凭据先尽力脱敏再截取，未命中字节不规范化，保留原始摘要与脱敏/截断标记，Base64 仅为字节保真而非加密。**尾段可能包含正常科研输出或服务回显的私密内容**，不提交、不分享、不放入公开错误/快照；不保证过滤部分或未知变形。必须使用已确认限制访问的独立目录：捕获器不设置 Windows ACL，也不收紧已有文件权限；`0600` 仅是新建文件的 POSIX mode，不构成操作系统级私有保证或沙箱。该开关不改变解析、重试预算或科学校验；生产 EXE 不配置观察器。
- `--capture-request-snapshots` 默认关闭，与以上三个捕获开关独立；覆盖内置 Chat / Responses / Anthropic 的最终请求结构，契约见[架构](ARCHITECTURE.md#最终请求安全结构观察)。仅在原传输返回后检查可重放副本，写独立 `http-request-snapshots.jsonl`，追加总上限 1 MiB；每个请求副本最多 4 MiB，输入和工具清单各最多128项，保留总数/省略数，最多64个比较基线。重开追加旧文件但生成新 session/HMAC 密钥，不恢复旧比较基线，不追录历史请求。
  - 不保存正文或密钥仍须按私有证据保管：字节数、指纹及本地 Run ID 也可能暴露关联；使用预先限制访问的独立根，不提交/分享。捕获器不设置 Windows ACL 或收紧旧文件，`0600` 不是 Windows 私有保证。同步检查/写盘有开销，不承诺零延迟；未返回、进程崩溃、超出捕获上限或写入故障可能缺行，文件行数/sequence **不能代替持久 Gate、费用或 token 账本**。不能靠结构快照独自判定科研语义、服务为何超时或真实喂料是否合理。
- 源配置的变更在下一次读取/模型解析时反映；一个正在执行的多轮 Run 可能继续用已解析的客户端。需要立即停止调试外发时使用 stop/Ctrl-C，不把禁用源 profile 当成即时中断。

### 输入案例与读取状态

端口默认随机（`--port 0`），仅 tcp4 `127.0.0.1`。`debug-connection.json` 含随机令牌、地址、实例标识与截止时间，客户端在本机读取；不要打印、提交、分享该文件或令牌。令牌不是对其他本机账户/同用户恶意程序的安全隔离，调试目录和数据也应保密。

```powershell
# 独立终端 B；先把案例写为 UTF-8 prompt.txt，不修改正式任务。
$root = Join-Path $PWD '.tmp/research-debug/my-case'
python scripts/research-debug-client.py --root $root models
# 从安全模型列表选择实际 profile-id / model-id。
# 调用前生成并保存稳定 requestId；响应丢失时必须复用它和完全相同的输入。
$requestId = [guid]::NewGuid().ToString()
$requestId | Set-Content (Join-Path $root 'case-request-id.txt')
python scripts/research-debug-client.py --root $root new `
  --name 'writing-feedback' --prompt-file .\prompt.txt `
  --profile '<profile-id>' --model '<model-id>' --request-id $requestId
python scripts/research-debug-client.py --root $root wait --case '<case-id>' --timeout 60
python scripts/research-debug-client.py --root $root snapshot --case '<case-id>' --full
python scripts/research-debug-client.py --root $root stop
```

每根最多 20 个案例。创建请求持久保存案例、稳定初始消息标识，再启动原聊天；同 requestId + 完全相同输入仅找回原 Run，冲突输入拒绝。中断发生在初始聊天创建前时，重放才尝试初始化；发生在原 Run/消息事务已保存之后则找回 Run，不自动再调用。错误时返回 `result.case` 和 pending 状态，不能把“清单已保存”当成模型已经启动。Project/讨论创建与案例索引不是同一事务，极早崩溃可能留下隔离目录内尚未调用模型的本地孤儿，不声称全创建原子化。所有创建都要求调用方先保存 requestId；普通 `message` 也应事先生成并保存 clientMessageId，其他动作不承诺透明幂等或通用恰好一次，超时后先读快照对照消息/Run，不能盲重试 POST。

快照含 case、activeConversationId、顶层 state、独立 discussionState / workflowState、当前工作流/步骤/错误、聊天、问题/审批、冻结计划和历史。完成讨论占位不是研究完成；`discussion_idle` 仅表示讨论本轮回复完成，`plan_available` 也仅是待采纳计划。以持久绑定区分普通讨论与内部阶段 Run，最新普通讨论失败不能被旧计划遮盖，也不会因新内部阶段而复活更老的讨论失败。主动取消为 `chat_cancelled`，中断为 `chat_interrupted`；原工作流状态仍独立保留。

客户端默认输出紧凑快照，`--full` 才输出完整快照；`humanCheckpoints` 保留待确认步骤的真实输入与哈希。wait 遇问题、审批、计划、可确认返修 `revision_available`、Workflow 人工确认/暂停、讨论空闲或终态即返回；失败/中断/初始化待恢复返回退出码 2，等待超时为 3。退出码 0 表示已到可处理状态，**不等于科研完成**，必须读取 JSON 的 `state`；只有 `workflow_completed` 才是流程完成。输出 JSON 用 Unicode 转义，避免 Windows 控制台管道损坏中文；本地客户端禁用代理和重定向，不泄漏 Bearer 令牌到其他地址。

### 原位调整已授权的调试限额

先读 `status` 的 `session.id/revision/expiresAt/hardStopAt/secondsRemaining/requestsRemaining` 和 `modelCalls`，核对当前授权再提交；也包含在案例快照中。时间和调用额度独立：停用/到期后的可发次数为零，累计差额仍能由 Limit/Used 核对。调用上限不是余额；不得自动扩额或续时。

```powershell
python scripts/research-debug-client.py --root $root status
# 下列值必须来自当前实例和本次明确授权，不自动套用新 descriptor 的实例标识。
python scripts/research-debug-client.py --root $root limits `
  --session-id '<session.id>' --expected-revision 1 --confirm `
  --max-requests 40 --expires-at '<RFC3339截止，不能超过hardStopAt>'
```

`POST /v1/limits` 不重启、不打断在途响应、不重发模型请求、不清零 Used、不启用原本禁用的模型、不恢复阶段/审批科学结论。必须显式确认、匹配随机实例标识和授权版本；重放/跨重启旧确认以 `DEBUG_LIMITS_CONFLICT` 拒绝，不自动重试。总上限 1–1000 且不低于 Used，新截止必须仍在未来且同时满足当前实例墙钟/单调硬上界；已到期的实例不能被此入口复活。重开仍需明确授权，且沿用相同绝对截止与已用次数。

预算文件先保存后提交运行时授权；保存失败不改限额/时间。若保存跨过旧截止，则保留已保存的总上限和 Used，但取消实例、禁止外发，不续时，返回 `DEBUG_LIMITS_EXPIRED_DURING_SAVE` 及真实结果，不能将错误解释为“全部没保存”而盲重发。`status` 与持久预算为权威，描述符只是派生连接元数据；刷新失败时已提交的授权仍成功，`descriptorUpdated=false`，地址/令牌不变。返回状态重新核对写盘期间的取消/到期，不保证网络收包时实例仍活跃。

### 固定 HTTP / 动作接口

请求头 `Authorization: Bearer <local-token>`；POST 使用精确的 `Content-Type: application/json`，单个 JSON 对象、最大 1MiB，拒绝未知字段。拒绝跨站 Origin / 错误 Host；只向当前隔离根拥有的案例开放操作，没有任意 SQL、反射、密钥或模型配置写入口。

| 方法/路径 | 作用 |
| --- | --- |
| GET `/v1/status`、`/v1/models`、`/v1/cases` | 预算/能力、安全模型元数据、案例清单 |
| POST `/v1/limits` | `sessionId,expectedRevision,maxRequests,expiresAt,confirmed:true`；只调整已明确授权的调试限额 |
| POST `/v1/cases` | `name,prompt,modelProfileId,modelId,requestId,permissionMode?`；默认 plan |
| GET `/v1/cases/{id}` | 真实科研快照与诊断 |
| POST `/v1/cases/{id}/actions/{action}` | 下列白名单动作 |
| POST `/v1/shutdown`，正文 `{}` | 取消并停止当前调试实例 |

动作正文放在 UTF-8 JSON 文件，通过 `client ... action --case <id> --name <action> --json-file <file>` 发送。

| action | 正文要点 / 原调用 |
| --- | --- |
| `message` | `text,clientMessageId?,webSearchEnabled?`；规划前沿用初始讨论，采纳后进入原任务正式会话；不修改初始化重放标识 |
| `answer` | 原 UserInput Response：`runId,callId,answers`；每题为 `{"optionIds":["id"],"text":"optional"}`，不替用户偷偷选科研方向 |
| `approve` | `runId,approvalId,allow`，仅当前聊天待批项/本次调用授权 |
| `approve-workflow` | 同上，仅当前 Workflow 待批项；不误走聊天恢复 |
| `adopt` | `runId,routeId,confirmExecution:true`；原采纳接口可能立即执行，必须启动时加 `--allow-full-access`，且每次显式确认 |
| `start-route` | 原 StartResearchRouteCommand；省略权限跟随隔离服务器策略，显式 `--allow-full-access` 时科研路线默认 full，否则默认 plan |
| `retry` / `decide` | 原 RetryCommand / SubmitDecisionCommand，仅本案例 Run；不抹去失败记录 |
| `confirm-revision` | `runId,proposalId,confirmed:true,confirmExecution:true`；仅确认原讨论工具已保存、当前仍可确认的返修提案，原 Facade 校验快照、受影响阶段与副作用 |
| `clarify` / `replan` | 原 AnswerResearchClarificationCommand / ReplanResearchStarterCommand，使用 `starterRunId`；继承前次冻结权限 |
| `pause` / `resume` / `cancel` | `runId`，原 Workflow 控制 |
| `cancel-chat` | `runId`，原 Chat 取消；不越权取消其他案例 |

其余字段以[动作适配器](../internal/debugapi/actions.go)及其原命令类型为准；ProjectID 由宿主绑定。Plan 下仍有逐调用审批，代理只批准已检查、符合案例预期的动作；不会为测试方便放开生产权限。讨论准备规划沿用该聊天 Run 的冻结权限，clarify/replan 延续原 Workflow 权限；科研入口默认 full，但显式选择 Plan 的旧任务仍保持 Plan，不因创建新阶段自动升权。

交付后返修先通过 `message` 让原模型提出提案，再检查快照 `revisionProposals` 的范围、材料与副作用后显式确认。`revision_available` 不是已执行或科学批准；不得通过修改验收要求、审批科学结论或直接改库使测试变绿。没有可确认提案时不能用该动作强行启动返修。

### 自测顺序与边界

1. 先跑低层和本机 HTTP/临时 SQLite 夹具，至少覆盖问答恢复、规划/采纳、审批、失败、初始化重放、预算与关闭。低内存环境使用 `GOMAXPROCS=2`、`go test -p 1`，资源耗尽须单列，不能当作通过。
2. 再用独立 root、固定输入/稳定 requestId 和有限预算接现有模型，代理读快照、显式答题/推进，不要求用户重复手工点击。未知服务拒绝、额度耗尽、科学校验失败都停止并保留证据，不自动批准、不无限修复/重试。
3. 科研失败先沿输入→模型回复→工具/阶段→验证的单条链定位；修复后先回放本地复现，再用新的受控案例或显式重试验证。正式资料、历史与冻结任务不因开发测试被改写；不得将旧失败追改成功。
4. 流程完成后仍人工核对正文语言、篇幅、来源级别和实际交付形式，不以绿灯代替内容验收。返修测试应保留首稿，核对同一任务中未涉及的上游阶段、材料和冻结要求不变；自动化夹具、真实模型流程、原生 UI/导出版式和专家科学验收分别记账。

待答停止另验：普通讨论用 `cancel-chat`，不要用 `answer` 的 dismiss 冒充停止（它会恢复模型）。验证停止与回答同帧互斥、切换会话不解锁或串锁、无完整 Run 的终态事件也清理卡片、旧快照不复活终态；重启后无待答/无自动调用。冻结科研阶段仍用其原 Workflow 取消入口，不能为测 UI 越权调用聊天取消。合法迟到回答的预期错误是 `USER_INPUT_EXPIRED`，不是内部故障；输入校验和原科学检查不因测试放宽。

当前入口主要覆盖**文本案例**，不提供上传、任意文件访问或全局设置接口；额外视觉回退在开发 runner 内禁用，避免绕开同一模型预算。隔离根隔离的是应用数据，不是 OS/容器沙箱：full access 或被显式批准的 Python/本地工具仍可能访问宿主，因此只使用可信测试输入，不加载不明工具/材料。文献检索网络、附件/导出、数小时运行、原生 WebView 渲染与领域科学正确性需要分别验收。上游拒绝不代表应用流程已通，也不能仅凭脱敏的 unknown/unrecognized 代码断定额度、重置时间或具体服务故障。

### 科研模式收敛验收

每轮改进先限定一条输入→阶段→材料/工具→校验→可观察结果的闭环；这是开发验收方法，不是新的科研执行器、自动批准机制或真实模型调用授权。

1. **先固定基线**：保存可脱敏回放的输入、失败位置、原稿/材料身份及未受影响阶段。说明改变哪一层、预期减少哪种失败或无效工作；先让针对性反例在原实现上失败，再验证修复，不只检查新增提示词是否出现。
2. **验证组合而非只测零件**：材料投影、导航和读取按[上下文契约](ARCHITECTURE.md#4-上下文是有界投影不是数据库删减)共同验收。至少覆盖短输入、已有分页/共享值、较宽目录、中文/转义、重复投影及不可容纳的输入；程序生成的导航须经实际授权 reader 回读，不能只断言有 `$read` 字样。
3. **验证增量与保护边界**：返修保留原稿及失败，核对无关阶段/字段、冻结输入/图和权限不变；保留负例，证明过期身份、不可见来源、越权读取和科学否决不会因优化放行。不把阅读状态等同于科研完成度。
4. **预先约定成本观察**：受控闭环记录请求数、读取/重复读取及完整请求大小或预算；真实验收另约定模型、案例规模、请求/时间上限，分别报告结果和实际消耗。预算依据量测与任务范围设置，不为变绿删掉必要核读，不用跨案例数字承诺普遍提速或费用下降。
5. **设定停止条件**：离线反例、组合回归仍失败或根因未明时，先回到最早不确定的边界，不用付费长链代替定位。同类技术阻塞复现后先修共同机制，不机械追加额度；达到授权限额即停并保留未完成状态，后续窗口必须另获授权。

报告分别列出代码完成、受控回归、真实模型、原生 UI/导出与科学验收，未执行的项明确标注。既有任务是否能用新行为取决于冻结契约；不得为测试便利静默升级、迁移或重建正式任务。

### 契约驱动的代表性验收

以下为2026-11-16修订后的分层验收安排。**V0已新增并执行受控应用夹具，V1–V6按本轮涉及范围选用，不代表全部已执行**；本说明不构成启动服务、恢复任务或真实模型调用授权。按[目标架构](ARCHITECTURE.md#目标架构草案科研任务主导agent驱动)和[开发依赖](RESEARCH_DEVELOPMENT.md#五后续开发顺序)检验[K1–K4](ARCHITECTURE.md#四项稳定契约)，不把旧接口/状态机/阶段顺序的全部夹具通过当作新架构准入条件。原测试只有仍表达目标契约时才采用；改写结构断言前须明确对应的目标行为与替代验证，不能顺带删除权限/来源/生命周期负例。

| 场景 | 用户可观察的通过与反例 | 已有入口 / 还需覆盖 |
| --- | --- | --- |
| V0：目标架构最小竖切 | 同一研究任务不先生成Workflow即可读取两份合成摘要、记录有依据的观察，因反证调整下一行动并输出有限对照；没有必跑阶段或兼容规划运行。取消/预算停止不丢原记录 | 已新增`research_task_vertical_slice_test.go`并覆盖直接创建、两份反证摘要、依据回读、取消和重开；预算停止与其他协议另列。脚本化模型不证明科研判断正确 |
| V1：不完整想法与候选在途 | 可直接解释、按需探索；候选准备/失败不夺走原讨论。反例：简单问答强制规划、并行重复规划或未采用即执行 | 原助手/讨论身份夹具，另以`research_direct_planning_integration_test.go`覆盖无旧Run首候选、未知材料拒绝后继续规划及主会话回读；真实适时提问/选工具另验 |
| V2：保存→导航→有界读取 | 沿任务读取原记录/版本，标明省略及实际返回范围；长题名/摘要、中文转义、稀疏继承和窄窗先单变量再组合。反例：拿目录当全文、将历史视图当新来源回执、跨任务回读或超窗仍发送 | 原`research_task_discovery_integration_test.go`、`research_working_basis_integration_test.go`、`tool_history_integration_test.go`及容量矩阵只作边界参考，不要求新结构沿用原reader |
| V3：来源缺失、未读与相反证据 | 缺原快照、无法访问、尚未读取和原文不支持分别说明；模型可缩小结论或停止。反例：从私有结果猜补可见快照、按相似文字补关系、将条目复核当整项科学通过 | 原来源/工作记录/预算负例及`BibliographyService`核对；新增跨对象场景先受控。模型是否正确理解反证须真实与科学验收，不由fake结论证明 |
| V4：计划内应变与重大变更 | 授权内读取/查询及局部工作计划调整不增加审批表单；超出目标/方法/材料/交付授权走明确变更与确认。反例：旧确认套用新方案、上传自动采纳、篡改已执行输入 | 原`research_discussion_integration_test.go`、`revision_materials_integration_test.go`、`discussion_revision_ownership_integration_test.go`作边界参考；新目标能力须按新控制接口验证，不能只跑旧返修证明应变 |
| V5：仅改一处与正确否决 | 保留原稿、未改字段/数据/代码和无关上游身份，明确实际失效范围；再次审查仍能否决不受支持的新稿 | `report_revision_efficiency_integration_test.go`、`revision_write_isolation_test.go`及原审查闭环；窄正文/字符选择等真实覆盖仍须单列 |
| V6：取消、迟到与重开 | 待答/读取/规划等选定边界能停止；晚到结果不发布，重开无自动HTTP/副作用。反例：取消当坏JSON、恢复退还已用限额、终态复活 | `recovery_control_integration_test.go`、原问题/预算/关闭回归及前端迟到测试；不是所有并发、多进程和原生连点已验收 |

上表已有Go文件分别位于`internal/bootstrap/`，`revision_write_isolation_test.go`位于`internal/app/workflow/`；它们不是新架构已具备的完整测试套件。V0已有目标行为入口，但仍须按变更补反例；V1–V6按涉及责任选择行为与负例，不能只适配旧夹具而遗漏目标。新能力可直接用目标行为证明缺失，不要求先制造旧系统Bug，也不因此推定所有旧代码都需重写。

执行与停止顺序：

1. **限定基线与反例**：保留当前源码/输入/原记录，预期包括成功与正确拒绝。受控数据只用合成材料、临时SQLite及本机HTTP，不继承正式资料或真实连接。
2. **共同边界组合**：在相同有效预算下变化一个因素，再做必要组合；三协议验证归一化/原生配对、最终请求和生命周期，不盲跑全笛卡尔积。原[容量矩阵](#科研容量矩阵)只作规模参考；合法大输入、完整真人回答、已执行/已承诺输入身份及缺原快照的保护性停止须按目标机制验证，不强求新结构产生相同旧阶段状态。
3. **界面分别取证**：特点/活动/对象状态用实际目标组件和完整App浏览器夹具验证，页面刷新/展开不能触发模型；原生桌面、导出版式和长时资源各自专项，浏览器绿灯不替代它们。
4. **真实与科学另验**：仅在当前有效授权内使用隔离案例；记录模型HTTP、读取/重复读取、各环节耗时及实际返回usage，缺usage不捏造。比较须控制目标、材料和有效预算，正确拒绝与技术失败分开；不以fake科学意见、单次通过或更宽窗口证明科研正确性/总费用改善。
5. **有界替换而非兼容优先**：一轮限定一条完整竖切、责任和替换范围；既可补共同机制，也可按目标替换不合适的旧控制器/对象。重复技术阻塞先停回最早不确定处；旧窗口不复活，不加次数/重试或长提示兜底。新设计/未实现能力与已定位Bug分开，不用新夹具通过宣称旧事件已修复。

## 检查与测试

常规完整检查入口：

```powershell
.\scripts\p0-check.ps1
```

脚本名沿用早期命名，但实际包括：Go 格式检查（不自动改写）、前端类型检查/测试/生产构建、Skill 清单校验、Go vet/全量测试、contracts JSON 语法检查。它不是原生桌面或商业模型端到端测试。

脚本设置 `GOPROXY=off` 和 `GOTOOLCHAIN=local`，依赖必须先准备好。**这只禁用 Go 依赖/工具链下载，不等于操作系统断网或所有测试绝不联网。** Python、浏览器等环境依赖及测试跳过项须单独记录。

手动分项检查：

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
Push-Location frontend
try {
    npm run typecheck
    npm test
    npm run build
} finally { Pop-Location }
# main.go 嵌入 frontend/dist，先完成前端构建。
go vet ./...
go test ./... -timeout 20m
```

复验时如需排除 Go 缓存，加 `-count=1`；报告中如实注明缓存与跳过。低内存机器可用 `GOMAXPROCS=2`、`go test -p 1` 降低并发。每条 native 命令都要核对退出码，后续成功不能抵消前一步失败。

专题测试入口：

| 范围 | 入口与限制 |
| --- | --- |
| 文献来源 | `internal/bootstrap/literature_sources_integration_test.go` 覆盖实际 ERIC 适配器→受控 HTTP→临时 SQLite→索引及重开保全。`SCIAIDE_LITERATURE_SOURCE_LIVE=1` 才启用 `TestERICLivePublicSourceToIndexedPDF` 和 `TestCORELiveAnonymousMetadataContract`：访问公开 API/PDF、新临时根，0 模型请求；默认 skip，不等于科研审查或原生界面验收 |
| 科研交接与返修 | `internal/bootstrap/research_handoff_integration_test.go`、`source_sharing_integration_test.go`；受控 HTTP、临时 SQLite，不证明模型科学质量 |
| 科研助手探索与工作记录 | `internal/bootstrap/research_assistant_integration_test.go`、`research_state_question_batch_integration_test.go`、`research_working_state_budget_test.go`；原 Application 的本机模型/来源 HTTP 与临时 SQLite，覆盖不建路线的探索、问题续答提议绑定、稀疏更新、分页读取、小窗口及重开保全。`go test ./internal/bootstrap -run '^(TestResearchAssistant|TestResearchStateQuestionBatchNeverUsesFutureAnswer|TestResearchOpeningDiscussesThenPreparesWithoutExecuting)' -count=1 -timeout 3m`；非真实模型或科学验收 |
| 工作记录字段来源 | `go test -p 1 ./internal/bootstrap -run '^TestResearchWorkingFieldSourcesReadAfterSparsePatchAndRestart$' -count=1 -timeout 3m -v`；32k原 Application/Chat/Executor、本机模型HTTP与临时SQLite，稀疏补丁后关闭重开，按原state reader完整分页核对来源JSON/不改记录/不建路线，包含实际checkpoint而非真实模型。`go test -p 1 ./internal/storage/sqlite -run '^TestResearchWorkingStateFieldSources' -count=1 -v` 覆盖129版本、显式同值/清空、未发布/失败、缺失/非法/重复参数、跨任务、归档重映射和重开；workflow定向来源测试验证原权限、Unicode分页和歧义参数拒绝。前端`researchWorkingSources.test.mjs`与下述浏览器入口验按需加载、换版/任务隔离及读取失败。来源只证明最后显式提交，不证明证据支持或用户批准 |
| 任务文献发现闭环 | `go test ./internal/bootstrap -run '^TestResearchTaskDiscoveryPersistsPagesWithoutRoute$' -count=1 -timeout 2m -v`；原 Chat/Executor/Discovery/SQLite，受控模型和 ERIC HTTP，核对真实32k配置、保存/分页/重开/目录回读、无重复来源请求、普通聊天和原路线隔离。`internal/tools/builtin/research_discovery_test.go`、`internal/storage/sqlite/research_task_discovery_test.go` 验证页面预算、所有权与缺失/旧快照拒绝；`internal/app/agent/latest_tool_context_test.go` 验证新结果与旧省略标记、软阈值/有效窗口组合。默认不读真实模型配置或正式材料 |
| 保存题名/摘要字段 | `go test ./internal/bootstrap -run '^TestResearchDiscoveryReadsOriginalFieldAfterRestart$' -count=1 -timeout 2m -v`；受控 ERIC 先后返回同一记录的不同摘要，关闭重开后由真实 Chat/Executor 读取旧查询原摘要，核对完整内容/哈希/偏移及无新增取源。`internal/app/research/task_discovery_field_test.go` 覆盖空值、单字符、400和50000字符、Unicode/转义/字节上限；复用上述工具与 SQLite 测试验证逻辑模式、重复键及查询/候选归属。只证明规范化快照的机械保全，非真实模型、论文全文或科学验收 |
| 讨论身份与规划解耦 | `go test ./internal/bootstrap -run '^TestResearchDiscussionContinuesDuringPlannerAndAfterFailure$' -count=1 -timeout 2m -v`；在本机 HTTP 栅栏保持真实规划在途，人类切换实际32k窗口后读已存目录/写记录，核对规划模型冻结、重复规划拒绝、失败/重开后续谈及重新准备、当前候选回读和采纳关闭。`go test ./internal/storage/sqlite -run '^TestResearchDiscussion' -count=1` 覆盖真人时序、完成栅栏、旧105迁移、来源缺失与阶段关闭；workflow 的 `TestResearchDiscussionCurrentInputsAreReadOnDemandNotPinned` 验证需求/资料元数据不常驻系统且按页原样重组；前端 `researchDiscussion.test.mjs` 验证宿主能力及首选会话。临时根/受控模型，无真实模型或正式任务 |
| 候选规划直接交接 | `go test ./internal/bootstrap -run '^(TestResearchOpeningPreparesFromHostDiscussionWithoutModelRewrite|TestResearchDiscussionHostHandoffKeepsPlanningGuards)$' -count=1 -timeout 2m -v`；原 Facade/Chat/Executor、本机 HTTP 和临时 SQLite，让模型实际调用 `research_plan_prepare({})`，核对当前已存用户文本逐字复制、旧约束通过冻结讨论回读、仅候选无正式执行，以及32k在途讨论、重复规划拒绝、失败/重开后再准备和采纳关闭。`internal/app/workflow/research_plan_idea_test.go` 与 `internal/tools/builtin/research_opening_test.go` 验证身份/角色/终态/空值/Unicode上限、显式旧入口及模型不得指定宿主身份。只证明受控直接交接可行，不证明真实模型会选此路径或调用/耗时已下降；契约仅见[架构](ARCHITECTURE.md#科研讨论身份与可选规划) |
| 继承模型与同例返修 | `internal/bootstrap/inherited_debug_workflow_integration_test.go`、`internal/bootstrap/testdata/inherited-debug-numeric.json`；只继承临时合成来源库，覆盖先准备 / 漏准备后同例 Retry、重开不自动执行与计数保留、过期审查哈希拒绝、当前原生数值选段及局部返修 / 原门禁。已列入 `scripts/test-research-foundation.ps1`，不读取真实模型配置或任务，不构成真实模型验收 |
| 自然语言选源交接 | `internal/bootstrap/research_source_scope_handoff_integration_test.go`；从原研究请求、候选路线采纳、v6 结构化 `sourceIds` 到原文献工具 / 连接器的本机 HTTP 与临时 SQLite 回归，固定 ERIC / OpenAlex / Crossref 三源并拒绝越界来源。由 `scripts/test-research-source-scope.ps1` 独立运行；脚本模型是受控夹具，不构成真实模型语义理解或科研质量验收 |
| 独立审查提交 | `internal/bootstrap/review_local_repair_integration_test.go`；三协议、局部/完整修正、错误哈希与伪摘录、预算耗尽、重启及显式 Retry；完整科学拒绝仍保留。`SCIAIDE_REVIEW_BASIS_REPLAY` 可指向含冻结输入/Schema/三轮提交的只读本地捕获目录，启用 workflow/agent 可选事故回放；没有捕获时 skip 不算通过 |
| 审查共享视图与共同预算 | `internal/app/workflow/stage_input_shared_pages_test.go` 经原授权 reader 逐页还原 JSON，核对旧页、当前引用、Unicode/数字及坏引用拒绝。`SCIAIDE_REVIEW_JOINT_REPLAY` 可显式指向含 `captured-review-contract.json`、`citation-decision.json` 的只读捕获目录，然后运行 `go test -p 1 ./internal/app/agent -run '^TestCapturedSharedReviewAdmitsCompleteJointInterface$' -count=1 -v`；先校验原输入/提示哈希，以真实材料和明确合成的固定开销联合装配完整目录/提交接口，验证隐藏来源仍需实际可见页；无模型、无 Application 或数据库写入，不恢复旧执行。默认 skip 不算已回放。`go test -p 1 ./internal/bootstrap -run '^(TestSharedInventoryHTTPPagingPreservesFrozenSourceAcrossRestart|TestWideReviewNavigationHTTPRestartRepairAndGateAcrossProtocols)$' -count=1 -timeout 5m -v` 另验三协议本机 HTTP/临时 SQLite、原读取、重启/显式重试与门禁；不代表真实模型或专家科学验收 |
| 上下文与长报告 | `internal/app/agent/*budget_test.go`、`context_*test.go`；部分捕获回放需显式提供本地材料，不能把 skip 计作通过 |
| 前端行为 | `frontend/src/*.test.mjs`；`npm test` 不涵盖全部浏览器/原生交互 |
| 浏览器交互 | `scripts/test-*.cjs`；`test-user-input.cjs` 覆盖完整 App 问答/科研时间线和终态旧快照，`test-question-stop.cjs` 覆盖延迟取消/回答、跨会话及终态清卡。按脚本声明准备 Playwright/浏览器及生产 CSS；均为受控 Facade，不等于原生或真实后端 |
| 科研助手浏览器 | `node scripts/test-research-assistant.cjs`；完整 App / 受控 Facade / 本机 Edge，覆盖 1280–760px 面板、已保存发现、规划中/失败后的原人类输入框、候选/正式执行标签、任务归属、迟到响应、错误、普通聊天隔离和活动任务取消；工作记录来源另验展开才读取、同版复用、换版关闭旧详情、来源错误及明确重试。需已有 `esbuild`、Playwright 和 Edge；`SCIAIDE_PLAYWRIGHT_PATH` 可指向本机已安装的 Playwright Node 包，可先用 Codex workspace dependencies 工具定位预装包，不盲目新增依赖。`SCIAIDE_UI_FIXTURE_OUTPUT` 可指定独立诊断输出目录，默认在忽略的 `.tmp/diagnostics/20261112-research-assistant-first-slice/ui/`；零模型、非原生桌面验收。脚本存在或前轮通过不代表本轮已执行，环境缺失须单独记录 |
| 科研助手原生夹具 | `internal/bootstrap/research_native_interaction_fixture_test.go`；显式空隔离根、本机受控 HTTP，默认 skip；准备和停止步骤见下节，不以 seed/server 测试通过代替 EXE 交互验收 |
| 原生故障恢复 | `scripts/test-native-ui-recovery.ps1`；包含故障注入，只能用于隔离实例，不能对正式任务随手运行 |
| 原生退出诊断 | `scripts/test-native-lifecycle.ps1`；无控制台重定向的隔离启动、正常关闭、受控结束宿主及重开，不配置模型；不能据此宣称真实闪退已复现 |
| 真实科研验收 | `examples/research-cases/README.md`；真实模型、真实桌面与独立结果核对分别记录 |

### 共同预算诊断与首轮准入

```powershell
go test -p 1 ./internal/app/agent ./internal/tools/builtin -run '^(TestContextBudget|TestBuiltin|TestMCPDiscovery|TestOptionalBuiltinSearch)' -count=1 -v
go test -p 1 ./internal/bootstrap -run '^TestColdAdmission' -count=1 -timeout 3m -v
```

以上均为原宿主路径的受控验证，不读真实模型或正式资料。先保留24k无usage短答拒绝及无分项的红例，再核对同窗1次HTTP可开口、辅助定义→原目录→按原路径精确只读的5次HTTP闭环、超长输入仍0次HTTP拒绝。Agent夹具另验同批猜调用、失败/伪造/过期回执、当前范围、原审批与恢复；原MCP、协议完整性、真人回答、冻结提交和下述25组矩阵须组合验证，不把某工具搜索成功当权限或科学批准。

错误的`budget`部分记录`phase`、`estimation_source`、原有效预算/剩余、定义与输入字符足迹及回放/usage预留，仅计量不保存正文。`component_unit`明确：字符足迹不能与已上报usage预留直接相加；`current_input_isolated=false`表示未单独定位当前输入，最终请求只标用户消息合计。此处的本地定义候选构建不发HTTP，不是模型重试；真正需要辅助能力时的发现轮次另计，不能隐去该成本。机制只见[架构](ARCHITECTURE.md#4-上下文是有界投影不是数据库删减)，结果只见[开发进度](PROGRESS.md#共同预算诊断与首轮准入2026-11-15)。

### 恢复审计来源身份

```powershell
go test -p 1 ./internal/app/agent -run '^TestRecoveryDiagnostics' -count=1 -v
go test -p 1 ./internal/bootstrap -run '^TestRecoveryControlAuditPersistsAcrossCancelShutdownAndRestart$' -count=2 -timeout 3m -v
```

只用原三协议客户端/Chat/Application、本机HTTP和新临时SQLite，不读正式资料或调用真实模型。先复现主动关闭落成`INTERNAL_ERROR / invalid_syntax`的红例，再验取消与宿主deadline来源、包装错误、主类型错误优先、缺载荷不解析`Error()`及同样文本来自真实坏SSE时仍拒绝。三协议分别验显式取消/宿主关闭→连接断开→原终态和审计持久保存→重开不重发，每组仅一次模型HTTP、零工具执行，不把fixture成功算作真实模型或原生UI验收。契约只见[恢复分层](ARCHITECTURE.md#3-三种模型协议与恢复分层)，已执行结果见[开发进度](PROGRESS.md#恢复审计的宿主控制身份修复2026-11-16)。

### 最终请求结构回归

```powershell
go test -p 1 ./internal/model ./internal/modelutil ./internal/model/gateway ./scripts/research-debug -run '^TestRequestSnapshot' -count=2 -timeout 2m -v
go test -p 1 ./internal/bootstrap -run '^Test(RequestSnapshotsFollowOriginalToolLoopAndSQLiteRestart|RecoveryControlAuditPersistsAcrossCancelShutdownAndRestart)$' -count=2 -timeout 3m -v
```

仅本机受控 HTTP/临时 SQLite：三协议分别比较开关前后的完整接收字节、原流和请求数，核对真实工具循环、usage关联与重开；另验参数协商/重定向、Gate拒绝、超限/坏副本、隐私标记、自定义取消原因和响应头超时。运行器默认关闭及显式开启都不授权模型外发；测试中不继承真实配置。与原容量/恢复组合验证，不把夹具绿例、字节增长或收到响应头当真实模型/科学验收；结果只见[开发进度](PROGRESS.md#最终请求安全结构快照2026-11-16)。

### 完成工具历史窗口与回读

```powershell
go test -p 1 ./internal/app/contextmemory ./internal/tools/builtin ./internal/app/agent ./internal/app/permission -run '^Test(ToolLedger|ToolHistory|PlanToolHistory)' -count=2 -timeout 2m -v
go test -p 1 ./internal/bootstrap -run '^TestToolHistoryOriginalApplicationWindowAndRereadRestart$' -count=2 -timeout 3m -v
```

这是正常原执行链的受限候选，不需调试开关或新数据库；触发/排除规则只见[架构](ARCHITECTURE.md#当前run完成工具历史的有界回读)。仅通过候选后开放 `builtin.context.tool_history`；复制宿主引用的 `throughCallId` 与 `ledgerSha256`，`view=index` 每页最多4条，`view=arguments/model_context` 对目录中的 `callId` 每页最多1000个Unicode字符，按 `nextOffset` 至-1，不把字符数当token。只读当前Run已存视图，不能传入其他Run/项目或任意文件路径；旧缺失/不支持的快照不猜补，也不提供私有完整Result。页在签发续页前核对原模型结果封装完整性，转义超限明确拒绝，不用截断头尾冒充完整页。

测试只用本机HTTP/临时SQLite，无真实模型或外部MCP。三协议分别验近期窗口→旧目录→已存参数两页精确回读，以及窗口启用后的取消/关闭/重开；Chat Completions用两批各32次已完成配置查询触发较早的归一化预算入口，两原生协议保留原32次夹具。较小Chat样本旧代码本已能走完成证据入口，不能把它当新增修复收益。单元验归一化/原生32/96次调用、最低返回预留、完整真人回答、缺快照、前缀/范围变化、Unicode/转义、Plan边界及混合原生历史的顺序/配对反例。未设置 `SCIAIDE_TOOL_LEDGER_BASELINE` 时始终测当前实现；该变量只供配合保全原文件的Go overlay复现旧应用基线，不是产品开关、恢复案例或真实模型授权。与原准入、容量、独立审查和权限回归组合；真实回读选择、总成本及科学效果须另验，结果只见[开发进度](PROGRESS.md#归一化工具协议预算入口统一2026-11-16)。

### 科研容量矩阵

仅测试原 Application/Chat/Executor 和临时 SQLite，以本机假模型/ERIC HTTP探测窗口、材料与连续调用的组合边界；不读取真实模型配置或正式材料，不恢复旧例，不构成真实模型调用授权：

```powershell
$env:GOPROXY='off'; $env:GOTOOLCHAIN='local'; $env:GOMAXPROCS='2'
$env:SCIAIDE_CAPACITY_OUTPUT=Join-Path $PWD ('.tmp/diagnostics/capacity-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
go test -p 1 ./internal/bootstrap -run '^TestResearchCapacity(RepresentativeScaleAndNarrowWindows|OrdinaryConversationControls|SyntheticUsageControls)$' -count=1 -timeout 8m -v
```

- 18个科研样本覆盖16k–36k最短讨论、3/12/48候选、128/1600/6400字符连续摘要、50000字符来源的规范化首尾抽读、28k/30k/32k显式关联和长当前输入；3个普通对话控制使用同窗口、同输入、同Full Access，区分共同入口与科研绑定。每组独立临时根，不扩大原窗口；固定本机HTTP上限96、checkpoint上限4，单组等待终态最多45秒。它是确定性模型响应夹具，不是第二执行器或模型语义/科学评判。
- `outcome=selected_work_completed`只表示选定夹具动作完成；`capacity_stopped`是已保留材料的容量停止，不能写成科研通过。32k短答/小索引/短摘要/关联为最低操作控制，较大样本是容量调查，不预先假定都能完成；夹具异常或最新结果被误当可见会使测试失败。
- 主矩阵不返回usage；另外4组仅改变此变量，在本机响应上报`max(1,完整HTTP的Unicode字符数/4)`合成prompt usage，覆盖原计量锚点路径及首轮尚无回执的边界。合成比例不是tokenizer、真实usage或收费证据，不得移入生产估算；这4组与21个无usage样本分开记账，已准入的合成正控须完成同一所选工作，不能以仍然停止冒充正控通过。
- JSON记录实际本机HTTP/取源/checkpoint、请求字节及角色/定义字符数、`phaseAtStop`、已存索引/字段结果与模型实际已见数量、provider上报input tokens（Run累计，非单次请求窗口）和预算，`case.syntheticUsage`明确标识合成锚点。无回执时宿主走保守字符估算；HTTP字节、Unicode字符、估算单位、合成或真实token不能混算。来源规范化上限与上下文分页另列，不把大来源首尾两页当完整核读。
- 重放时核对原用户文本、查询快照、任务冻结输入/编译、无重复页/取源、无额外材料/路线/执行，特别保留“工具结果已保存但尚未递交模型”的停止边界。`SCIAIDE_CAPACITY_OUTPUT`只输出这些合成材料的计量JSON，不保存完整HTTP正文或聊天；每次选独立目录以保留原测量。需要临时诊断时先备份并核对哈希，完成后立即原样撤回，不留观测性生产改动。

当前结果及未修边界只见[开发进度](PROGRESS.md#科研材料规模与窄窗容量2026-11-15)。新增测试或目录不需要重新编译EXE；只有生产代码改变时才按[唯一构建入口](#唯一生产构建入口)重新核验，不能把旧EXE当新机制已交付。

### 工作记录显式摘要关联

受控验证仍走原 Application / Chat / Executor / SQLite，不新建科研执行器，不读正式材料或真实模型配置：

```powershell
go test -p 1 ./internal/app/workflow ./internal/storage/sqlite -run '^(TestResearchWorkingBasis|TestResearchWorkingAbstract|TestResearchWorkingStateExplicitBasis)' -count=2 -v
go test -p 1 ./internal/bootstrap -run '^TestResearchWorkingBasisLinksActualReadReceiptsAndReopensAfterRestart$' -count=1 -timeout 2m -v
```

前者以独立临时库重复核对关联上下限、失败/跨任务/晚到/截断/快照与实际回执不匹配的拒绝、稀疏继承/显式重交、原页回看和归档身份定点映射；后者在原32k配置实际读两页→关联→只改下一步→关闭重开→只读回看，记录本机假模型请求数与完整请求字节，不能作为真实模型效率或科学通过。还须组合上表的规划在途/直接交接、小窗口和来源回读回归，不能只验新增功能。前端 `researchWorkingBasis.test.mjs` 与下述完整App离线浏览器入口覆盖按需读取、同版复用、换版/任务隔离、纯文本及错误重试；不是Wails原生后端或真实科研验收。机械契约只见[架构](ARCHITECTURE.md#科研讨论的工作记录)，实际结果只见[开发进度](PROGRESS.md#工作记录显式摘要关联2026-11-15)。

### 科研助手原生交互夹具

只用于已授权的隔离交互验证，不继承正式模型/材料，不调用真实模型。`SCIAIDE_NATIVE_INTERACTION_FIXTURE` 必须为本仓库 `.tmp` 下的新目录，不能复用已有 `home`。在一个终端设置其绝对路径后运行：

```powershell
go test ./internal/bootstrap -run '^TestResearchNativeInteractionFixture$' -count=1 -timeout 25m -v
```

夹具通过原 Facade 建合成项目、任务和普通聊天，**关闭 Application 后**才写 `ready.json`；另一个终端核对其中隔离 `home`，仅为该 EXE 进程设置 `SCIAIDE_HOME`，启动当前 `build/bin/SciAide.exe`。夹具余下进程只提供 loopback 假模型及固定控制，不执行科研；第24次本机请求或20分钟触发停止，也可显式 `/fixture/finish` 提前结束。此阈值不是并发在途请求的严格物理 HTTP 预算，不用于真实模型。不要对正式实例注入此模型或使用其控制地址。

若验收停止，在隔离科研讨论发送“验收：停止当前模型回复”，看到合成回复开头后使用原生窗口的“停止生成”。核对不出现“不应出现的迟到正文”、不会自动重发，并可继续发送新消息；关闭重开后仍是原终态。夹具 `/fixture/status` 的 `stopRequests` / `stoppedReplies` 分别指请求进入持有分支和本机 HTTP 断连；它们不能证明客户端已收到迟到帧。已解码帧在取消后返回的竞态由 Agent 定向流测试验证。

应由真实 EXE 发送“更新工作记录”“准备候选”，在规划等待期间继续原人类讨论；用 `/fixture/release-planner` 释放受控失败后，再发送含“失败后”的记录更新，核对版本及普通聊天隔离。假模型仅按上述固定输入返回预置响应，不是通用科研助手；此夹具不创建正式执行路线，正式执行锁定另由浏览器/后端组合覆盖。未经过的动作不补记通过。结束时先关闭且完整路径确认测试实例，再调用 `/fixture/finish`，保存 `model-summary.json`，确认无遗留进程/监听。Go 测试的 pass 只代表 seed/server 生命周期；默认 skip、原生启动或自动化截图/输入通道失败均不代表原生交互通过。

### 直接科研讨论停止浏览器回归

从项目根运行；须有最新的生产前端 `frontend/dist`（可在 `frontend` 下执行 `npm run build`，或由唯一发布脚本生成）。接受可解析的 `playwright`，也可通过 `SCIAIDE_PLAYWRIGHT_PATH` 指向单独准备的 Playwright / Playwright Core；默认使用已安装的 Edge，可通过 `SCIAIDE_BROWSER_EXE` 指向已安装的 Chromium 浏览器。不要求向项目依赖清单添加浏览器驱动。

```powershell
node scripts/test-research-direct-stop.cjs
```

夹具只加载生产前端资源，内存替代 Wails Facade，不使用 SQLite、正式会话或模型。创建直接任务后发送、停止；在暂缓返回终态快照时送达更晚的文本和完成事件，确认不会改写已停止的回复。重开原任务不自动发消息，显式续问仍属于同一任务且不创建 Workflow。此夹具不替代上节原生点击、真实模型或科学验收；实测结果见[开发进度](PROGRESS.md#直接科研停止后的前端事件保护2026-11-22)。

#### 问题卡与候选切换

沿用上节生产前端与浏览器配置，从项目根运行：

```powershell
node scripts/test-research-direct-handoff.cjs
```

注入采纳状态与候选方案读取失败，核对待答和提交仍归原讨论；候选已保存但读取失败时重开，验证原问题卡和失败提示，恢复读后才显示未采纳候选。分别阻塞左侧历史、右侧任务列表和会话归属查询，在同项目切到另一任务才释放旧响应，核对当前会话和面板都不会切回；归属读取失败不同于确认无科研绑定。各场景检查没有额外发送、采纳或启动，不替代原生或真实模型验收。

待补数据采纳的故障回归见 `internal/bootstrap/research_starter_integration_test.go` 的 `TestResearchStarterAdoptedDataRouteKeepsOneTaskIdentity`。在临时 SQLite 阻断采纳事件写入，核对方案列表与待数据入口都未新增；解除故障后重新采纳、读取和启动仍绑定原任务和同一冻结版本。仅本机受控 HTTP。结果见[开发进度](PROGRESS.md#科研任务切换与采纳一致性2026-11-24)。

### 同请求上限文献对照

`internal/research/connectors/literature_comparison_test.go` 的 `TestLiteratureEqualBudgetLive` 默认跳过；显式启用才访问公开文献 API。它复用原 Service/适配器，不运行科研任务、不读取正式材料或配置模型、不下载 PDF。版本化计划先落盘并计算 SHA256，固定两个问题、年份、每式条数和三策略；总上限 **32 次实际 HTTP / 480 秒**，每策略每问题预留 **14 次物理 HTTP**。PubMed 每槽预留两次，不以一次逻辑工具调用冒充一次 HTTP；未花完的预算不追加检索。只有此实验将传输尝试设为 1，不改生产重试策略。

当前捕获为 v2，使用不可透明重放的标准 TLS / HTTP/1.1 新连接模式。旧 v1 只记录外层 RoundTrip，不能证明线上精确 HTTP 总数或硬上限；分析器保留题录消融数据，但标明 `legacy_outer_roundtrip_only_unverified_wire_ceiling` 并将线上 `actualHttp` 设为 unknown，不追改旧捕获。

以下为显式联网操作示例，**不是自动执行授权**；先确认本次范围/上限。新建独立父目录，`$capture` 必须为绝对路径且不存在，分析输出也不得已存在：

```powershell
$root = Join-Path (Get-Location) ('.tmp/diagnostics/literature-eval-' + (Get-Date -Format 'yyyyMMdd-HHmmss'))
New-Item -ItemType Directory -Path $root -ErrorAction Stop | Out-Null
$capture = Join-Path $root 'live'
$analysis = Join-Path $root 'analysis.json'
try {
    $env:SCIAIDE_LITERATURE_COMPARISON_LIVE = '1'
    $env:SCIAIDE_LITERATURE_COMPARISON_OUT = $capture
    go test -p 1 ./internal/research/connectors -run '^TestLiteratureEqualBudgetLive$' -count=1 -timeout 9m -v
    if ($LASTEXITCODE -ne 0) { throw 'capture failed; keep evidence and do not expand bounds automatically' }
} finally {
    Remove-Item Env:SCIAIDE_LITERATURE_COMPARISON_LIVE -ErrorAction SilentlyContinue
    Remove-Item Env:SCIAIDE_LITERATURE_COMPARISON_OUT -ErrorAction SilentlyContinue
}
# 以下两步仅离线处理，不再请求来源接口。
python scripts/analyze-literature-comparison.py --self-test
if ($LASTEXITCODE -ne 0) { throw 'offline analysis self-test failed' }
python scripts/analyze-literature-comparison.py --capture-dir $capture --out $analysis
if ($LASTEXITCODE -ne 0) { throw 'offline analysis failed; do not overwrite the original capture' }
```

`plan.json` / `capture.json` / `http-*.raw` 分别保留计划、规范化结果/逐请求状态与响应体；摘要只保存端点，不保存请求头、查询串或传输错误中的代理凭据。捕获体有 8 MiB 上限并标明是否仅前缀，读取/关闭错误不被包装器吞掉。分析器核对计划、完整槽位、时间、逐请求计数及原始体哈希，拒绝缺失、越界路径和覆盖现有输出。来源失败属于有效观测，不伪装成空结果；条件未满足时保留部分证据，不自动加额或重跑。

三策略共享冻结响应池，是**离线反事实子集**，不是三次独立计费/耗时实验；同上限不等于相同实际花费或 API 额度单位。保守标识分组不是独立研究，关键词代理不是纳入/相关率/召回率，PDF URL 不是正文。已执行结果及下一步判断只见[开发进度](PROGRESS.md#4-验证结果与边界)，来源权限只见[来源契约](NETWORK_BROWSER.md#文献来源与能力契约)。本入口不需要新建科研任务，也不改变旧冻结图。

### 领域分配与预算接口验证

领域分配的独立公开 API 测试是 `TestLiteratureProfileLiveBounded`，默认 skip。它通过原 Service、适配器和实际预算专用传输执行预设两个领域/问题，**不测试模型分类或科研阶段**；每例根预算 14，总 HTTP 上限 28、共享 5 分钟，0 模型调用，不取 PDF。先取得本次范围授权，再为环境变量 `SCIAIDE_LITERATURE_PROFILE_LIVE=1`、`SCIAIDE_LITERATURE_PROFILE_OUT=<新的绝对捕获目录>` 赋值，运行：

```powershell
go test -p 1 ./internal/research/connectors -run '^TestLiteratureProfileLiveBounded$' -count=1 -timeout 6m -v
if ($LASTEXITCODE -ne 0) { throw 'keep the partial profile capture; do not expand limits automatically' }
```

目录父级需预先准备、目标目录不得存在；结束后在 `finally` 中恢复/清除上述两个 opt-in 环境变量。逐 HTTP 原始体、计划和规范化结果另存，领域捕获版本与前节反事实对照版本不同，不把后者的分析脚本套到此数据。来源失败保留为观察，不把测试完成写成所有来源成功。具体请求定义与兼容边界只见[来源契约](NETWORK_BROWSER.md#文献来源与能力契约)，最新已执行结果只见[开发进度](PROGRESS.md#4-验证结果与边界)。

### 文献查询质量与元数据标注对照

此入口评估预设表达式返回的元数据，而不是第二套科研执行器。版本化协议：`contracts/literature-quality-pilot-v2.json` 为两道写作题/12 次来源 HTTP；`literature-quality-pilot-v3.json` 扩至 AI、同伴、教师反馈和自评四题/24 次。均固定 ERIC / OpenAlex / Crossref、2022—2026、每式首屏 5 条、480 秒采样窗口；不运行模型、访问正式库、取全文或自动补搜。旧协议及响应保持冻结，不升级证据。协议先冻结，再捕获；执行结果及限制只见[开发进度](PROGRESS.md#4-验证结果与边界)，走完槽位不等于来源成功或质量通过。

先在禁用所有 `SCIAIDE_LITERATURE_*_LIVE` 开关的独立进程中执行本地检查（环境同下一节，逐条检查 `$LASTEXITCODE`）：

```powershell
go test -p 1 ./internal/research/connectors -run '^TestLiteratureQuality(ExpandedPreflight|PreflightControls|DispatchGuard|DispatchTLSFixture|PilotPlan|QueryAuditReplay|ReplayTransportIsClosed)' -count=1 -v
python scripts/analyze-literature-quality.py --self-test
```

v2 先以不委派网络的 transport 运行原 Service/适配器，检查六组实际表达式恰为基线加预设人群后缀，且年份、条数、排序及其他参数不变；这只是文本控制，不保证布尔语义等价、排名单调或检索质量提升。**v1 AI / Crossref 的词频混杂现在能在发送前复现并拒绝**，不修改旧协议或生产查询编译。实际派发 guard 再核对 GET、固定 endpoint、白名单参数及原始编码查询；漂移/未知参数/重复槽在进入 HTTP 计数及 delegate 前拒绝。不采集凭据或请求头。

受控 TLS 夹具与 live 共用冻结交错顺序；本机服务实际核对收到的参数。默认夹具为 v2 的 12 个本机请求，显式设置 `SCIAIDE_LITERATURE_QUALITY_FIXTURE_PROTOCOL=literature-quality-pilot-v3` 时为 24 个，均非公开来源请求。可用 `SCIAIDE_LITERATURE_QUALITY_FIXTURE_OUT` 导出到新的绝对目录，或用 `SCIAIDE_LITERATURE_QUALITY_PREFLIGHT_OUT` 导出 v2 预检；仅运行相应测试，结束后在 `finally` 恢复/清除变量。再按下段导出 raw/query replay，可用 `python scripts/analyze-literature-quality.py --self-test --capture-dir '<本机夹具目录>'` 验证模式/参数漂移、顺序、显式 partial 与严格拒绝反例；它只变异私有临时副本。

新的公开捕获仍必须有明确授权及本次预算。更改表达式/来源/次数需另行版本化并复核，不能改旧计划后套用旧响应。确需运行时，在新绝对输出目录上显式设置 `SCIAIDE_LITERATURE_QUALITY_LIVE=1`、`SCIAIDE_LITERATURE_QUALITY_PROTOCOL`（完整 v2 或 v3 版本名）、`SCIAIDE_LITERATURE_QUALITY_OUT`，只执行 `go test -p 1 ./internal/research/connectors -run '^TestLiteratureQualityPilotLive$' -count=1 -timeout 9m -v`；缺失/旧协议在建目录或请求前拒绝。目录必须不存在、父级预先准备；来源返回限流后沿原客户端冷却，未用上限不自动重分配或补跑。所有 opt-in 变量在 `finally` 中恢复/清除；本段命令不是执行授权。

已有完整成功捕获可离线回放，不发新 HTTP。将 `SCIAIDE_LITERATURE_QUALITY_REPLAY_DIR` 指向包含 `plan.json` / `capture.json` / `http-NNN.raw`（v2/v3 另有 `preflight.json`）的绝对目录，`SCIAIDE_LITERATURE_QUALITY_AUDIT_OUT` 指向新的绝对 JSON 文件，运行 `TestLiteratureQualityQueryAuditReplay` 并在 `finally` 中恢复/清除变量。它按冻结计划选对应 v1/v2/v3，核对当前同版本协议字节一致；禁止委派外网、跨 endpoint 或第二次请求，复用原 Service/适配器。导出拒绝覆盖。**默认严格入口仍拒绝含失败槽的质量报告**，不将失败、无体或截断重写为成功。

需要检查已保存的限流及成功题录时，可额外显式设置 `SCIAIDE_LITERATURE_QUALITY_DIAGNOSTIC_PARTIAL=1` 导出部分诊断审计，再给下述 Python prepare/分析命令都加 `--diagnostic-partial`。成功槽才重放原体；失败槽保留 failed，不重放为 empty，仅以无网络夹具重算投影并逐槽比对冻结 preflight（包括编码参数），有现场请求才与现场比对。包额外绑定 partial replay 哈希，报告强制 `partial_failure_diagnostic_not_quality_acceptance`、`strictQualityReportEligible=false`，失败指标/缺源及非完整配对为 null/未知，不作零文献。严格入口不能消费该审计。

此 partial 入口要求所有计划行完成、完整保存的响应（含 HTTP 429 零字节原体文件）或冷却阶段零 HTTP 失败；无体传输失败、读关错误、截断、aborted 等仍明确拒绝，不是任意失败均能出诊断或自动恢复。不得将诊断当科研审批、完整质量验收或扩大请求授权。每份分离标注封存后才开组间对照，分歧另存协调稿而不覆盖原标签；第二代理标注也不等于专家或严格双盲。

```powershell
python scripts/analyze-literature-quality.py --capture-dir '<原捕获目录>' --query-audit '<新离线回放.json>' --prepare '<新标注包.json>'
# 逐条人工/独立评审标注并封存后，再打开组间对照；脚本不推断科学标签。
python scripts/analyze-literature-quality.py --capture-dir '<原捕获目录>' --query-audit '<新离线回放.json>' --labels '<封存标签.json>' --out '<新分析.json>'
```

标注包省略组别，但保留来源元数据，不称严格双盲；标签绑定协议、捕获及包哈希。参与者学段不能从作者机构猜测，缺摘要/干预不明保留待核实；写作表现、修订、过程、体验及综述不能混成已证实的能力效果。分析核对 ordinal 原体、成功槽 fresh HTTP、完整来源审计及标注字段与逐槽 raw 原链回放一致；v2 额外核对协议顺序、预检与实际派发参数，零 HTTP 失败行保留，trace 只匹配实际尝试子序列。v1 没有现场参数，离线回放不能补造原 wire 证据；v2 记录的也只是客户端传输边界，不能证明来源收到或正确执行了过滤语义。

v2 报告明确区分 `sourceHttp`（生产器声明的公开请求）与 `dispatchHttp`（总派发尝试）；fixture 报告前者为 0，并显著标注非真实质量实验。`evidenceMode` 由入口声明、不是独立网络来源认证，哈希也不证明内容可信；手工改写捕获及派生哈希不可能靠当前格式识别为可信运行。锚点首屏缺失不当数据库缺失；输出拒绝覆盖，重标/重算另存版本。

### 来源查询编译与审计回归

先运行不需要模型/公网的语法诊断及真实连接器本机 HTTP / 临时 SQLite 夹具：

```powershell
$env:GOARCH='amd64'; $env:CGO_ENABLED='0'; $env:GOTOOLCHAIN='local'; $env:GOPROXY='off'; $env:GOMAXPROCS='2'
go test -p 1 ./internal/app/research -run 'TestRanked|TestSourceQuery|TestInvalidRanked|TestOldSourceQuery|TestSourceProjection' -count=1 -v
go test -p 1 ./internal/app/workflow -run 'TestReportSourceProjection|TestSourceAttemptKeys|TestLiteratureSourceFailureIsNotCleared' -count=1 -v
go test -p 1 ./internal/bootstrap -run '^(TestSourceQueryProjectionHTTPAndSQLiteRestart|TestArXivInvalidPlannedQueryDoesNotDispatch|TestNativeYearQueryHTTPMatchesPersistableAudit)$' -count=1 -v
```

每条命令检查 `$LASTEXITCODE`；测试完成后恢复本进程原有环境设置。上述诊断语料的标签是预期查询字符串、拒绝与审计限制，不是论文相关性、金标准召回或模型科研验收。夹具核对实际连接器请求参数、闭/开出版年范围、无效输入零请求及 SQLite 重启保全；不启动正式任务。可另以 `go test -p 1 ./internal/app/research -run '^$' -fuzz '^FuzzRankedQueryProjection$' -fuzztime=10s -parallel=2` 做有界语法健壮性检查；模糊测试通过也不证明检索语义正确。

契约和历史兼容边界只见[来源说明](NETWORK_BROWSER.md#文献来源与能力契约)，本轮已执行结果及证据只见[开发进度](PROGRESS.md#4-验证结果与边界)。对新的查询质量做比较必须重新捕获新请求并预先固定问题/预算；旧响应离线重放不能证明新式召回提升。

### 题录身份、快照与归档回归

本地夹具不需要模型授权；只连接临时 HTTP、临时 SQLite 和独立项目，不读正式材料。这些是可复用命令，不构成真实科研任务/公开 API 的执行授权；已执行结果只见[开发进度](PROGRESS.md#4-验证结果与边界)。

```powershell
go test -p 1 ./internal/app/research ./internal/storage/sqlite -run 'TestRecordIdentity|TestCandidateAliasesReject|TestNormalizeWorksPreserves|TestRecordGroupingConflictWitness|TestResearchNewRecordsRequire' -count=1 -v
go test -p 1 ./internal/app/workflow -run 'TestIdentityAwareScreening|TestIdentityAudit|TestGroupingV1History' -count=1 -v
go test -p 1 ./internal/bootstrap -run '^TestRecordHintsV1RealAdaptersHTTPScopedImportAndRestart$' -count=1 -v
```

各条命令后检查 `$LASTEXITCODE`，失败时保留日志而非自动扩大范围。公开题录回放 `TestPublicCapturedIdentityExamplesReplayWithoutMergingOrPromoting` 默认 skip，仅在显式将 `SCIAIDE_IDENTITY_CAPTURE` 指向已有公开 `capture.json` 时读取；它不发新 HTTP、不取全文或运行模型，结束后恢复/清除该变量。身份/类型规则和有界清单指纹只维护在[来源契约](NETWORK_BROWSER.md#文献来源与能力契约)及其代码入口，不把夹具的疑似关系升级为专家或真实模型验收。

## 唯一生产构建入口

遵守 [AGENTS.md](../AGENTS.md)：

1. 构建前检查 `build/bin/SciAide.exe` 是否运行，以**完整可执行路径**匹配，而非只看进程名。
2. 先尝试正常关闭；未退出时，仅终止确认属于该完整路径的进程。不要结束其他目录的同名程序。
3. 运行 `scripts/build-release.ps1`；它不代替前面的进程关闭，也不代替测试。
4. 覆盖原 `build/bin/SciAide.exe`，核对 PE Machine `0x8664` 和 `SciAide.exe.sha256`；不生成带版本名、修复名或备份名的 EXE。
5. `build/bin` 仅留当前程序与必要配套文件。编译成功不等于发布完成；提交、推送、Release 另行处理。

发布脚本先核对压缩 Skill，再通过 [patch-wails-runtime](../scripts/patch-wails-runtime/main.go) 生成 `.tmp/wails-ui-runtime/go.work`：仅将锁定 Wails 的 Windows 页面重载改为 UI 线程原生导航，避免依赖已崩溃页面内的 JavaScript。脚本不修改共享 module cache 或 `go.mod/go.sum`；补丁无法精确应用时必须复核，不模糊替换。

因此直接 `wails build` 不等价于项目正式构建。复核派生运行时可执行：

```powershell
$previous = $env:GOWORK
try {
    $workfile = go run ./scripts/patch-wails-runtime
    if ($LASTEXITCODE -ne 0) { throw 'Preparing Wails runtime failed' }
    $env:GOWORK = $workfile.Trim()
    go test ./... -timeout 20m
    if ($LASTEXITCODE -ne 0) { throw 'Derived runtime tests failed' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Derived runtime vet failed' }
} finally { $env:GOWORK = $previous }
```

该修复不自动重跑科研任务，也不保证恢复已退出的整个宿主进程。

## 默认 Skill 维护

- `internal/opensciskill/defaultskills/` 是可审阅派生树；manifest、转换与能力审计和 ZIP 必须一致。
- `scripts/sync-openscience-skills.ps1 -Check` 校验；`-Update` 是显式更新，不作为启动动作。
- `go run ./scripts/pack-skills -check` 核验嵌入 ZIP。压缩只改变存储，不裁剪方法、正文、脚本资源或许可证。
- 测试专用路由评测与源文件校验清单不进入生产嵌入；运行时需要的 `capability-audit.v1.json` 仍随 ZIP 保留，不能误删。默认包更新后要验证发现、加载、分页、物化和哈希，不只比较 EXE 大小。

## 文件归属与提交

| 位置 | 用途 |
| --- | --- |
| `docs/` | 当前架构、开发指南、进度与必要发布说明 |
| `examples/` | 可复现案例、输入材料及分离的人工核对参考 |
| `contracts/` | 版本化数据契约 |
| `build/` | Wails 构建配置与产物；`build/bin/` 被忽略 |
| `.tmp/` | 诊断、捕获、临时数据库、构建回放、截图和修改前备份；被忽略 |
| `.sciaide-dev/` | 隔离开发数据；被忽略 |

不要提交运行数据库、日志、密钥、`node_modules`、`frontend/dist` 或生成绑定。迁移采用新编号并保留既有校验和；不要为清理“旧设计”删除历史迁移或改写用户冻结任务。修改权限、外部协议或存储语义时，同步更新对应专题、回归测试和 [进度](PROGRESS.md)。
