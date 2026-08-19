# ADR-0024：FTS5/BM25 与有界知识检索

## 上下文

P5.1 使用 `unit-v1`：一个解析 Unit 对应一个 Chunk，查询时由 Go 扫描项目全部 Chunk。它能证明跨文献、项目隔离和缓存重建链路，但长 PDF 页粒度较粗，文献规模增长后查询成本线性增加。P5.1 的知识工具还会同时把 snippet 写入 Text 与 Structured，导致模型上下文重复。

## 决定

1. P5.2 使用新的 `retrieval_engine=fts5_bm25_v1` 和 `bounded-unit-v2`，通过迁移 25 增量扩展版本元数据。已发布迁移 24 不修改，旧索引默认识别为 `lexical_scan_v1`。
2. 长 Unit 按句号、换行和空白边界拆分，目标 1,200 rune、最大 1,600 rune、相邻重叠 80 rune。Chunk ID 包含 Document ID、分块版本、Unit/locator、源起止位置与内容 SHA256。
3. 项目索引使用 SQLite contentless FTS5。`chunks` 只保存一份规范正文；`chunks_fts` 只保存词项倒排关系，不保存正文副本。
4. 英文、数字、希腊字母和科研标识符按连续字母数字规范化；连续汉字生成双字词项。单字符查询和无法生成词项的查询回退到有界词法扫描。
5. 搜索使用 BM25，标题权重为 6、正文权重为 1，并叠加完整短语匹配。候选结果按文档最多保留 3 个，防止单篇长文档垄断上下文。
6. 新索引以 building 状态在独立 `index-vN.db` 中构建。只有当前项目全部显式 Knowledge Document 均完成、无活动/失败任务、本地 SHA256 覆盖校验通过并完成 FTS optimize/WAL checkpoint 后，才在全局事务中把新版切换为 ready、旧版切换为 retired。
7. `builtin.knowledge.search@2` 默认返回 8 条、最多 20 条；一次模型可见正文总预算为 8,000 rune，单 snippet 最多 900 rune。正文只出现在 Text，Structured 只保留 ID、格式、locator、源范围、排名、分数和命中词。
8. FTS5、分块与排序完全本地运行，不调用模型 API。索引数量和模型 Token 没有直接关系；只有最终有界 ToolResult 进入模型上下文。

## 理由

- 倒排索引把查询成本从遍历全部 Chunk 改为读取命中词项的 posting list。
- contentless FTS5 避免正文重复存储，适合项目本地派生缓存。
- 影子构建保持旧版可用，失败或中断不会暴露半完成索引。
- 有界输出、跨文档配额和按需 `builtin.document.read` 能在控制 Token 的同时保留精读路径。

## 负面影响

- 中文双字词项比纯英文索引产生更多 posting，项目索引仍会比仅保存 Chunk 正文更大。
- 双字词项不是语言学分词，复杂同义词与语义相关性仍需后续 Embedding/混合检索补充。
- v1 到 v2 迁移期间会短暂同时存在两个索引文件；旧版当前保留为回退缓存。
- 当前 Token 预算使用保守的一 rune 一 token 估算，尚未接入协议/模型专用 tokenizer。

## 重新评估条件

- 固定中英文语料的 Recall@K 或 MRR 不达标时，先调整词项、分块和排序，再考虑引入额外依赖。
- P5.3 引入 Embedding 时必须新建 IndexVersion，不能把不同模型或维度写入 FTS5 版本。
- 项目索引磁盘压力测试显示 posting 占用过高时，评估词项裁剪、自动清理 retired 版本和索引压缩策略。
