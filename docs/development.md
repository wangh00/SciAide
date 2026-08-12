# SciAide 开发环境

## 基线

- Windows 10/11 x64（当前开发机）
- Go：以 `go.mod` 为准，必须使用 `amd64`
- Node.js 22 LTS 与 npm 10+
- Wails CLI：与 `go.mod` 中 Wails 主版本兼容
- WebView2 Runtime
- 本项目使用 `modernc.org/sqlite`，`CGO_ENABLED=0`，不需要 GCC

当前机器的 Go 全局环境被设置为 `windows/386`。不要修改用户的全局 Go 环境；使用项目脚本，或在当前 PowerShell 会话执行：

```powershell
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
```

## 安装依赖

开发代理可以按任务需要安装依赖，但必须在阶段总结中报告新增包、版本、用途及锁文件变化。首次手动准备环境时可执行：

```powershell
# 1. 安装 Wails CLI
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go install github.com/wailsapp/wails/v2/cmd/wails@v2.14.0

# 2. 下载并校验 Go 模块，同时生成 go.sum
go mod tidy
go mod verify

# 3. 安装前端依赖并生成 package-lock.json
Set-Location frontend
npm install
Set-Location ..
```

如果依赖安装在自定义位置，请保证以下命令可用，或把具体路径告知开发代理：

```powershell
wails version
go version
node --version
npm --version
```

## 开发与检查

```powershell
# 开发模式
.\scripts\dev.ps1

# P0 质量门禁
.\scripts\p0-check.ps1
```

为验证不会意外联网，可在依赖安装完成后执行离线检查：

```powershell
.\scripts\p0-check.ps1
```

`p0-check.ps1` 已默认设置 `GOPROXY=off` 和 `GOTOOLCHAIN=local`，不会下载 Go 模块或工具链。依赖未安装完整时会直接失败。

## 运行数据

生产默认使用操作系统应用数据目录。开发时可隔离到仓库：

```powershell
$env:SCIAIDE_HOME = Join-Path $PWD ".sciaide-dev"
.\scripts\dev.ps1
```

`.sciaide-dev` 已加入 `.gitignore`，不得提交数据库、日志或密钥。

## 提交要求

1. `go.mod`、`go.sum`、`frontend/package.json`、`frontend/package-lock.json` 同步提交。
2. 不提交 `node_modules`、构建产物、运行数据库和日志。
3. Schema、权限或外部协议变化同时更新测试、`start.md` 和 ADR。
4. 不把 Wails 基础设施对象直接绑定给前端，只绑定 `internal/transport/wails` 中的 Facade。
