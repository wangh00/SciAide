package opensciskill

import (
	"strings"
	"unicode"
)

const routingConceptPrefix = "concept:"

// routingConceptAliases maps both request text and Skill metadata onto the
// same language-neutral feature. Keep the vocabulary focused on task intent;
// a Skill can declare domain-specific translations with routing-aliases.
var routingConceptAliases = map[string][]string{
	"manuscript":        {"论文", "稿件", "手稿", "学术语气", "论文正文", "manuscript", "paper", "academic writing", "scientific writing", "academic tone"},
	"revision":          {"润色", "改写", "修改稿件", "proofread", "revise", "revision", "edit", "polish"},
	"literature":        {"文献", "资料调研", "literature", "publication", "scholarly source"},
	"literature-review": {"文献综述", "系统综述", "综述", "研究现状", "研究空白", "综述框架", "literature review", "systematic review", "scoping review", "state of research", "research gaps", "review outline"},
	"citation":          {"引用", "引文", "参考文献", "citation", "reference", "bibliography", "bibtex", "doi"},
	"search":            {"检索", "查文献", "查找文献", "搜索论文", "权威文献", "相关研究", "近期研究", "出版信息", "find", "search", "lookup", "retrieval", "find papers", "recent studies"},
	"schematic":         {"示意图", "流程图", "架构图", "科学图解", "画出", "工作流", "diagram", "schematic", "flowchart", "architecture diagram", "diagram the workflow"},
	"visualization":     {"图表", "绘图", "可视化", "科研图", "数据图", "多面板", "定量结果", "figure", "plot", "chart", "visualization", "multi panel", "journal ready"},
	"image-generation":  {"生成图片", "插画", "配图", "image generation", "illustration", "artwork"},
	"experiment":        {"实验", "试验", "experiment", "assay"},
	"protocol":          {"实验方案", "实验流程", "操作规程", "protocol", "procedure"},
	"statistics":        {"统计", "统计检验", "显著性", "组间差异", "多重比较", "效应量", "样本量", "功效", "statistics", "statistical", "significant", "group differences", "hypothesis test", "power analysis", "multiple comparison", "effect size"},
	"data-analysis":     {"数据分析", "探索性分析", "数据分布", "异常值", "缺失值", "相关性分析", "主要模式", "eda", "analysis", "data analysis", "exploratory analysis", "data patterns", "outliers", "missingness", "correlations"},
	"clinical":          {"临床", "诊疗", "病人", "clinical", "patient", "medical"},
	"biology":           {"生物", "生物学", "biology", "biological"},
	"chemistry":         {"化学", "化合物", "chemistry", "chemical"},
	"physics":           {"物理", "物理学", "physics", "physical"},
	"gene":              {"基因", "基因组", "转录组", "突变", "拷贝数变异", "基因表达", "驱动基因", "gene", "genome", "genomics", "transcriptome", "mutation", "copy number", "gene expression", "driver genes"},
	"protein":           {"蛋白", "蛋白质", "蛋白组", "protein", "proteomics"},
	"single-cell":       {"单细胞", "单细胞测序", "single cell", "single-cell", "scrna"},
	"bioinformatics":    {"生物信息", "生信", "bioinformatics", "computational biology"},
	"molecule":          {"分子", "小分子", "molecule", "molecular"},
	"drug-discovery":    {"药物发现", "药物设计", "小分子药物", "先导化合物", "药效团", "候选分子", "结合配体", "drug discovery", "drug design", "small molecule drugs", "lead compound", "lead optimization", "pharmacophore", "binding ligand"},
	"imaging":           {"成像", "医学影像", "显微图像", "显微镜", "荧光显微", "组织切片", "形态特征", "imaging", "bioimage", "biomedical images", "microscopy", "micrograph", "radiology", "tissue section", "morphological features", "quantify cells"},
	"machine-learning":  {"机器学习", "machine learning", "ml"},
	"deep-learning":     {"深度学习", "神经网络", "deep learning", "neural network"},
	"model":             {"模型", "建模", "model", "modeling"},
	"training":          {"训练", "微调", "train", "training", "fine tuning", "finetuning"},
	"poster":            {"海报", "墙报", "poster", "beamerposter"},
	"slides":            {"幻灯片", "汇报", "演示文稿", "口头报告", "slides", "presentation", "slide deck", "conference talk"},
	"grant":             {"基金", "标书", "项目申请", "申请书", "预算论证", "研究目标", "创新性", "grant", "funding proposal", "grant proposal", "funding application", "specific aims", "budget justification", "impact section"},
	"peer-review":       {"同行评审", "审稿", "审稿人", "审稿意见", "拒稿", "大修", "方法学缺陷", "peer review", "review manuscript", "referee", "reviewer report", "rejection", "major revision"},
	"meta-analysis":     {"荟萃分析", "元分析", "meta analysis", "meta-analysis"},
	"survival":          {"生存分析", "生存曲线", "生存率", "删失", "时间事件", "一致性指数", "survival analysis", "survival model", "patient survival", "kaplan meier", "cox regression", "censored", "time to event", "concordance index"},
	"hypothesis":        {"研究假设", "科学假设", "提出假设", "实验假设", "竞争性假设", "假说", "可证伪", "hypothesis", "hypothesis generation", "competing hypotheses", "falsifiable"},
	"reproducibility":   {"复现", "可重复性", "重复实验", "reproduce", "reproducibility", "replication"},
	"database":          {"数据库", "数据源", "database", "dataset"},
	"protein-diagram":   {"蛋白质结构域", "蛋白结构域", "蛋白序列特征", "蛋白结构注释", "功能位点", "protein domain", "protein sequence feature", "annotated protein structure", "functional sites"},
	"physics-fitting":   {"物理实验数据", "测量曲线", "非线性拟合", "拟合参数", "拟合优度", "physics fitting", "physical experiment data", "nonlinear fitting", "fitting parameters", "fitted parameters", "goodness of fit", "uncertainty of fitted parameters"},
}

