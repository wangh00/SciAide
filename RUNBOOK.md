# 开发操作手册

更新：2026-10-30。这里只保留有依据的操作边界；命令均为步骤说明，未执行不代表成功。

## 接手任务

1. 读取 [AGENTS.md](AGENTS.md)、[PROJECT_INDEX.md](PROJECT_INDEX.md) 和 [NOW.md](NOW.md)。
2. 按任务路由读取必要专题，不默认扫描全项目。
3. 修改前查看目标章节和 `git status`；保留用户修改，不使用 reset 或全量格式化覆盖成果。

## 隔离开发数据

依据：[docs/development.md](docs/development.md)。推荐在隔离数据根运行：

```powershell
$env:SCIAIDE_HOME = Join-Path $PWD '.sciaide-dev'
.\scripts\dev.ps1
```

启动命令的当前验证范围见[开发进度](docs/PROGRESS.md)，不要以本节示例推断原生 UI 已验收。

## 检查与构建

- 前端类型检查和测试、`go vet`、Go 测试及构建入口见 [docs/development.md](docs/development.md)。
- 正式构建必须先按完整路径检查并关闭 `build/bin/SciAide.exe`，再使用 `scripts/build-release.ps1`；只覆盖原 EXE，更新 `.sha256`。
- 构建成功、测试通过、安装验证、真实模型验收和发布授权是五种不同状态，必须分别记录。

当前测试与构建身份统一记录在[开发进度](docs/PROGRESS.md)，不在操作手册重复维护结果。

## 科研任务核验

1. 先读 [NOW.md](NOW.md) 判断当前任务是否已有完成结果。
2. 结果核验优先读取报告、独立审查和交付门禁的当前冻结记录；不要默认重跑。
3. 真实模型验收使用 `examples/research-cases/README.md` 的步骤，并把自动化夹具结果与真实模型结果分开。
4. 开发代理需自行试跑新案例时，使用[科研流程调试入口](docs/development.md#科研流程调试入口)。它只操作独立调试根，通过原 Facade 执行；先固定案例输入、稳定 requestId、模型请求上限和会话截止，再显式推进问题/审批/路线。不将其指向正式数据库，不用它自动恢复原任务。
