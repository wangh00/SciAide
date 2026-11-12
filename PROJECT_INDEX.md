# SciAide 项目导航

> 本文件只负责路由，不复制专题正文。更新：2026-10-30。

## 项目定位

SciAide 是本地优先的桌面 AI 科研助手，围绕自然语言讨论、科研路线、文献证据、数据分析、报告审查和返修组织可追溯工作。当前工作树包含发布后的开发增量；发布能力以对应 Release 说明为准。

## 按任务定位

| 当前问题 | 首选入口 | 继续阅读 |
| --- | --- | --- |
| 了解项目用途或使用方式 | [README.md](README.md) | [docs/README.md](docs/README.md) |
| 接续当前开发工作 | [NOW.md](NOW.md) | [docs/PROGRESS.md](docs/PROGRESS.md) |
| 了解模块边界、数据流和恢复分层 | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | [RISKS.md](RISKS.md) |
| 研究模式产品方向、路线与返修 | [docs/RESEARCH_DEVELOPMENT.md](docs/RESEARCH_DEVELOPMENT.md) | [DECISIONS.md](DECISIONS.md) |
| 本地开发、隔离数据、测试或构建 | [docs/development.md](docs/development.md) | [RUNBOOK.md](RUNBOOK.md) |
| 让开发代理自行试跑科研案例、回答问题和定位阶段错误 | [科研流程调试入口](docs/development.md#科研流程调试入口) | [当前验证结果](docs/PROGRESS.md) |
| 网络、浏览器、MCP 和 Skill | [docs/NETWORK_BROWSER.md](docs/NETWORK_BROWSER.md) | [docs/threat-model.md](docs/threat-model.md) |
| 查看近期验证、构建和未完成项 | [docs/PROGRESS.md](docs/PROGRESS.md) | [NOW.md](NOW.md) |
| 追溯已经归档的设计或发布资料 | [history/README.md](history/README.md) | `docs/releases/` |

## 权威来源约定

- 当前状态、阻塞和下一步以 [NOW.md](NOW.md) 为准。
- 长期产品/架构原则以 [docs/RESEARCH_DEVELOPMENT.md](docs/RESEARCH_DEVELOPMENT.md) 和 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 为准。
- 操作方法以 [docs/development.md](docs/development.md) 和 [RUNBOOK.md](RUNBOOK.md) 为准；执行结果必须另有当前验证证据。
- 发布事实只以 `docs/releases/` 对应版本说明为准；不要用 CHANGELOG 或旧过程推断当前状态。