// routingSkillConcepts is a small audited semantic profile for high-value
// workflows. It avoids relying on incidental words in long descriptions and
// makes Chinese, English, and mixed-language requests score the same intent.
var routingSkillConcepts = map[string][]string{
	"scientific-writing":        {"manuscript", "revision"},
	"citation-management":       {"citation"},
	"scientific-schematics":     {"schematic"},
	"statistical-analysis":      {"statistics"},
	"research-grants":           {"grant"},
	"peer-review":               {"peer-review"},
	"research-lookup":           {"search"},
	"literature-review":         {"literature-review"},
	"hypothesis-generation":     {"hypothesis"},
	"exploratory-data-analysis": {"data-analysis"},
	"scikit-survival":           {"survival"},
	"bioimage-analysis":         {"imaging"},
	"cancer-genomics-analysis":  {"clinical", "gene"},
	"drug-design":               {"drug-discovery"},
	"protein-diagram":           {"protein-diagram"},
	"scientific-visualization":  {"visualization"},
	"scientific-slides":         {"slides"},
	"latex-posters":             {"poster"},
	"protocolsio-integration":   {"protocol"},
	"physics-fitting":           {"physics-fitting"},
}

var englishRoutingLemma = map[string]string{
	"analyses": "analysis", "analyzed": "analysis", "analyzing": "analysis", "analytical": "analysis",
	"bibliographies": "bibliography", "charts": "chart", "citations": "citation", "cited": "citation",
	"datasets": "dataset", "diagrams": "diagram", "drafted": "draft", "drafting": "draft", "drugs": "drug",
	"edited": "edit", "editing": "edit", "experiments": "experiment", "figures": "figure",
	"genes": "gene", "genomic": "genomics", "grants": "grant", "hypotheses": "hypothesis", "illustrations": "illustration", "manuscripts": "manuscript",
	"models": "model", "modelled": "model", "modeling": "model", "modelling": "model",
	"molecules": "molecule", "papers": "paper", "patients": "patient", "plots": "plot",
	"posters": "poster", "presentations": "presentation", "proteins": "protein", "protocols": "protocol",
	"references": "reference", "reviewed": "review", "reviewing": "review", "revised": "revise",
	"revising": "revise", "slides": "slide", "sources": "source", "trained": "train", "training": "train",
	"visualisation": "visualization", "visualize": "visualization", "visualizing": "visualization",
	"writes": "write", "writing": "write", "written": "write",
}

