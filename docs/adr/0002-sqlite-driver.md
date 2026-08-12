# ADR-002：使用纯 Go SQLite Driver

- 状态：Accepted for MVP
- 日期：2026-08-12

## 上下文

当前开发机没有 GCC，且 Go 全局目标误设为 386。桌面发行需要减少 CGO 工具链和跨平台打包差异。

## 候选方案

1. `mattn/go-sqlite3`（CGO）。
2. `modernc.org/sqlite`（纯 Go）。
3. 外部数据库服务。

## 决定

MVP 使用 `modernc.org/sqlite`，项目脚本固定 `GOARCH=amd64`、`CGO_ENABLED=0`。

## 理由

- 不需要本机 GCC。
- 更适合单二进制和跨平台 CI。
- SQLite 足以承担 P0 的本地事务和迁移。

## 负面影响

- 二进制体积和部分性能特征需实测。
- FTS/向量扩展能力要在 P5 前验证。

## 重新评估条件

若性能测试、FTS5 或向量扩展无法满足项目规模，再比较 CGO Driver、sqlite-vec 或独立索引 Adapter。
