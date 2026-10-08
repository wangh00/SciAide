# 本轮准备验证记录

2026-09-30，验证对象为案例包和本地生产解析/预检代码，不是模型生成的科研成果。

## 已完成

- 公开下载三篇 PDF，pypdf 检查题名、作者、版本和页数；Poppler 渲染并查看三篇首页、XGBoost PDF 第 4 页及 CatBoost 第 8 页。没有重写 PDF。Poppler 发出本地部分字体未配置警告，所看页面仍可读；它不代表所有页公式均已人工核对。
- SciAide `internal/document.Parse` 实际解析全部 10 件材料（3 PDF、3 逐页文本、2 CSV、1 说明、1 Python）。均有非空文本、无截断。原 RF PDF 有 1 个空白页；XGBoost/CatBoost 分别有 532/489 个未映射字形警告，已明确保留原件和派生文本局限。机器记录：`material-parser.json`。
- 从当前源码提取并直接运行 `dynamicDataPreflightCode`，两份逗号版 CSV 均为 14 列、1000/1599 行、0 缺失，检查覆盖全文件。机器记录：`data-preflight.json`。
- 校验原 ZIP 内红/白数据 1599/4898 行；每个派生红数据值逐一对应原件，没有造样本或改测量。初版等于全量首 1000 行，ID/split 稳定，同特征组合不跨训练/测试。
- Python 3.12.14 / NumPy 2.3.5 实际计算初版和全量的均值基线、OLS、Ridge。OLS 用 lstsq 与 normal equations 互检，Ridge 用参考 normal equations 与 augmented least squares 互检（预测 atol/rtol=1e-9），重复干净拟合通过。含全量重拟合在初版共同 test 上的检查。数值：`reference-results.json`。
- ZIP 回读与逐文件 SHA256 校核由 `tools/package_cases.py` 执行。`SHA256SUMS.txt` 不包含它自身；不要把完整包一次性上传给模型，内有人工核对答案。

## 没有完成或没有声称

- 没有 SciAide 真实模型执行、商业 API 调用或原生窗口端到端测试。没有通过这次准备为此前的 HTTP 模型夹具增加“真实科研通过”的结论。
- 没有检查所有论文每一页的公式/表格，也没有宣称逐页提取稿解决了所有字体问题。
- 没有复现三篇论文的训练实验或 Cortez 2009 指标。文献任务和线性基线任务是两条测试旅程，材料真实不意味着最终模型科学解释一定正确。
- 没有修改产品代码、正式用户数据库、配置或模型密钥；没有重新编译/关闭 SciAide。

## 本地重放（从仓库根目录）

```text
python examples/research-cases/verification/replay.py
python examples/research-cases/verification/check_preflight.py
go run ./examples/research-cases/tools/check_materials.go
```

第一条需 NumPy，第二/三条需要保留 SciAide 源码；Go 可使用现有模块缓存。本轮用 bundled Python，不等同于用户项目 Python 环境。`prepare_data.py` 能重建 CSV；`prepare_sources.py` 会重新生成文本时间戳及 manifest，重建后再运行 package 脚本更新哈希，不修改原 PDF。
