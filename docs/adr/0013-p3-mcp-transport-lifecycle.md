# ADR 0013：P3 MCP Transport、生命周期与权限边界

- 状态：Accepted
- 日期：2026-08-13

## 背景

SciAide 需要接入本地 stdio 与远程 Streamable HTTP MCP Server，同时不能让外部 Tool、Resource、Prompt 绕过 P2 已建立的权限和审计边界。

## 决策

1. 使用 `github.com/modelcontextprotocol/go-sdk v1.7.0` 处理 MCP 协议、initialize、分页发现和通知，不自建 JSON-RPC 方言。
2. Server 配置由应用层 `mcpserver.Service` 管理，SQLite 只保存非敏感配置、状态与 SecretRef；Secret 明文保存在原生 SecretStore。
3. stdio 使用 `exec.Command(command, args...)`，不通过 Shell；只继承最小环境 allowlist，再注入显式非敏感 Env 与 SecretEnv。
4. 远程 Endpoint 默认必须是 HTTPS；开发用 HTTP 只接受 localhost/回环 IP，敏感 Header 不进入普通配置。
5. MCP 工具名固定为 `mcp.<stable_namespace>.<sanitized_original_name>`。同一 Server 内清洗后重名时整次注册失败，旧命名空间保持或被安全卸载。
6. MCP Tool Adapter 固定携带 `tool.invoke`、moderate、non-idempotent 定义，通过 ToolRegistry → JSON Schema → Plan/Full Access → ToolExecutor，不能由 MCP 描述降低风险。
7. Tools、Resources、Prompts 在连接后发现；列表变更通知触发重新发现与原子工具替换。Resource/Prompt 不自动加入对话。
8. initialize 成功后才将 Server 标为 ready。异常断开会卸载工具并同步 failed；显式 Disconnect 会先从 Manager 移除会话，避免监控协程把正常断开误报为失败。
9. 应用启动时恢复陈旧的 starting/initializing/ready/degraded/stopping 状态；P3 不自动启动 Server，避免未经当前用户动作启动本地进程或建立远端连接。
10. Windows GUI 进程启动 stdio MCP 时设置隐藏窗口与 `CREATE_NO_WINDOW`，控制台不是用户交互或生命周期入口；stdio 管道、显式断开和应用退出仍是连接管理边界。
11. 当前“连接并启用”建立本次应用生命周期内的正式连接并注册工具，保存配置本身不启动 Server。应用正常退出时统一关闭 MCP；重新启动后保持 disconnected，不能把历史 ready 状态当作活连接。
12. MCP Tool Schema 必须在构建模型请求前可用，因此不能等模型已经调用未知工具后才启动 Server。未来自动连接应发生在 Agent 上下文构建前，并仅面向已启用、已信任且由用户选择自动连接的 Server；不能把“保存配置”解释为允许自动执行外部命令。
13. 批量连接/断开仍复用单 Server 的验证与生命周期路径。批量连接只接受已启用、已显式信任且当前未连接的 Server；采用三路有界并发，单项失败不阻断其他项，结果按请求顺序返回。批量断开同时覆盖已建立和正在建立的连接，不能因批量入口绕过信任边界或残留 pending 进程。

## 后果

- 优点：协议实现可升级；MCP 与 builtin/未来 Skill Tool 共用一个安全执行路径；配置和运行状态可恢复。
- 代价：MCP Tool 被保守视为非幂等中风险，无法自动推断真实副作用；P3 暂不读取 Resource/Prompt 内容。
- 后续：统一 NetworkClient 后补足 DNS rebinding、代理与重定向逐跳检查；Windows 发布加固阶段评估 Job Object 进程树托管。
