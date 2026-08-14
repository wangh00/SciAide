# ADR-0016：思考强度运行时协商与能力记忆

- 状态：已接受
- 日期：2026-08-14

## 背景

不同供应商、协议和兼容网关对思考强度的字段、档位及错误格式并不一致。仅依赖模型名称白名单会让新模型被旧客户端错误限制，例如用户选择 `max`，客户端却在请求前静默改成 `high`。后台发送探测对话又会产生额外费用、污染会话语义和用量统计。

## 决策

SciAide 对用户始终暴露统一的五档偏好：

`low → medium → high → xhigh → max`

能力来源按以下顺序处理：

1. `/v1/models` 明确声明的 `supported_reasoning_efforts` 或 `supported_reasoning_levels`；
2. 已知模型族的内置兼容信息；
3. 未知兼容模型按完整五档乐观处理，并等待真实请求验证；
4. 运行时已经验证或拒绝的结果优先于静态推断。

不会发送后台探测请求。未知模型选择 `max` 后，下一次真实对话直接尝试 `max`。

## 协商状态机

```text
用户选择 max
  └─ 真实请求尝试 max
       ├─ 成功：记录 max 已验证
       ├─ 400/422 明确拒绝该值：尝试 xhigh → high → medium → low
       ├─ 400/422 明确拒绝整个控制字段：删除可选参数，使用模型默认
       └─ 其他错误：原样返回，不降档、不改写错误
```

仅允许在流式响应尚未建立时协商。一旦服务端已接受请求并打开响应流，后续中断、网络错误或 SSE 错误都不得重新发送该轮对话。

## 协议映射

- OpenAI Chat Completions：`reasoning_effort`
- OpenAI Responses：`reasoning.effort`
- Anthropic 新式模型：`thinking.type=adaptive` 与 `output_config.effort`
- Anthropic 旧式模型：`thinking.type=enabled` 与 `budget_tokens`

Anthropic 未知模型先尝试 adaptive；若服务端明确拒绝该模式，则在同一档位尝试 legacy。成功的 wire mode 会被记忆，避免同一会话的后续工具轮次重复协商。

## 持久化与失效

运行时观察按 `model profile + API protocol + Base URL + model ID` 隔离，保存：

- 已验证档位；
- 已拒绝档位；
- 是否不支持整个思考控制；
- 最近请求档位与实际档位；
- 实际 wire mode。

同一连接配置重新保存时保留观察结果；协议或 Base URL 改变时清空旧观察，防止跨端点污染。较新的成功会清除同档位的旧拒绝，较新的拒绝也会撤销同档位的旧成功。

## 用户界面

顶部思考强度显示真实状态，而不是请求前猜测：

- `max · 待验证`
- `max · 已验证`
- `max → xhigh`
- `max · 模型默认`

模型设置页区分服务端声明、待运行验证、运行时已验证和模型原生思考。

## 结果

- 新模型无需等待客户端更新白名单即可尝试更高档位；
- 不增加后台请求和额外计费；
- 只对可证明安全的参数拒绝进行降级；
- 已验证结果可复用，并能在连接边界变化时正确失效。
