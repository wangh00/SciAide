# 项目映射

更新：2026-10-22。仅记录仓库内可由现有文档确认的关系；未核实的部署位置不在此编造。

| 区域 | 用途 | 入口 |
| --- | --- | --- |
| `internal/` | Go 后端、科研工作流、模型协议、工具和存储 | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| `frontend/` | React、TypeScript、Vite 前端 | [docs/development.md](docs/development.md) |
| `contracts/` | 版本化契约与 Schema | [docs/development.md](docs/development.md) |
| `examples/` | 可复现案例、输入材料和人工核对参考 | `examples/research-cases/README.md` |
| `scripts/` | 构建、测试、Skill 和桌面诊断脚本 | [RUNBOOK.md](RUNBOOK.md) |
| `docs/` | 当前架构、开发、科研方向、进度和发布说明 | [docs/README.md](docs/README.md) |
| `build/bin/` | 当前本地构建产物；按规则只保留当前 EXE 及校验文件 | [AGENTS.md](AGENTS.md) |
| `.tmp/` | 忽略的诊断、捕获、临时数据库和构建证据 | 不作为默认资料读取 |

## 运行与数据关系

- 正式构建对象为 `build/bin/SciAide.exe`，目标架构 Windows amd64；构建和校验规则见 [AGENTS.md](AGENTS.md)。
- 开发数据可通过 `SCIAIDE_HOME` 隔离；全局与工作区数据关系见 [docs/development.md](docs/development.md)。
- 当前没有在本文件记录密钥、个人路径、正式数据库身份或未核实部署节点。