func routingFeatures(value string, detectNegation bool) (map[string]struct{}, map[string]struct{}) {
	lower := strings.ToLower(value)
	features := map[string]struct{}{}
	wordSet := map[string]struct{}{}
	for _, word := range routingWords.FindAllString(lower, -1) {
		word = normalizeRoutingWord(word)
		if len(word) <= 2 {
			continue
		}
		if _, stopped := routingStopWords[word]; stopped {
			continue
		}
		wordSet[word] = struct{}{}
		features[word] = struct{}{}
	}
	for _, sequence := range hanSequences(lower) {
		runes := []rune(sequence)
		if len(runes) <= 4 {
			features[sequence] = struct{}{}
		}
		for size := 2; size <= 3; size++ {
			for index := 0; index+size <= len(runes); index++ {
				features[string(runes[index:index+size])] = struct{}{}
			}
		}
	}
	negated := map[string]struct{}{}
	for concept, aliases := range routingConceptAliases {
		matched, denied := false, false
		for _, alias := range aliases {
			if !routingPhraseMatches(lower, wordSet, alias) {
				continue
			}
			matched = true
			if detectNegation && routingPhraseNegated(lower, alias) {
				denied = true
			}
		}
		feature := routingConceptPrefix + concept
		if denied {
			negated[feature] = struct{}{}
		}
		if matched && !denied {
			features[feature] = struct{}{}
		}
	}
	return features, negated
}

func normalizeRoutingWord(value string) string {
	if lemma := englishRoutingLemma[value]; lemma != "" {
		return lemma
	}
	return value
}

func routingPhraseMatches(lower string, words map[string]struct{}, phrase string) bool {
	if containsHan(phrase) {
		return strings.Contains(lower, phrase)
	}
	terms := routingWords.FindAllString(strings.ToLower(phrase), -1)
	if len(terms) == 0 {
		return false
	}
	for _, term := range terms {
		if _, ok := words[normalizeRoutingWord(term)]; !ok {
			return false
		}
	}
	return true
}

func routingPhraseNegated(lower, phrase string) bool {
	for _, marker := range []string{"不要", "不需要", "无需", "不用", "别"} {
		start := strings.Index(lower, marker)
		for start >= 0 {
			after := []rune(lower[start+len(marker):])
			window := string(after[:min(40, len(after))])
			if routingPhraseMatches(window, routingWordSet(window), phrase) {
				return true
			}
			next := strings.Index(lower[start+len(marker):], marker)
			if next < 0 {
				break
			}
			start += len(marker) + next
		}
	}
	if containsHan(phrase) {
		return false
	}
	phraseTerms := routingWords.FindAllString(strings.ToLower(phrase), -1)
	if len(phraseTerms) == 0 {
		return false
	}
	tokens := routingWords.FindAllString(lower, -1)
	for index, token := range tokens {
		negative := token == "without" || token == "avoid" || token == "exclude" || token == "excluding" || token == "no"
		if token == "not" || token == "don't" || token == "dont" {
			negative = true
		}
		if !negative {
			continue
		}
		end := min(len(tokens), index+7)
		window := make(map[string]struct{}, end-index-1)
		for _, value := range tokens[index+1 : end] {
			window[normalizeRoutingWord(value)] = struct{}{}
		}
		matched := true
		for _, term := range phraseTerms {
			if _, ok := window[normalizeRoutingWord(term)]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func containsHan(value string) bool {
	for _, character := range value {
		if unicode.Is(unicode.Han, character) {
			return true
		}
	}
	return false
}
