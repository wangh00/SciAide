# ADR-0007：P1 首个模型 Provider 为 OpenAI-compatible

- 状态：已采纳
- 日期：2026-08-12

P1 只实现一个稳定的 OpenAI-compatible 纵向闭环：`POST {base_url}/chat/completions` 且 `stream=true`。

Adapter 解析 SSE，规范化文本、Usage、FinishReason 和完成事件。错误映射为稳定代码：`MODEL_AUTH_FAILED`、`MODEL_NOT_FOUND`、`MODEL_RATE_LIMITED`、`MODEL_TIMEOUT`、`MODEL_UNAVAILABLE`、`MODEL_STREAM_INVALID`。

429、5xx 和临时网络错误只在尚未成功建立内容流时最多重试两次，采用指数退避、抖动及 `Retry-After`。流建立后的读取错误不自动重试。

连接测试调用 `{base_url}/models`。自定义 Header 仅允许非敏感值；Authorization、API-Key、Cookie、Token 及 CRLF 注入会在应用层被拒绝。

模型配置页可在保存前使用当前 Base URL 和临时输入的 Key 请求模型列表，也可对已保存 Profile 使用 SecretStore 中的 Key。模型列表只在当前界面内存中展示，不写入数据库；服务未实现 `/models`、返回空列表或协议不兼容时，用户仍可手动填写 Model ID。
