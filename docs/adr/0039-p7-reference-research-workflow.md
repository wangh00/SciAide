# ADR-0039：P7.6 以人工检查点闭合参考科研 Workflow

- 状态：Accepted
- 日期：2026-08-25

## 上下文

P6 已分别具备公共数据库、材料导入、知识索引、可信 Citation、Artifact 和正式导出，P7.2 又提供可复现 Python 分析。如果缺少一条可运行的参考流程，这些能力仍难以证明能在应用重启、人工复核和失败恢复下形成一致来源链。

## 决定

1. 内置参考模板固定串联：公共数据库检索、人工候选筛选、材料导入、知识索引、本地证据检索、人工 Citation 选择、项目 Python 环境、Kernel 分析、CSV/SVG Artifact、Markdown 报告、DOCX/PDF 导出。
2. 在线候选不能自动提升为可信来源。候选必须由用户选择并物化为本地材料，经过现有 Knowledge 索引与 Chunk 哈希链后，Citation 再由用户选择并冻结。
3. Python 分析使用冻结环境指纹和结构化输入，声明新的 `analysis-output/` 文件；结果文件经 ToolResult/Artifact 管道登记，复现哈希和上游 ToolCall 进入报告 lineage。
4. 报告节点重新校验每条本地 Citation 和分析文件，发布不可变 Markdown Artifact，并复用 P6 确定性导出器生成 GB/T DOCX/PDF。失败不能留下半报告或伪造来源。
5. 端到端测试必须跨两次应用重启，证明候选决策、Citation、Workflow 检查点、Kernel 结果、Artifact、正式导出和来源关系都可从 SQLite 与对象存储恢复。
6. Run 详情只从冻结的已提交 Step Output 推导环境 Manifest 与产物图谱，不从当前环境、Workspace 扫描或未完成步骤补猜历史证据。

## 结果

- P6 的发现、证据和产物与 P7 的执行、环境和 Workflow 形成可验证闭环。
- 参考模板是可选起点，不是所有用户请求的强制流程；非科研和开放式任务继续使用 Agent Loop。
- 流程保证来源与执行记录可复核，不替代用户对检索范围、证据质量、统计方法和科学结论的判断。

## 重新评估条件

- 增加系统综述、实验设计、数据清洗、投稿或领域专用参考模板。
- 需要 CSL、图表原位排版、协作审阅或远程计算后端。
