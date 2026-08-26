# ADR-0035：P7 本地进程执行边界

- 状态：Accepted
- 日期：2026-08-24

## 上下文

动态 Skill 已能提供科研方法、正文和附属源码，但 SciAide 还没有模型可调用的 Shell/Python 落地能力。直接执行 Skill 脚本、把命令塞进 MCP，或另建绕过 ToolExecutor 的运行路径都会破坏现有权限、审批、取消、审计和 Artifact 来源链。Windows 子进程还可能在纳入 Job Object 前派生后代；只截断读取则可能因 stdout/stderr 管道反压死锁。

## 候选方案

1. 继续要求用户自行配置外部 MCP 执行器。
2. 完整移植 Codex 的进程执行与操作系统沙箱。
3. 借鉴 Codex 已验证的进程生命周期机制，在 SciAide 现有 Tool 管道内实现边界清晰的本机执行器。

## 决定

1. 选择方案 3。首批工具为 `builtin.shell.execute` 和 `builtin.python.execute`，固定为高风险、非幂等，并声明 Workspace 读写及精确 `process.execute` 权限。`Plan` 必须逐次审批；`Full Access` 只跳过审批，不跳过 Registry、Schema、Workspace、输出、审计或进程生命周期边界。
2. Windows Shell 仅支持受信任探测得到的 PowerShell/CMD；Python 仅支持 PATH 上的 Python 3。Python 以参数数组及 `-I -u -X utf8` 执行内联代码或 Workspace 脚本，不经 Shell 拼接。解释器文件版本与 SHA256 进入审计，模型不能提供任意解释器路径。
3. 工作目录、Python 脚本和声明产物必须位于当前项目 Workspace，拒绝绝对路径、`.sciaide`、符号链接、junction 和 reparse point。该检查只约束宿主传入路径；获准命令仍拥有当前用户权限，因此可以主动访问绝对路径、网络和其他本机资源。P7.1 是经授权的本机执行器，不是安全沙箱。
4. 子进程从核心 allowlist 构造环境，不继承完整宿主环境；名称包含 `KEY`、`SECRET` 或 `TOKEN` 的变量一律排除。Shell 不读取用户 Profile。P7.1 不向子进程注入模型、MCP、视觉渠道或 Credential Manager 密钥。
5. Windows 以挂起状态创建进程，先分配带 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` 的 Job Object 再恢复；启动/纳管与应用关闭串行化。超时、取消、应用退出以及根进程正常结束后均终止整个 Job，后台后代不能越过 ToolCall 生命周期。非 Windows 保留独立进程组适配基础，但当前正式发布和验收目标仍为 Windows x64。
6. stdout/stderr 分流并持续排空，默认分别保留 64 KiB；达到保留上限后仍计算完整字节数和 SHA256，进程结束后另设 2 秒管道 drain timeout。正常、非零退出、超时、取消、应用退出和启动失败使用不同终止原因。
7. `process_execution_audits` 记录 ToolCall/Run/Project、工具、解释器路径/版本/SHA256、脚本路径/SHA256、内联命令 SHA256、工作目录、timeout、环境变量名称、PID、退出码、终止原因及输出统计。命令正文不复制到该表；应用启动将遗留 `prepared/running` 标记为 `app_shutdown`，项目归档重映射关系。
8. 只有进程成功退出、ToolResult 显式声明且本轮内容新增或改变的 Workspace 常规文件才登记 Artifact。失败、取消、超时、未声明文件、旧内容或路径逃逸均不进入产物库。
9. Shell/Python 工具调用一旦按会话模式获准执行，默认具有网络访问能力；不再追加逐域名授权、网络白名单或独立联网弹窗。`network.domain` 只用于已知目标的能力声明、界面披露和审计，不阻断普通子进程 socket。依赖安装仍因修改项目环境而使用专门的显式确认，密钥使用继续由 `secret.use` 和具体 Adapter 控制。

## 理由

- 复用现有权限与来源链，避免形成第二套模型可达执行通道。
- Codex 的最小环境、持续排空和 Windows Job Object 顺序解决了常见泄密、死锁及子进程逃逸问题。
- 明确承认本机权限边界，比用 Workspace 路径检查伪装强沙箱更准确，也为 P7.2 的资源治理留下清晰接口。

## 负面影响

- 用户批准的命令可使用当前用户权限访问 Workspace 外路径或联网，不能用于运行不可信代码。
- 默认联网降低了配置和审批成本，也扩大了获准脚本的数据外传风险；Plan 用户应核对完整命令，Full Access 用户承担自动工具调用的网络副作用。
- P7.1 没有 CPU/内存强限制、包锁定、持久 Kernel、网络隔离或依赖安装管理。
- 输出正文有界，完整输出只能通过字节数和 SHA256 审计；需要完整日志时应显式写入 Workspace 文件并声明为产物。
- 非 Windows 的进程组代码尚未作为正式发布目标完成解释器版本与平台验收。

## 重新评估条件

- P7.2 引入项目 Python 环境、持久 Kernel、依赖安装、内存限制或结构化表格/图片结果。
- 需要运行不可信第三方代码、容器、受限 Token/AppContainer 或真正文件系统/网络沙箱。
- 增加 macOS/Linux 正式发布，需补平台解释器身份、进程树和资源限制测试。
