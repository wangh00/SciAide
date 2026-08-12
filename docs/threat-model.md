# SciAide 威胁模型（P1）

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
| Prompt 注入诱导工具执行 | P2 | JSON Schema、PolicyEngine、Approval、预算 |
| 路径穿越和 junction/symlink | P2 | PathGuard、Workspace 根、逐资源授权 |
| SSRF 和 DNS rebinding | P2/P3 | NetworkClient、地址复查、域名/端口权限 |
| 恶意 MCP 子进程或远端服务 | P3 | 首次信任提示、最小环境、生命周期和权限管道 |
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
