# ADR-0023：项目级知识索引基线

状态：P5.1 基线已由 ADR-0024 的 P5.2 FTS5/BM25 方案演进；本文保留历史决策与兼容边界。

## 上下文

P4.6 已能把用户明确导入的 PDF、DOCX、XLSX 和文本文件解析为带页码、段落、Sheet/行号的 `document.Unit`，但 `builtin.document.search` 一次只能搜索一个附件。P5.1 需要跨多篇项目文献统一检索，同时继续满足项目隔离、大体积缓存跟随 Workspace、索引可删除重建和程序中断可恢复。

## 决定

1. 只有当前项目中状态为 `ready` 的 Attachment 可以进入知识索引。不会扫描普通 Workspace、其他项目、聊天记录、Skill 或 MCP 文件。
2. 全局 `~/.sciaide/data/sciaide.db` 只保存 `knowledge_documents`、`knowledge_import_jobs` 和 `knowledge_index_versions` 管理元数据；Chunk 正文保存在 `<Workspace>/.sciaide/cache/knowledge/index-vN.db`。
3. P5.1 的一个 Knowledge Document 对应一个不可变 Attachment。该边界保留后续把多个附件版本归并为同一逻辑文献的能力。
4. P5.1 使用 `unit-v1` 分块：一个已解析 `document.Unit` 对应一个带内容 SHA256 的确定性 Chunk，保留原始 locator。更细粒度的语义分块由 P5.2 通过新 IndexVersion 引入。
5. ImportJob 使用 `queued → running/loading → chunking → indexing → completed` 状态。启动时遗留 `running` 任务回到 `queued`；上下文取消时任务回队，不把半完成索引标记为可用。
6. 单篇文档在项目索引数据库内事务替换。先提交项目本地 Chunk，再提交全局完成状态；两者之间崩溃时，恢复任务会按 Attachment SHA256 幂等重建。
7. 项目索引缓存缺失时，首次 `builtin.knowledge.search` 对已有 Knowledge Document 补建索引。缓存是派生数据，删除不会删除附件原件或全局会话记录。附件与知识库成员的显式边界由 ADR-0025 补充。
8. `builtin.knowledge.search` 不接受 `project_id` 参数。ToolExecutor 从当前 Run 推导项目，返回有界排序片段和 Attachment/locator 引用；Plan 模式按当前项目低风险幂等读取处理。
9. P5.1 使用可取消的本地词法排序，不声称已经实现 FTS5/BM25、Embedding、OCR 或最终 Citation 持久化。这些能力必须通过后续版本化索引增加。

## 理由

- 大体积 Chunk 不进入系统盘全局数据库，符合项目数据跟随用户磁盘的约束。
- 项目 ID 同时由全局复合外键、本地索引身份元数据和 ToolExecutor Run 归属校验，避免跨项目混用。
- 附件原件与派生索引分离后，索引损坏或算法升级不影响科研原始证据。
- 持久任务和单文档事务避免导入中断产生可检索的半成品。

## 负面影响

- P5.1 搜索需要扫描项目 Chunk，超大知识库性能不如 FTS5。
- 一个 Unit 可能仍然较长，召回粒度和排序质量需要 P5.2 改进。
- 全局元数据与项目本地索引不是分布式事务，只能通过幂等重建达到最终一致。
- 暂无知识库进度界面和用户主动取消入口；应用退出、Tool 取消和失败恢复已经具备底层语义。

## 重新评估条件

- P5.2 引入 FTS5/BM25 和新版分块时，应构建新 IndexVersion，完成后再切换 ready 版本。
- P5.3 引入 Embedding 时，索引身份必须增加 Provider、Model ID、维度和向量格式。
- 如果项目级 SQLite 在固定大语料性能测试中不达标，再评估独立索引 Adapter，不改变上层知识库 Port。
