# ADR-0007：P1 首个模型 Provider 为 OpenAI-compatible

- 状态：已采纳
- 日期：2026-08-12

P1 只实现一个稳定的 OpenAI-compatible 纵向闭环：`POST {base_url}/chat/completions` 且 `stream=true`。

Adapter 解析 SSE，规范化文本、Usage、FinishReason 和完成事件。错误映射为稳定代码：`MODEL_AUTH_FAILED`、`MODEL_NOT_FOUND`、`MODEL_RATE_LIMITED`、`MODEL_TIMEOUT`、`MODEL_UNAVAILABLE`、`MODEL_STREAM_INVALID`。

429、5xx 和临时网络错误只在尚未成功建立内容流时最多重试两次，采用指数退避、抖动及 `Retry-After`。流建立后的读取错误不自动重试。

连接测试调用 `{base_url}/models`。自定义 Header 仅允许非敏感值；Authorization、API-Key、Cookie、Token 及 CRLF 注入会在应用层被拒绝。
