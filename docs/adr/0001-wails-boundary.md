# ADR-001：Wails 与应用核心的边界

- 状态：Accepted
- 日期：2026-08-12

## 上下文

SciAide 使用 Wails 构建桌面 UI，但 Agent、科研服务和存储不应依赖桌面框架，以便测试和替换 Adapter。

## 候选方案

1. 直接把所有 Go Service 绑定给 Wails。
2. 只把最小 Facade 绑定给 Wails，由 Facade 调用 Application Use Case。

## 决定

采用方案 2。Wails 代码限于 Bootstrap 和 `internal/transport/wails`。

## 理由

- 避免前端绕过参数和权限边界。
- Use Case 可在无 WebView 环境测试。
- Wails 升级或替换不会侵入领域逻辑。

## 负面影响

需要维护 DTO 转换和少量 Facade 样板代码。

## 重新评估条件

如果 Wails 提供独立、可生成且具同等校验能力的边界层，可重新评估样板代码。
