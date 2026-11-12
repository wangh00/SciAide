package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/research"
)

const researchReportVersion = "dynamic-report-v6"
const retrievalAppendixHeading = "## 附录：检索方法记录"

func usesReportProvenance(node CompiledNode) bool {
	return node.ID == "report_drafting" && (node.PromptVersion == researchReportVersion || strings.HasPrefix(node.PromptVersion, researchReportVersion+"-") || node.PromptVersion == "dynamic-report-v5" || strings.HasPrefix(node.PromptVersion, "dynamic-report-v5-"))
}

func reportRetrievalAppendix(input json.RawMessage) (string, error) {
	var stage struct {
		Audit    *researchRecordAudit `json:"researchRecordAudit"`
		Contract struct {
			Query string `json:"query"`
		} `json:"researchContract"`
		Source struct {
			Retrieval *struct {
				Queries        []string                `json:"queries"`
				Sources        []research.SourceSearch `json:"sources"`
				CandidateCount int                     `json:"candidateCount"`
				CompletedAt    *string                 `json:"completedAt"`
				FirstPageOnly  bool                    `json:"firstPageOnly"`
			} `json:"retrieval"`
		} `json:"researchSourceContext"`
	}
	if err := json.Unmarshal(input, &stage); err != nil {
		return "", fmt.Errorf("读取报告检索记录失败：%w", err)
	}
	r := stage.Source.Retrieval
	if r == nil {
		return "", nil
	}
	var b strings.Builder
	b.WriteString(retrievalAppendixHeading + "\n\n检索记录用于复核获取范围，不代表已穷尽所有相关研究。来源请求失败不等同于没有相关文献。\n")
	if r.CompletedAt != nil {
		b.WriteString("\n检索完成时间：" + *r.CompletedAt + "\n")
	}
	if r.FirstPageOnly {
		b.WriteString("\n范围：每个检索式、每个启用来源仅获取首页，未自动翻页。\n")
	}
	if stage.Audit != nil && stage.Audit.Version == recordAuditVersion {
		b.WriteString("\n### 已保存的题录归并记录\n\n" + stage.Audit.Interpretation + "\n")
		if stage.Audit.Status != "available" {
			b.WriteString("\n部分查询归并记录暂不可读取；不能把未提供的部分当作未执行。\n")
		}
		for _, q := range stage.Audit.Queries {
			fmt.Fprintf(&b, "\n查询 `%s`：%d 条保存的来源记录归属到 %d 个候选，组内额外来源记录 %d 条。此处不代表独立试验数量。\n", researchTableText(q.QueryID), q.SourceRecords, q.Candidates, q.GroupedRecords)
			writeRecordedGroupingMethod(&b, q)
			b.WriteString("\n| 候选 ID | 来源记录 ID（按保存归属） | 原记录标识（供复核，不倒推匹配规则） |\n| --- | --- | --- |\n")
			for _, g := range q.Groups {
				ids := []string{}
				identifiers := []string{}
				for _, r := range g.Records {
					ids = append(ids, r.Source+":"+r.ID)
					identifier := r.SourceRecordID
					for _, alias := range r.Aliases {
						if alias.Kind == "doi" || alias.Kind == "pmid" || alias.Kind == "arxiv" {
							identifier = alias.Kind + ":" + alias.Value
							break
						}
					}
					if identifier != "" {
						identifiers = appendUnique(identifiers, identifier)
					}
				}
				fmt.Fprintf(&b, "| %s | %s | %s |\n", researchTableText(g.CandidateID), researchTableText(strings.Join(ids, "；")), researchTableText(strings.Join(identifiers, "；")))
			}
		}
	} else if usesExplicitReportAssembly(input) {
		fmt.Fprintf(&b, "\n保存的候选记录数：%d；该字段不提供去重规则、去重前后计数或独立研究数，不能据此宣称完成了可复核的研究去重。\n", r.CandidateCount)
	} else {
		// Frozen reports must remain reproducible. New attempts opt in at input
		// binding; never change a saved appendix/hash during recovery.
		fmt.Fprintf(&b, "\n合并去重候选数：%d；候选数不等于纳入文献数或独立研究数。\n", r.CandidateCount)
	}
	if stage.Contract.Query != "" {
		b.WriteString("\n### 原定检索式\n\n" + retrievalCodeBlock(stage.Contract.Query))
	}
	b.WriteString("\n### 实际执行检索式\n")
	for index, query := range r.Queries {
		fmt.Fprintf(&b, "\n检索式 %d：\n\n%s", index+1, retrievalCodeBlock(query))
	}
	if len(r.Queries) == 0 {
		b.WriteString("\n当前记录未提供完整检索式。\n")
	}
	if stage.Contract.Query != "" && (len(r.Queries) != 1 || r.Queries[0] != stage.Contract.Query) {
		b.WriteString("\n实际检索式与原定文本不完全相同，以上并列保留原文供核对；不据此断言两者语义等价。\n")
	}
	b.WriteString("\n### 来源执行记录\n\n各次来源请求的检索语法、年份限制及返回状态如下；返回数量不是最终纳入数量。\n")
	for index, source := range r.Sources {
		// Encode the recorded fields, without guessing a query the provider never
		// reported. A fenced value cannot inject report Markdown structure.
		entry := map[string]any{"来源": source.SourceID, "状态": source.Status, "返回条数": source.Count, "实际查询": source.ProviderQuery, "有效检索式": source.EffectiveQuery, "查询模式": source.QueryMode, "年份限制": source.PublicationYears, "已应用年份限制": source.YearFilterApplied, "达到返回上限": source.LimitReached}
		if source.QueryProjectionVersion != "" {
			delete(entry, "实际查询")
			delete(entry, "已应用年份限制")
			entry["编译后的来源查询表达式"] = source.ProviderQuery
			entry["已构造年份限制"] = source.YearFilterApplied
			entry["查询投影版本"] = source.QueryProjectionVersion
			entry["查询输入来源"] = source.QueryOrigin
			entry["对应逻辑检索式"] = source.LogicalQuery
			entry["来源输入式"] = source.InputQuery
			entry["投影说明"] = "限制列表只描述来源输入式到来源查询的语法转换，不证明规划式与逻辑原式语义等价；表达式及已构造的年份参数不证明本次已发送 HTTP、未命中缓存或上游已执行过滤，实际请求另查执行记录。"
			if len(source.QueryLimitations) > 0 {
				entry["未保留的查询约束"] = source.QueryLimitations
			}
			if source.QueryMode == "ranked_bibliographic" {
				entry["排名式检索边界"] = "排名式关键词不等价于布尔检索，不保证扩大召回；字段、短语、否定及纳入条件仍需筛选核对。"
			}
		}
		if source.ErrorCode != "" {
			entry["错误代码"] = source.ErrorCode
		}
		encoded, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "\n来源请求 %d：\n\n%s", index+1, retrievalCodeBlock(string(encoded)))
	}
	return strings.TrimSpace(b.String()), nil
}

