# ADR-0019：P4 Run 级 Skill 渐进注入与不可变快照

## 状态

Accepted（2026-08-14）

## 背景

P4.1/P4.2 已能校验、安装和按项目启用 Skill，但“项目已启用”不能等价于“把所有 `SKILL.md` 永久塞入每次模型请求”。后者会挤占科研资料上下文，并使审批恢复时读取到升级、卸载或被篡改后的另一份指令。

本设计对照了 Codex 当前的 `codex-rs/skills` 与 `codex-rs/ext/skills`：每个用户 Turn 使用固定的 Skill 快照，常驻上下文只包含有界 catalog，显式选择后才读取正文，正文以 contextual user fragment 注入，Skill 不会因此获得更高权限。

## 决策

1. 项目启用只表示 Skill 可以进入本项目的 Run catalog，不表示每轮自动加载正文。
2. 每个新 Run 只根据该 Run 的用户消息选择 Skill：`$skill-id` 为显式选择；`activation.mode=suggest` 仅按 Manifest 中的确定性 trigger 匹配。上一 Run 的选择不会自动沿用。
3. catalog 按项目优先级稳定排序，预算为已知上下文窗口的 2% 且最多 20K；200K 默认窗口对应 4K 的保守 Token/字符预算。优先保留 ID/名称，再使用剩余预算放描述，超出项明确计为 omitted。20K 上限同时保证 catalog 文本及其结构化审计副本不会突破 SQLite 快照的 512 KiB 上限。
4. 只读取被选中的 `SKILL.md`，并在读取时再次核对 Manifest、正文和全包 SHA256。正文完整注入，不截断半份指令；单个正文仍受 Manifest 预算限制。
5. 一个 Run 最多注入 8 个 Skill，正文总量不超过上下文窗口的 20%，且上限为 40K。显式选择超限时在首次模型请求前失败；自动建议按项目优先级跳过超限项并记录数量。
6. catalog 和选中正文都作为低优先级 contextual user message 注入。catalog 位于稳定上下文区；选中正文位于历史消息之后、本轮用户消息之前，保持 Codex 的“当前 Turn contextual fragment”时序。固定 system 规则明确 Skill 只是任务指导，不能授权工具、切换权限模式、读取密钥或绕过审批。
7. 首次模型请求前，使用一个 SQLite 事务同时写入 `run_skill_contexts` 和查询友好的 `run_skills`。快照包含有界 catalog、完整选中 Manifest/正文、选择原因、优先级、安装包/来源归档定位及三类哈希，并用独立 SHA256 防止意外损坏。
8. 同一 Run 的模型工具循环复用内存中的同一快照；审批 Resume 只读取持久化快照，不重新读取项目启用关系或安装目录。已有快照只接受字节一致的幂等写入。
9. Skill Manifest 的 `permissions` 仅作声明和审计，不进入授权决策。所有脚本、MCP 和内置工具仍必须通过 ToolRegistry、参数 Schema、Workspace 边界、Plan/Full Access 和 ToolExecutor。
10. 坏包在目录刷新阶段独立隔离；未被选择的 Skill 正文不会被读取。被选择包若在首次使用前变化，则阻止该 Run 使用不可信内容。
11. 显式 `$skill-id` 若未知、未为项目启用或当前不可用，不得静默忽略；状态写入同一不可变快照并以有界提示交给模型继续回退。catalog 发生省略时必须在可见 catalog 中保留 omission marker。
12. Codex 风格导入是“导入后归一化为 SciAide 包”，不是直接信任任意主机路径。选中 `SKILL.md` 引用的文本资源通过 `builtin.skill.resource.read_text` 渐进读取：工具只接受 `run_id` 隐式绑定的 Skill ID 和包内相对路径，校验 Run 快照与包/来源归档哈希，并继续经过 ToolRegistry、Plan/Full Access 和 ToolExecutor。脚本不会因此自动执行。

## 结果

- 上下文不会因项目启用大量 Skill 而无界增长。
- 工具调用、审批暂停和后续模型轮次看到完全相同的 Skill 指令。
- 项目配置变化、包替换或卸载不会改写历史 Run 的行为和审计证据。
- 常见 Codex Skill 的 `references/` 文本资源可按需读取；`scripts/` 仍只能在后续注册为受控 Tool，不能把 Skill 包变成任意文件或进程入口。
- 当前阶段尚不提供 Skill 管理 UI、文件 watcher、在线市场、签名发布者或内置科研 Skill；它们分别由 P4.4、后续生态能力和 P4.5 处理。
