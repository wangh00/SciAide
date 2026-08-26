# ADR-0037：P7.3 派生默认 Skill 的 SciAide 执行边界

- 状态：Accepted
- 日期：2026-08-25

## 上下文

OpenScience 默认 Skill 中既有科研方法，也有上游产品专属的目录、命令、自动凭据、用量上报和托管服务假设。原样嵌入会让模型承诺 SciAide 不存在的能力；彻底删除脚本和资料又会损失可复用的方法与实现。

## 决定

1. 默认 Skill 继续从固定 OpenScience `2.0.31` 源树派生，但发布资源不是运行时上游目录。同步过程保留作者、许可证、NOTICE 和逐文件 provenance，并以 `transformVersion: p7.3-v2` 固定派生结果。
2. 对需要脚本或命令的 238 个 Skill 注入统一执行边界：先用 `builtin.skill.resource.materialize` 将经哈希验证的文件发布到当前 Workspace，再检查内容，最后通过正式 `builtin.python.execute`、`builtin.shell.execute` 或项目 Kernel 执行。
3. 依赖安装必须调用 `builtin.python.environment.install` 并单独审批。Skill 的 `allowed-tools` 只是能力声明，不能授予权限、注册工具或触发安装。
4. 转换检查拒绝上游自动注入凭据、自动计费/用量上报、虚假 `providerClient` 和不存在的 SciAide 托管 API。无法映射的云端能力明确标记为用户配置的外部 MCP/API 或不可用。
5. 311 项能力审计与包哈希绑定，状态只有 `native`、`requires_dependency`、`requires_external_service`、`unavailable`；第三方覆盖仍为 `unreviewed`。当前 v2 结果为 4/200/87/20/0。
6. 同步默认执行 `-Check`；只有人工审阅来源差异后使用 `-Update`，并在暂存树通过路径、数量、许可证、转换和能力审计门禁后替换嵌入资源。

## 结果

- 默认 Skill 的科研知识得到保留，但不存在绕过 Tool 权限链的隐式脚本执行路径。
- 派生树与上游不再逐字节相同，升级必须同时审阅转换器、能力审计和许可证记录。
- “需要依赖”或“需要外部服务”是透明缺口，不代表运行时自动提供包、密钥或付费服务。

## 重新评估条件

- SciAide 增加签名插件运行时、自动依赖解析或新的原生科研 Tool。
- OpenScience 的目录格式、许可证或运行契约发生不兼容变化。
