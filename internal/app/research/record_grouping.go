package research

// The version describes the exact repository decision procedure, not a
// scientific judgement or a claim that reports represent independent trials.
const RecordGroupingVersion = "record-grouping-v2"

const recordGroupingV1Rules = "题录归组按项目内保存记录执行：相同来源和来源记录ID复用既有候选归属，不重新推断其历史归并依据。新来源记录先规范化DOI、PMID、arXiv、OpenAlex标识；有任一强标识时仅按这些标识精确查找，不按题名兜底。无强标识时，年份至少1000且规范化题名至少16个字符，才使用年份与规范化题名精确匹配；题名转小写，非字母数字字符折叠为空格。全部匹配只指向一个候选则归入；指向多个候选则保留为新的歧义候选，不自动合并已有候选；无匹配也新建候选。不是作者匹配、模糊题名匹配或独立试验去重。"

// Older snapshots retain their actual procedure; never label v1 decisions v2.
const RecordGroupingRules = "题录归组按项目内保存记录执行：相同来源和来源记录ID复用既有候选归属，不重新推断历史归并依据。新来源记录仅按经语法形状校验的规范化DOI、PMID、arXiv、OpenAlex强标识精确查找（不证明已注册或可解析）；题名和年份不作为自动归并身份。命中唯一候选且共享标识种类不存在不同取值时归入；命中多个候选或共享种类取值冲突时独立保存歧义候选；无命中新建。每种别名保存全部命中数和按候选ID排序的至多两个命中见证，足以证明唯一或歧义；唯一候选每种标识保存首个冲突值见证，不保存平方级关系清单。题名、作者、摘要相似和版本/伴随材料提示仅为待核实线索，不合并候选，不代表独立试验去重。"

func RecordGroupingRulesFor(version string) (string, bool) {
	switch version {
	case RecordGroupingVersion:
		return RecordGroupingRules, true
	case "record-grouping-v1":
		return recordGroupingV1Rules, true
	default:
		return "", false
	}
}

type RecordGroupingAliasCount struct {
	Kind     string `json:"kind"`
	Value    string `json:"value"`
	Total    int    `json:"total"`
	Retained int    `json:"retained"`
}
type RecordGroupingMatch struct {
	CandidateID string `json:"candidateId"`
	Kind        string `json:"kind"`
	Value       string `json:"value"`
}

// Captured in the same transaction as the query snapshot. Never filled from
// today's mutable bibliography when an old snapshot did not record a decision.
type RecordGrouping struct {
	MatchCounts     []RecordGroupingAliasCount `json:"matchCounts,omitempty"`
	InputOrdinal    int                        `json:"inputOrdinal"`
	Version         string                     `json:"version"`
	Action          string                     `json:"action"`
	CandidateID     string                     `json:"candidateId"`
	ComparedAliases []CandidateAlias           `json:"comparedAliases,omitempty"`
	Conflicts       []RecordGroupingMatch      `json:"conflicts,omitempty"`
	Matches         []RecordGroupingMatch      `json:"matches,omitempty"`
}
