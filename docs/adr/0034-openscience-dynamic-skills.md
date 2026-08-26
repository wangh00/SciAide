# ADR-0034：采用 OpenScience 动态 Skill 目录与模型语义路由

- 状态：Accepted
- 日期：2026-08-24

## 上下文

SciAide 原 P4 Skill 体系使用 `skill.yaml`、应用自带的少量版本化包、项目绑定、确定性 trigger 和 Run 启动前的依赖/冲突协调。该方案能审计固定包，但领域覆盖有限；Skill 数量增长后，预选和预注入正文也会持续占用上下文，并让 SciAide 形成一套与 OpenScience 不同的包格式和维护流程。

OpenScience 已提供按领域分类的 `SKILL.md`、references、assets、scripts、语义目录和模型按需加载约定。迁移需要同时满足本地优先覆盖、历史 Run 可复现、第三方内容不扩大权限，以及发布包不依赖用户目录。上游源码目录没有可验证的 Git 元数据，因此本次导入不能伪造 commit SHA；一致性以逐文件路径、字节和 SHA256 校验记录为准。

## 候选方案

1. 保留旧 P4 机制，只继续增加 SciAide 内置包和 trigger。
2. 同时运行旧 P4 与 OpenScience 两套新 Run 路由，由模型或用户选择体系。
3. 新 Run 统一采用 OpenScience 动态目录；旧实现仅保留历史数据和归档兼容。

## 决定

1. 选择方案 3。OpenScience 默认 Skill 树连同 `LICENSE`、`NOTICE` 作为只读资源嵌入 EXE；`defaultskills.manifest.json` 固定 OpenScience `2.0.31` 的 1,624 个路径、大小和 SHA256，`sync-openscience-skills.ps1` 默认只检查漂移，显式 `-Update` 才经暂存验证替换。默认资源不会解包到用户目录，也不依赖运行目录中的 `config.json`。
2. 目录来源优先级固定为 `project > user > installed > default`。Project 来源扫描 Workspace 的 `.openscience/`、`.synsc/` 和 OpenScience 配置中的相对 `skills.paths`；User 与 Git Installed 分别使用 SciAide 数据根的 `user-skills`、`installed-skills`。同名高优先级包整体覆盖低优先级包。当前明确不扫描或兼容 `.claude/skills`。
3. 新 Run 首次模型请求只获得不可信的有界目录元数据、分类和最多 16 个重排候选；召回层最多保留 20 项。候选综合当前消息、最近三条用户任务、中英文词/n-gram/科研别名和最近真实加载项，模型仍决定任务是否需要 Skill，并通过 `builtin.skill.load` 按名称加载。`/skill` 只插入 `Use the <name> skill:` 显式选择文本，不直接注入正文。
4. `builtin.skill.load` 第一次成功加载时重新校验当前包哈希，并原子写入 `run_dynamic_skills`：Run/Project/ToolCall、加载顺序、名称、来源、分类、内容 SHA256、包 SHA256 和完整指令快照。同一 Run 再次加载同名 Skill 必须与快照完全一致；磁盘包在此期间变化时失败关闭，不能组合新正文与旧 provenance。
5. Skill 正文通过普通 ToolResult 进入现有 Agent Loop。短正文完整返回；长 Markdown 先返回有界章节目录，再以稳定 `section-N` 按章节读取，完整正文仍作为不可变 Run 快照保存；无标题长文保留字符分页兜底。references、assets 和 scripts 只有在该 Skill 已加载后，才能先经 `builtin.skill.resource.list` 查看路径、分类、大小、媒体类型和文本可读性，再由 `builtin.skill.resource.read_text` 以包内相对路径、有界字节数读取 UTF-8 文本。SciAide 不执行 Skill 脚本。
6. `allowed-tools` 是能力声明而非授权。加载时将它与实时 Tool Registry 取交集，返回 `available`、`mcp_available` 或 `missing` 诊断及匹配工具；每次实际调用仍完全使用匹配工具自己的风险和权限定义，Skill 不能注册工具、授予权限或绕过 Registry、Policy、审批和 PathGuard。
7. 新 Run 的召回输入由“当前一句英文词”扩展为当前消息、最近三条用户任务和同一 Conversation 最近实际加载的动态 Skill。请求和 Skill 的 `name/category/description/tags/routing-aliases` 通过同一中英文概念词典对称归一化，英文执行受控词形归一化，明确否定的任务概念直接排除对应候选；未知领域可由包作者在 `routing-aliases` 中显式声明中英文说法。召回最多保留 20 项、向模型暴露 16 项再由主模型重排；最近 Skill 只提供连续性证据，不会自动继承或加载。显式 `/skill-name` 或 UI 插入的 `Use the <name> skill:` 保持最高优先级。路由提示只在 Run 首次进入时构造，并以 SHA256 校验的不可变快照绑定到 Run；工具续轮和审批恢复只读取该快照，不重新读取变化中的目录。
8. 启用策略按 Skill 名称持久化；禁用项不进入语义候选且不能加载。Git 安装使用无凭据的受限克隆、静态安全审查和固定 commit SHA；带警告的同一 SHA 必须由用户二次确认，卸载进入可恢复归档。User Skill 只允许受校验的 `SKILL.md` CRUD。管理页可浏览当前生效包的规范相对目录树和只读文件内容；文本读取限制为 2 MiB，二进制与超大文件只显示元数据，磁盘包在目录发现后变化时失败关闭。该用户查看能力不等于模型已加载 Skill，也不会放宽 Run 资源读取或脚本执行边界。
9. 新 Run 不再创建或读取旧 `run_skill_contexts/run_skills`。已有旧 Run 只读恢复其不可变旧快照，不能混入当前动态目录；`internal/app/skill` 只保留历史 DTO、严格解码/哈希校验、上下文渲染和加载器，`internal/skillpkg` 只保留退休包归档辅助函数，SQLite 兼容 Repository 只保留历史读取及归档恢复所需的幂等写入。旧安装、卸载、版本选择、trigger、依赖/冲突协调、回滚和 PackageStore 已删除；历史迁移与归档字段不改写。动态 Skill provenance 随 Artifact 和无密钥项目归档保存，但归档不携带或激活 Skill 源码。
10. 路由提示与结构化审计在同一事务写入：审计保存候选分数组成、否定、连续性 Run、显式选择、短名单和输入 SHA256，不复制用户原文；实际动态 Skill 加载结果在读取时关联，候选不可变字段由独立 SHA256 检测篡改。版本化 320 条中文、英文、混合、否定和无需 Skill 评测作为门禁。
11. 311 个默认 Skill 必须全部进入版本化能力审计，状态为 `native`、`requires_dependency`、`requires_external_service` 或 `unavailable`；第三方覆盖默认为 `unreviewed`。审计锁定包哈希并逐项记录 Tool、Python/CLI、外部服务与缺口，运行时再与 ToolRegistry 求交集。等级只披露已知运行依赖并限制错误承诺，不授予工具、凭据或脚本执行权限。

