# ADR-0032：P6 后半段以在线发现到本地可信证据为主线

## 背景

P6.1 和 P6.2 已经建立不可变 Artifact、来源/引用快照与确定性 DOCX/PDF 导出。原路线把项目备份列为 P6 的一个交付物，但不能因此把 P6.3 之后全部缩减为备份和恢复。SciAide 仍缺少科研工作中位于本地知识库之前的文献发现、跨来源去重、书目补全和筛选记录。

OpenScience 的源码提供了可借鉴但不能照搬的边界：一个 Connector 对应一个公开科研数据源，Connector 通过固定的 Catalog/Search/Fetch 工具面暴露；共享 HTTP 层负责超时、取消、限速、重试和缓存；单一用户可见 Research Agent 按需加载 Skill，并只在有界任务中执行 Explore/Review。其 OpenAlex、Crossref、arXiv、PubMed、Europe PMC 和 Semantic Scholar 路径主要使用公开、免 Key 或可选 Key API，不代表与文献提供商存在合作关系。

## 决策

1. P6.3 建立 SciAide 自有的 Connector Port 和固定三个内置 Tool。模型只能选择已注册来源和结构化查询，不能借 Connector 请求任意 URL。所有调用继续经过 ToolRegistry、PolicyEngine、ToolExecutor、取消和审计链。
2. 网络 Adapter 固定来源 Host allowlist，并统一实现外层超时、调用方取消、响应上限、按 Host 限速、有界重试和短期缓存。来源错误、限流和真正零结果是不同状态。
3. 在线命中只是“不可信候选”。候选及原始来源快照可以持久化和显示，但不能直接创建 `message_citations` 或可信 `[K-...]` 标记。
4. P6.4 只有在用户显式纳入后才下载开放全文或生成明确标注的元数据/摘要 Markdown，随后复用既有 Attachment、Knowledge ImportJob、Chunk 和可信 Citation 链。没有全文时不得把摘要附件描述成论文全文。
5. 聚合去重是可逆的：规范 DOI/PMID/arXiv/OpenAlex ID 优先，标题/年份只做保守匹配；每个来源记录和冲突字段都保留。候选的纳入、排除、待定、原因和笔记属于项目事实。
6. P6.5 建立规范书目与字段级来源快照。用户修订必须显式并保留历史；模型不得猜测作者、年份、期刊或 DOI。Artifact 只快照生成当时已经验证或明确标注等级的书目数据。
7. 多 Skill 协调继续沿用 Run 级不可变快照：有界 catalog、确定性选择、依赖/冲突/优先级/总预算检查、少量正文按需加载。P6 不引入递归多 Agent 或完整 Workflow/DAG。
8. 备份/恢复放在 P6.6 收尾。备份包含项目关系和真实对象，不包含任何密钥；导入在隔离暂存中完成 Manifest、路径、大小、SHA256、SQLite 外键和版本校验后才原子发布，默认恢复为新项目。

## 结果

- P6 形成“在线发现 → 筛选 → 本地知识化 → 可信引用 → Artifact → 正式导出 → 项目恢复”的科研闭环，而不是孤立的文档导出或备份功能。
- Connector 数量增长不会扩大模型 Tool Schema；来源权限、网络行为和失败语义保持集中可审计。
- 在线元数据可以帮助发现和补全书目，但只有本地证据链能取得当前可信引用身份。
- Skill 规模增长不会导致全部正文进入上下文；P7 仍有清晰的 Workflow/DAG 与数据分析运行时边界。
