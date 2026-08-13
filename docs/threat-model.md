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
- P2.5 权限入口只有会话级 `Plan` 与 `Full Access`。`Plan` 每个 ToolCall 都请求一次确认；`Full Access` 自动授权已注册并通过参数校验的工具。
- 风险级别只向用户展示，不替用户限制授权；两种模式都不能绕过 Workspace 边界、Schema、超时、取消和结果大小限制。
- Approval 与审计事件同事务持久化；同一 ToolCall 只存在一个 pending Approval，处理过的审批不能重复解析。历史 Grant 数据不再参与决策。
- 启动时 pending Approval 先过期并审计，再中断 ToolCall 和 Run，不自动重放工具。

## P2 ToolExecutor 与 Workspace 只读工具已落实的控制

- Executor 只执行已进入 `running` 的 ToolCall；调用前复查 Run/Project 归属以及 Registry Definition 与安全快照。
- 所有调用具有默认超时、context 取消、同 Call 并发保护和 panic 隔离，内部错误与 panic 内容不返回模型。
- 文本与结构化 Result 有独立大小上限；文本按 UTF-8 边界截断，超大结构化结果失败关闭。
- Workspace 路径拒绝绝对路径、卷标、`..` 越界和兄弟目录前缀；使用 `os.Root` 防止打开时通过符号链接逃逸。
- 内置目录工具不递归且限制条目数；文本工具限制读取字节、拒绝 NUL/非 UTF-8/非常规文件。

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

| Prompt 注入诱导工具执行 | P2 | JSON Schema、PolicyEngine、Approval、预算 |
| 路径穿越和 junction/symlink | P2 | 已实现 PathGuard、`os.Root`、Workspace 根和安全回归测试；写路径仍在 P2.6 |
| SSRF 和 DNS rebinding | P2/P3 | NetworkClient、地址复查、域名/端口权限 |
| 恶意 MCP 子进程或远端服务 | P3 | 已实现首次信任、SecretEnv 隔离、最小环境、生命周期恢复和统一权限管道；Job Object/DNS rebinding 仍需发布加固 |
| Skill 供应链和自动脚本执行 | P4 | 包校验、哈希、显式启用、脚本只注册 Tool |
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