func retrievalCodeBlock(value string) string {
	fence := "```"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	return fence + "text\n" + value + "\n" + fence + "\n"
}

func enrichReportProvenance(value, input json.RawMessage) (json.RawMessage, []AIStageNormalization, error) {
	fields := decodeObject(value)
	markdown, ok := fields["markdown"].(string)
	if !ok {
		return nil, nil, fmt.Errorf("报告缺少 Markdown 正文")
	}
	appendices, err := assembledReportAppendices(markdown, input)
	if err != nil {
		return value, nil, err
	}
	rule := "append_frozen_retrieval_log"
	if len(researchHandoffAppendices(markdown, input)) > 0 || decodeObject(input)["computationRecordPolicy"] == computationRecordVersion {
		rule = "append_frozen_research_records"
	}
	if reportDesignMaterialAppendix(input) != "" {
		rule = "append_frozen_design_materials"
		if len(appendices) > 1 {
			rule = "append_frozen_research_records"
		}
	}
	if len(appendices) == 0 {
		return value, nil, nil
	}
	appendix := strings.Join(appendices, "\n\n")
	if strings.HasSuffix(strings.TrimSpace(markdown), appendix) {
		if usesExplicitReportAssembly(input) {
			// Collapse only consecutive, byte-identical generated suffixes. Keep
			// raw model output in the journal and audit this deterministic change;
			// do not delete merely similar headings, prose, or old frozen outputs.
			body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(markdown), appendix))
			duplicate := false
			for body == appendix || strings.HasSuffix(body, "\n\n"+appendix) {
				body = strings.TrimSpace(strings.TrimSuffix(body, appendix))
				duplicate = true
			}
			if decodeObject(input)["computationRecordPolicy"] == computationRecordVersion && strings.Contains(body, computationAppendixHeading) {
				return nil, nil, fmt.Errorf("计算执行记录正文中存在另一份同名或冲突记录；请省略模型抄写的记录，保留科学正文")
			}
			if usesDesignMaterialAssembly(input) && strings.Contains(body, designMaterialsHeading) {
				return nil, nil, fmt.Errorf("设计材料正文中存在另一份同名或冲突附录；请省略宿主附录，材料修正须回其设计产出阶段")
			}
			if duplicate {
				updated := body + "\n\n" + appendix
				fields["markdown"] = updated
				return mustJSON(fields), []AIStageNormalization{{Path: "$.markdown", Rule: "deduplicate_identical_host_appendices", BeforeSHA256: hashBytes([]byte(markdown)), AfterSHA256: hashBytes([]byte(updated))}}, nil
			}
		}
		return value, nil, nil
	}
	// An author can omit the appendix entirely. Never silently replace an
	// altered one, which could hide a discrepancy in the research methods.
	headings := []string{retrievalAppendixHeading, screeningAppendixHeading, bibliographyAppendixHeading}
	if usesDesignMaterialAssembly(input) {
		headings = append(headings, designMaterialsHeading)
	}
	if decodeObject(input)["computationRecordPolicy"] == computationRecordVersion {
		headings = append(headings, computationAppendixHeading)
	}
	for _, heading := range headings {
		if strings.Contains(markdown, heading) {
			return nil, nil, fmt.Errorf("%s 由保存的原始记录自动附加，请省略该附录，不要改写旧附录；正文方法和科学局限仍需保留", heading)
		}
	}
	updated := strings.TrimSpace(markdown) + "\n\n" + appendix
	fields["markdown"] = updated
	return mustJSON(fields), []AIStageNormalization{{Path: "$.markdown", Rule: rule, BeforeSHA256: hashBytes([]byte(markdown)), AfterSHA256: hashBytes([]byte(updated))}}, nil
}

