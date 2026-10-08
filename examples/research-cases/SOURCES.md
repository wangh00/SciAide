# 来源、版本和材料权利说明

下载及准备日期：2026-09-30。逐文件 SHA256 与字节数见 `SHA256SUMS.txt`，结构化来源信息见 `sources/source-manifest.json`。这两条案例都是公开真实材料，不是用户私有科研项目的代表性验收。

## 论文原件

1. **Leo Breiman. Random Forests (2001).** 原件是作者站点的 January 2001 manuscript，PDF 33 页（含一个空白页），不冒充期刊排版最终版。论文 DOI：[10.1023/A:1010933404324](https://doi.org/10.1023/A:1010933404324)。下载：[作者 PDF](https://www.stat.berkeley.edu/~breiman/randomforest2001.pdf)。
2. **Tianqi Chen, Carlos Guestrin. XGBoost: A Scalable Tree Boosting System (2016).** 本包为 arXiv:1603.02754v3，2016-06-10，PDF 13 页。DOI：[10.1145/2939672.2939785](https://doi.org/10.1145/2939672.2939785)。[arXiv 记录](https://arxiv.org/abs/1603.02754v3) / [下载](https://arxiv.org/pdf/1603.02754)。
3. **Liudmila Prokhorenkova, Gleb Gusev, Aleksandr Vorobev, Anna Veronika Dorogush, Andrey Gulin. CatBoost: unbiased boosting with categorical features.** NeurIPS 2018 论文，本包为 arXiv:1706.09516v5，2019-01-20 修订稿，PDF 23 页，不能将稿件版本日期当成发表年份。[arXiv 记录](https://arxiv.org/abs/1706.09516v5) / [下载](https://arxiv.org/pdf/1706.09516)。**不是**另一个题为 “CatBoost: gradient boosting with categorical features support” 的 arXiv:1810.11363。

原 PDF 未重新排版、删页或添加批注。`.pages.txt` 是 pypdf 按页提取的派生文本，头部记录原 PDF 哈希、版本、提取时间和工具。文本顺序、连字、公式、表格可能失真；关键数值应核对原 PDF，不能把两种载体算作独立文献。

公开可下载不等于自由许可。论文版权保留给原权利人；本包用于本地阅读/测试，不声称论文适用 CC BY。两个 arXiv 记录展示非独占分发许可，而不是对本包授予统一再发布许可；XGBoost 原件第一页有个人/课堂使用的权利说明。若要公开发布项目或案例包，应按各原件权利说明处理，必要时只提供原文链接，不把数据集许可套用于论文。

## 数据

**P. Cortez, A. Cerdeira, F. Almeida, T. Matos, J. Reis (2009). Wine Quality. UCI Machine Learning Repository.**

- [数据集主页](https://archive.ics.uci.edu/dataset/186/wine+quality)，[数据 DOI](https://doi.org/10.24432/C56S3T)，[原始 ZIP](https://archive.ics.uci.edu/static/public/186/wine+quality.zip)。
- 主页明确 **CC BY 4.0**，[许可](https://creativecommons.org/licenses/by/4.0/legalcode)。数据作者归属保留；本包的 CSV 是上述公开数据的格式转换及子集，增加追踪/划分列，非新采集或合成数据。
- 原说明给出介绍论文：*Modeling wine preferences by data mining from physicochemical properties*. Decision Support Systems 47(4):547–553，[10.1016/j.dss.2009.05.016](https://doi.org/10.1016/j.dss.2009.05.016)。本包未附该论文全文，也不声称复现其方法/指标。
- ZIP 内原件：红 1599 行，白 4898 行；UCI 当前元数据 `num_instances=4898` 不能当两者合计。案例只分析红葡萄酒，白数据只留作原始来源备份。
- 转换明细及变量含义：`02-data/materials/DATA_NOTES.md`。可从 ZIP 用 `tools/prepare_data.py`重建两份输入。

## 方法参考与核对材料

`ridge_reference.py` 以及核算脚本为本案例新编写，不是上述三篇树模型论文或葡萄酒介绍论文的官方代码。OLS/Ridge 是验证执行、方法续接与数据续接的轻量案例，文献任务与数据任务互不声称为同一实验。
