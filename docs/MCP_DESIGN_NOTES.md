# MCP 工具展示与状态查询：源码核对

核对日期：2026-09-25。只读分析 `D:/软著/codex-main`，该目录没有 Git 元数据，不能标定提交号；以下不是对线上 Codex 所有版本的保证。

## 本地 Codex 实现

1. 连接管理器收集服务端工具目录，保留原始服务名/工具名及模型可见 namespace/name。允许/禁用工具过滤与应用策略先于模型展示。
2. `core/src/mcp_tool_exposure.rs`：`search_tool_enabled` 为真时注册为 Deferred，否则为 Direct；部分 Agent 插件定义另有体积预算及 Hidden 状态。
3. `core/src/tools/spec_plan.rs:search_tool_enabled`：要求模型 `supports_search_tool` 且 provider 支持 `namespace_tools`，并不是任意兼容 API 都能直接使用相同协议。
4. `core/src/tools/handlers/tool_search_spec.rs` / `tool_search.rs`：工具发现使用 deferred 工具元数据 BM25 检索，返回可加载的名称、说明、参数定义，供后续模型请求使用。明确要求 MCP 工具发现不应使用 `list_mcp_resources`/`list_mcp_resource_templates`；后二者是数据资源目录。
5. `core/src/tools/handlers/mcp.rs` 和 `codex-mcp/src/tools.rs`：模型可见名称映射到原始服务/工具执行，不能靠猜测名称调用。
6. `app-server-protocol/src/protocol/common.rs` 的 `mcpServerStatus/list` 是客户端管理 API。`app-server/src/request_processors/mcp_processor.rs` 汇总运行状态、认证状态、工具、资源、模板及工具目录错误，支持分页。`bespoke_event_handling.rs` 将启动状态更新发给客户端。该 API 不等于内置模型函数。

## SciAide 已实现（2026-09-25）

- 普通自由对话开局提供内置工具和两个发现入口，不一次注入全部 MCP 参数定义。`builtin.mcp.list` 查看配置名称、命名空间、启用/连接状态及当前范围的工具数量/能力例子；不返回 command/env/headers/URL/原始错误，不主动连接或修改配置。名称和描述属于模型可见的非可信元数据，不保证服务端自行写入的任意秘密均可识别。
- `builtin.tools.search` 通过关键词或精确服务器 namespace 查询当前注册工具，单次最多 8 个，可分页。名称/说明使用简单关键词匹配排序，不是语义搜索；中文无结果可按 namespace 浏览。返回名称、短说明和定义指纹；下一轮由宿主把完整 Schema 加入模型工具表，兼容普通 function calling，不依赖 Codex 原生 tool_search。
- 成功结果持久化，只恢复当前 Run 内本地主机搜索选中的工具；同一 Run 已加载工具累计保留，新 Run 重新发现。失败、伪造的第三方结果不能激活工具；同批搜索并猜测调用会拒绝。断连工具消失，定义改变后必须重搜；实际执行继续走原审批和契约校验，发现并不授权执行。
- 科研绑定会话（包括交付后的追问）继续遵循原阶段范围。新路线可查 MCP 状态，但当前自动 AgentStage 的安全模型不允许执行通用 MCP；search 返回受限范围和明确说明，不能因发现而扩权。固定 MCP 节点的原有执行方式不变。旧冻结任务不强行注入新工具。后续要让科研 AI 自主执行 MCP，需单独设计能力声明、阶段授权和审批，而不是放宽所有工具。
- MCP 服务状态、可调用工具、MCP 资源、Skill 文档是不同对象；不应互相充当查询入口。
- 单次搜索限量不等于整个 Run 的工具体积预算；大量持续发现或服务端巨型 Schema 仍有上下文成本，尚未实现自动卸载。

## 本轮 Workspace 修改边界

仅开放 `.sciaide` 已知目录概览，不开放其全部文件；科研任务内相对路径读取不变。管理写入仍走现有专用入口，现有本机 Shell/Python 不具备 OS 级隔离，本轮不声称解决了任意脚本访问控制。