func validateReportProvenance(node CompiledNode, value, input json.RawMessage) error {
	if !usesReportProvenance(node) {
		return nil
	}
	var report struct {
		Markdown     string   `json:"markdown"`
		ClaimSummary []string `json:"claimSummary"`
		Method       string   `json:"methodSummary"`
		Limitations  []string `json:"limitations"`
	}
	if json.Unmarshal(value, &report) != nil {
		return fmt.Errorf("报告缺少与真实检索日志一致的附录")
	}
	appendices, err := assembledReportAppendices(report.Markdown, input)
	if err != nil {
		return err
	}
	if len(appendices) > 0 && !strings.HasSuffix(strings.TrimSpace(report.Markdown), strings.Join(appendices, "\n\n")) {
		return fmt.Errorf("报告来源附录与冻结的筛选记录或实际引用身份不一致")
	}
	if len(appendices) > 0 {
		// The exact suffix is already checked above. A saved source/reason may
		// quote internal wording; that is not authored report-policy commentary.
		report.Markdown = strings.TrimSuffix(strings.TrimSpace(report.Markdown), strings.Join(appendices, "\n\n"))
	}
	if decodeObject(input)["computationRecordPolicy"] == computationRecordVersion && strings.Contains(report.Markdown, computationAppendixHeading) {
		return fmt.Errorf("计算执行记录正文中存在另一份同名或冲突记录")
	}
	if usesDesignMaterialAssembly(input) && strings.Contains(report.Markdown, designMaterialsHeading) {
		return fmt.Errorf("设计材料正文中存在另一份同名或冲突附录；不能通过报告改写冻结设计材料")
	}
	// Narrow, explicit process-language checks only. Do not strip text or ban
	// citation markers, words such as 'review', or genuine research limitations.
	fields := map[string]string{"markdown": report.Markdown, "methodSummary": report.Method, "claimSummary": strings.Join(report.ClaimSummary, "\n"), "limitations": strings.Join(report.Limitations, "\n")}
	for name, text := range fields {
		for _, phrase := range []string{"显示时可呈现为常规编号引用", "这些标记不是额外文献", "独立审查曾要求", "获准撰写初稿", "获准在覆盖有限前提下撰写初稿"} {
			if strings.Contains(text, phrase) {
				return fmt.Errorf("报告 %s 残留内部流程说明 %q；请删除该说明，保留合法引用、科学内容和真实局限", name, phrase)
			}
		}
	}
	return validateReportOutputSchema(node, value, input)
}

const reportProvenanceInstruction = `
The host appends a retrieval-methods appendix from the actual frozen search logs before review and delivery. Do not write, copy or edit the section titled "` + retrievalAppendixHeading + `"; omit it when revising an earlier draft. Keep a scientifically accurate methods summary and limitations in the authored report. The appendix preserves planned/executed queries, source adaptations, status, time and limits; it does not establish exhaustive coverage or semantic equivalence of changed queries. Keep review findings and response-to-review commentary out of all report fields. Preserve valid citations and scientific limitations.`