## 理由

- 一个运行时只有一套新 Run 语义，避免双路由重复注入、名称冲突和不可预测的优先级。
- 按需加载使 311 个默认 Skill 可以被发现，而完整正文只在实际需要时消耗上下文。
- 四级覆盖允许项目定制和用户扩展，同时保持默认库随发布包可用。
- 首次加载快照把复现边界放在真正进入模型上下文的时刻，磁盘变化不会改写历史 Run。
- 脚本只读和既有工具权限链保持了 OpenScience 资料价值，但不把第三方仓库变成隐式代码执行入口。

## 负面影响

- 语义候选依赖词法重合，中文请求或领域别名可能没有 shortlist；模型需要通过分类目录继续发现，显式 `/skill` 仍是确定入口。
- 311 个默认包显著增加 EXE 体积和构建时间；上游更新必须重新执行完整树一致性检查和许可证审查。
- 旧 P4 表和类型暂时不能删除，维护者需要区分“历史回放”与“新 Run 动态加载”。
- OpenScience Skill 中描述的脚本不会自动运行；涉及计算的工作仍需现有受控 Tool/MCP 或后续运行时能力。

## 重新评估条件

- 上游 Skill 格式、许可证、目录结构或语义路由契约发生不兼容变化。
- 实际评测显示当前词法 shortlist 对主要中文科研任务召回不足，需引入本地 embedding 或模型目录浏览协议。
- 需要兼容 `.claude/skills`、签名市场、自动依赖安装或受控脚本运行时；这些能力必须另立安全边界和 ADR，不能隐式扩展当前文本加载机制。
