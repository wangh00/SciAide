package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const literatureHierarchyInstruction = `按 _literature.phase 执行且仅提交对应阶段字段。batch: 逐篇初筛，提交 summary/candidateAssessments/evidenceNotes；不得输出全局 coverage、supplementalQueries 或推荐清单。每个候选对应一条 evidenceNotes；exclude 只需 candidateId 和 quotes，排除依据写在 assessment.reason；core/support 必须包含研究设计、人群、干预/暴露、对照、结局、主要发现及待核验项，未提供的信息明确写 unknown。原文完整分布在 titleSegments/abstractSegments 的 text，按顺序连接就是原文，没有摘要删节。quotes 优先只填写 {field,segmentId}，例如 {"field":"abstract","segmentId":"a0002"}；选择支持本候选判断的实际片段，宿主按当前 candidateId 和 field 取回原句，不必抄写 quote。编号仅在本候选字段内有效，不能借用其他候选片段。若使用 quote 则必须是原文连续逐字摘录，最多500字符。有摘要时引用摘要，无摘要时引用题名且不能判 core。reason 简短说明依据。synthesis: 依据 evidenceRecords 或 childSummaries 整理一致结论、冲突和遗漏，仅提交 summary/findings/uncertainties，每条包含 text 和真实 candidateIds；保留重要不确定性，不将同一试验的多篇文章计为独立试验。coverage: 提交 summary/coverage/supplementalQueries/recommendation/recommendedCandidateIds/recheckCandidateIds/rechecks/findings/uncertainties，不重新逐篇初筛。需要原文解决具体冲突或误排时在 recheckCandidateIds 填候选 ID，并在 rechecks 逐项写具体问题。先前 AI 初筛不是已验证事实。预算不足、来源失败不是学术证据不足，不按报告长短收窄检索。任何 findings/uncertainties 只能引用当前输入覆盖的候选。core=摘要支持冻结范围的直接相关材料，support=相关但需要核验，exclude=有明确不符依据。synthesis 必须在 findings 或 uncertainties 中涵盖每个保留候选，多层综合保留子层重要发现及疑点。所有回复包含 phase，只输出当前阶段字段，不写重复背景。`

type literatureQuote struct {
	Field     string `json:"field"`
	Quote     string `json:"quote,omitempty"`
	SegmentID string `json:"segmentId,omitempty"`
}

func completeLiteratureNote(n *literatureNote) bool {
	if n == nil {
		return false
	}
	for _, value := range []string{n.Design, n.Population, n.Intervention, n.Comparator, n.Outcome, n.Finding, n.Uncertainty} {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

const literatureEfficiencyInstruction = `本阶段只做题名/摘要证据整理，不是全文质量认证。排除项只输出 candidateId 和支持排除的原文 quotes，其余证据字段省略；保留项各字段用简短事实或 unknown，避免重复研究背景。题名由宿主绑定，无需重复抄写；有摘要必须摘录摘要，无摘要必须摘录题名，保留连续原文依据。不输出解释性前言。coverage 不逐篇重写记录，聚焦跨研究结论与真实缺口；同一试验的报告不能计为独立试验。recheckCandidateIds 与 rechecks[{candidateId,question}] 一一对应，仅请求能通过现有题名/摘要解决的具体冲突或疑似误排；全文、注册号或缺失数据才能解决的问题记入 uncertainties，不能要求反复回读相同摘要。不要请求 alreadyRecheckedCandidateIds 中的未变化来源；remainingRecheckRounds=0 时两个回查数组为空。来源失败和缺少全文不等同于检索不存在相关研究，仍明确限制结论。`

type literatureNote struct {
	CandidateID  string            `json:"candidateId"`
	Design       string            `json:"design"`
	Population   string            `json:"population"`
	Intervention string            `json:"intervention"`
	Comparator   string            `json:"comparator"`
	Outcome      string            `json:"outcome"`
	Finding      string            `json:"finding"`
	Uncertainty  string            `json:"uncertainty"`
	Quotes       []literatureQuote `json:"quotes"`
}
type literatureFinding struct {
	Text         string   `json:"text"`
	CandidateIDs []string `json:"candidateIds"`
}
type literatureEvidenceRecord struct {
	CandidateID  string               `json:"candidateId"`
	Title        string               `json:"title"`
	SourceSHA256 string               `json:"sourceSha256"`
	ExecutionID  string               `json:"executionId,omitempty"`
	Assessment   literatureAssessment `json:"assessment"`
	Note         *literatureNote      `json:"note,omitempty"`
}
type literatureSummary struct {
	Key                   string              `json:"key"`
	ExecutionID           string              `json:"executionId"`
	CandidateIDs          []string            `json:"candidateIds"`
	Summary               string              `json:"summary"`
	Findings              []literatureFinding `json:"findings"`
	Uncertainties         []literatureFinding `json:"uncertainties"`
	UncertainCandidateIDs []string            `json:"uncertainCandidateIds"`
	ExcludedCandidateIDs  []string            `json:"excludedCandidateIds"`
}

func hierarchySchema() json.RawMessage {
	s := decodeObject(candidateScreeningSchema())
	p := s["properties"].(map[string]any)
	s["required"] = []string{"phase", "summary"}
	p["phase"] = map[string]any{"type": "string", "enum": []string{"batch", "synthesis", "coverage"}}
	p["recommendedCandidateIds"].(map[string]any)["maxItems"] = 100
	p["candidateAssessments"].(map[string]any)["maxItems"] = 20
	assessment := p["candidateAssessments"].(map[string]any)["items"].(map[string]any)
	assessment["required"] = []string{"candidateId", "decision", "relevance", "reason", "importAction", "purpose"}
	assessmentProps := assessment["properties"].(map[string]any)
	assessmentProps["importAction"] = map[string]any{"type": "string", "enum": []string{"direct", "verify", "background", "exclude"}}
	assessmentProps["purpose"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 240}
	p["candidateAssessments"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["reason"].(map[string]any)["maxLength"] = 400
	ids := map[string]any{"type": "array", "maxItems": 32, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}
	p["recheckCandidateIds"] = map[string]any{"type": "array", "maxItems": 20, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}
	p["rechecks"] = map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"candidateId", "question"}, "properties": map[string]any{"candidateId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "question": map[string]any{"type": "string", "minLength": 1, "maxLength": 400}}}}
	for _, name := range []string{"findings", "uncertainties"} {
		p[name] = map[string]any{"type": "array", "maxItems": 12, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"text", "candidateIds"}, "properties": map[string]any{"text": map[string]any{"type": "string", "minLength": 1, "maxLength": 600}, "candidateIds": ids}}}
	}
	noteProps := map[string]any{"candidateId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "quotes": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"field", "quote"}, "properties": map[string]any{"field": map[string]any{"type": "string", "enum": []string{"title", "abstract"}}, "quote": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}}}}}
	quoteSchema := noteProps["quotes"].(map[string]any)["items"].(map[string]any)
	quoteSchema["required"] = []string{"field"}
	quoteSchema["properties"].(map[string]any)["segmentId"] = map[string]any{"type": "string", "pattern": "^[at][0-9]{4,6}$"}
	fields := []string{"candidateId", "design", "population", "intervention", "comparator", "outcome", "finding", "uncertainty", "quotes"}
	for _, name := range fields[1 : len(fields)-1] {
		noteProps[name] = map[string]any{"type": "string", "minLength": 1, "maxLength": 300}
	}
	p["evidenceNotes"] = map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"candidateId", "quotes"}, "properties": noteProps}}
	coverage := p["coverage"].(map[string]any)["properties"].(map[string]any)
	p["coverage"].(map[string]any)["required"] = []string{"strength", "sufficientForClaimedScope", "abstractAvailable", "metadataOnly", "gaps"}
	for _, name := range []string{"independentStudyEstimate", "directPopulationMatches", "abstractAvailable", "metadataOnly"} {
		coverage[name].(map[string]any)["maximum"] = 1600
	}
	return mustJSON(s)
}

func literaturePhaseNode(node CompiledNode, input json.RawMessage) CompiledNode {
	if node.ID != "candidate_screening" || node.PromptVersion != literatureScreeningVersion {
		return node
	}
	var in struct {
		Literature literatureInput `json:"_literature"`
	}
	if json.Unmarshal(input, &in) != nil {
		return node
	}
	schema := decodeObject(node.OutputSchema)
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return node
	}
	fields := literaturePhaseFields(in.Literature.Phase)
	if fields == nil {
		return node
	}
	kept := map[string]any{}
	for _, name := range append([]string{"phase", "summary"}, fields...) {
		kept[name] = properties[name]
	}
	kept["phase"] = map[string]any{"type": "string", "enum": []string{in.Literature.Phase}}
	if in.Literature.Phase == "coverage" {
		kept["supplementalQueries"].(map[string]any)["maxItems"] = 0
		node.Prompt += "\n已按每式每源首页20条完成有界检索，不再补搜或翻页。supplementalQueries必须为空；评估现有证据及真实局限，不声称穷尽数据库，来源失败不能解释为领域无证据。"
	}
	node.Prompt += "\n本阶段仅为确认纳入前的相关性筛选。synthesis和coverage只整理纳入理由、主题覆盖与待核验事项，不提前综合效果或下科研结论。findings字段仅记主题相关性；sufficientForClaimedScope只表示材料可进入深入分析，不表示结论已获证实。完整证据分析安排在用户确认纳入后。"
	schema["properties"] = kept
	schema["required"] = append([]string{"phase", "summary"}, fields...)
	node.OutputSchema = mustJSON(schema)
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	if in.Literature.Phase == "batch" {
		node.Prompt += "\n本课题若比较已发生的研究效果：原文明示尚无结果的protocol/研究方案应background，不用verify期待同一文件里出现未来结果；只有任务本身研究方案设计或进行中研究时才按该用途推荐。更正须明确关联原研究并说明核验修正内容，不把更正当新结果。不要用追查其引用的其他论文作为泛综述默认导入理由。"
		node.Prompt += "\n同一次初筛必须分别给出相关性decision与导入建议importAction，不追加全局评估。importAction=direct：摘要足以表明材料直接回答冻结问题，decision=core；verify：题名或摘要强烈提示直接相关但缺关键信息，decision=support，purpose说明为何值得优先核验及缺什么；background：仅主题背景、宽泛综述、未知对照且缺少直接匹配线索，decision=support，保留但不默认导入；exclude：明确不符合，decision=exclude。purpose用简短语句写该材料能回答哪部分问题、核验目的或不推荐原因。缺摘要既不是排除理由，也不是自动推荐理由。无结果方案和一般评述不能当效果证据；更正只在能关联重要研究且需核对时verify，不作独立研究。复合干预必须写明不能单独归因；对照/人群混合的综述只有明确可提取匹配部分才direct，否则按匹配线索verify或background。非医学课题按其对象、方法、情境判断，不强套临床要素。用户明确边界优先，不自行把随访、研究类型等偏好升为硬限制。不要固定选取数量或比例；不能因为未排除就推荐导入。"
		if in.Literature.Triage {
			node.Prompt += "\n本次为轻量相关性筛选（triage=true），覆盖全部候选。覆盖前文完整证据字段要求：每篇只输出判定、简短reason、evidenceNotes中的candidateId及1条原文quotes，不输出design/population/intervention/comparator/outcome/finding/uncertainty。明确不符合才exclude，资料不足或边界不确定用support，不因缺摘要直接排除；用户确认纳入后才做详细分析。"
			notes := kept["evidenceNotes"].(map[string]any)["items"].(map[string]any)
			props, _ := notes["properties"].(map[string]any)
			for _, key := range []string{"design", "population", "intervention", "comparator", "outcome", "finding", "uncertainty"} {
				delete(props, key)
			}
			node.OutputSchema = mustJSON(schema)
			node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
		} else {
			node.Prompt += "\n本次为详细证据提取或原文复核：核对原始题名摘要，为所有保留项填写完整研究要素；若发现明确不符合可改为exclude并给出原文依据。"
		}
	}
	return node
}

func literaturePhaseFields(phase string) []string {
	switch phase {
	case "batch":
		return []string{"candidateAssessments", "evidenceNotes"}
	case "synthesis":
		return []string{"findings", "uncertainties"}
	case "coverage":
		return []string{"coverage", "supplementalQueries", "recommendation", "recommendedCandidateIds", "recheckCandidateIds", "rechecks", "findings", "uncertainties"}
	}
	return nil
}

func validateLiteratureHierarchy(output, input json.RawMessage) error {
	var in struct {
		Literature literatureInput            `json:"_literature"`
		Candidates []literatureCandidate      `json:"candidates"`
		Records    []literatureEvidenceRecord `json:"evidenceRecords"`
		Children   []literatureSummary        `json:"childSummaries"`
	}
	var result literatureScreening
	if json.Unmarshal(input, &in) != nil || json.Unmarshal(output, &result) != nil {
		return fmt.Errorf("invalid literature hierarchy submission")
	}
	if result.Phase != in.Literature.Phase {
		return fmt.Errorf("literature phase mismatch")
	}
	obj := decodeObject(output)
	allowed := map[string]bool{"phase": true, "summary": true}
	required := []string{}
	switch result.Phase {
	case "batch":
		required = []string{"candidateAssessments", "evidenceNotes"}
	case "synthesis":
		required = []string{"findings", "uncertainties"}
	case "coverage":
		required = []string{"coverage", "supplementalQueries", "recommendation", "recommendedCandidateIds", "recheckCandidateIds", "rechecks", "findings", "uncertainties"}
	default:
		return fmt.Errorf("unknown literature phase")
	}
	for _, name := range required {
		allowed[name] = true
		if _, ok := obj[name]; !ok {
			return fmt.Errorf("literature %s requires %s", result.Phase, name)
		}
	}
	for name := range obj {
		if !allowed[name] {
			return fmt.Errorf("literature %s must not emit %s", result.Phase, name)
		}
	}
	offered := map[string]bool{}
	for _, c := range in.Candidates {
		offered[c.ID] = true
	}
	for _, r := range in.Records {
		offered[r.CandidateID] = true
	}
	for _, child := range in.Children {
		for _, id := range child.CandidateIDs {
			offered[id] = true
		}
	}
	if result.Phase == "batch" {
		byID := map[string]literatureCandidate{}
		for _, c := range in.Candidates {
			byID[c.ID] = c
		}
		assessed := map[string]bool{}
		for _, a := range result.CandidateAssessments {
			if err := validateLiteratureImportAction(a); err != nil {
				return err
			}
			if !offered[a.CandidateID] || assessed[a.CandidateID] {
				return fmt.Errorf("invalid or repeated assessment ID")
			}
			assessed[a.CandidateID] = true
			if a.Decision == "core" && strings.TrimSpace(byID[a.CandidateID].Abstract) == "" {
				return fmt.Errorf("core candidate requires source abstract")
			}
		}
		if len(assessed) != len(offered) {
			return fmt.Errorf("every offered candidate requires an assessment")
		}
		noted := map[string]bool{}
		quoteIssues := []string{}
		for noteIndex, n := range result.EvidenceNotes {
			if !offered[n.CandidateID] || noted[n.CandidateID] {
				return fmt.Errorf("invalid or repeated evidence note ID")
			}
			noted[n.CandidateID] = true
			for _, a := range result.CandidateAssessments {
				if a.CandidateID == n.CandidateID && a.Decision != "exclude" && !in.Literature.Triage {
					for _, text := range []string{n.Design, n.Population, n.Intervention, n.Comparator, n.Outcome, n.Finding, n.Uncertainty} {
						if strings.TrimSpace(text) == "" {
							return fmt.Errorf("retained candidate needs design, population, intervention, comparator, outcome, finding and uncertainty evidence notes")
						}
					}
				}
			}
			c := byID[n.CandidateID]
			hasTitle, hasAbstract := false, false
			for quoteIndex, q := range n.Quotes {
				if q.SegmentID != "" {
					original, ok := literatureSegmentQuote(c, q)
					if !ok || original != q.Quote {
						quoteIssues = append(quoteIssues, fmt.Sprintf("candidate %s: %s source segment %s does not match frozen %s", c.ID, literatureQuotePath(noteIndex, quoteIndex), q.SegmentID, q.Field))
					}
				}
				source := ""
				switch q.Field {
				case "title":
					source = c.Title
					hasTitle = true
				case "abstract":
					source = c.Abstract
					hasAbstract = true
				}
				if q.Quote == "" || source == "" || !strings.Contains(source, q.Quote) {
					quoteIssues = append(quoteIssues, fmt.Sprintf("candidate %s: %s evidence quote is not present in source %s", c.ID, literatureQuotePath(noteIndex, quoteIndex), q.Field))
				}
			}
			if c.Abstract == "" && !hasTitle || c.Abstract != "" && !hasAbstract {
				quoteIssues = append(quoteIssues, fmt.Sprintf("candidate %s: evidence note requires an abstract quote when available, otherwise a title quote", c.ID))
			}
		}
		if len(noted) != len(offered) {
			return fmt.Errorf("every offered candidate requires a traceable evidence note")
		}
		if len(quoteIssues) > 0 {
			return fmt.Errorf("%d source quote issues (first %d): %s. Prefer the exact candidate's titleSegments/abstractSegments IDs; do not rewrite original numbers or punctuation", len(quoteIssues), min(8, len(quoteIssues)), strings.Join(quoteIssues[:min(8, len(quoteIssues))], "; "))
		}
		return nil
	}
	for _, finding := range append(result.Findings, result.Uncertainties...) {
		if len(finding.CandidateIDs) == 0 {
			return fmt.Errorf("synthesis finding needs source candidate IDs")
		}
		for _, id := range finding.CandidateIDs {
			if !offered[id] {
				return fmt.Errorf("synthesis references candidate outside input: %s", id)
			}
		}
	}
	if result.Phase == "synthesis" {
		covered := map[string]bool{}
		for _, finding := range append(result.Findings, result.Uncertainties...) {
			for _, id := range finding.CandidateIDs {
				covered[id] = true
			}
		}
		for _, r := range in.Records {
			if r.Assessment.Decision != "exclude" && !covered[r.CandidateID] {
				return fmt.Errorf("synthesis omitted retained candidate %s", r.CandidateID)
			}
		}
		for _, child := range in.Children {
			for _, finding := range append(child.Findings, child.Uncertainties...) {
				hasSource := false
				for _, id := range finding.CandidateIDs {
					hasSource = hasSource || covered[id]
				}
				if !hasSource {
					return fmt.Errorf("parent synthesis omitted a child finding; retain it or describe its limitation")
				}
			}
		}
	}
	for _, id := range append(result.RecommendedCandidateIDs, result.RecheckCandidateIDs...) {
		if !offered[id] {
			return fmt.Errorf("selection or readback references unavailable candidate: %s", id)
		}
	}
	if result.Phase == "coverage" {
		if len(result.SupplementalQueries) > 0 {
			return fmt.Errorf("first-page literature policy does not accept supplemental queries")
		}
		if err := validateLiteratureRechecks(result, in.Literature, offered); err != nil {
			return err
		}
		if result.Coverage.Sufficient && (in.Literature.Pending > 0 || len(result.RecheckCandidateIDs) > 0) {
			return fmt.Errorf("unresolved screening or readback cannot claim sufficient coverage")
		}
		if result.Recommendation == "use_recommendation" && len(offered) > 0 && len(result.RecommendedCandidateIDs) == 0 {
			return fmt.Errorf("recommended selection is empty")
		}
		if result.Coverage.Sufficient && len(result.Findings) == 0 {
			return fmt.Errorf("sufficient coverage requires traceable findings")
		}
		for _, r := range in.Records {
			if r.Assessment.Decision == "exclude" {
				for _, id := range result.RecommendedCandidateIDs {
					if id == r.CandidateID {
						return fmt.Errorf("excluded candidate needs source readback before recommendation")
					}
				}
			}
		}
		for _, child := range in.Children {
			for _, excluded := range child.ExcludedCandidateIDs {
				for _, id := range result.RecommendedCandidateIDs {
					if id == excluded {
						return fmt.Errorf("excluded candidate needs source readback before recommendation")
					}
				}
			}
		}
	}
	return nil
}

func literatureRecords(detail RunDetail, state literatureCheckpoint, candidates []literatureCandidate, prior map[string]literatureAssessment, fingerprints map[string]string) []literatureEvidenceRecord {
	notes := map[string]literatureNote{}
	executions := map[string]string{}
	byID := map[string]AIExecution{}
	for _, e := range detail.AIExecutions {
		byID[e.ID] = e
	}
	for _, id := range state.Executions {
		e, ok := byID[id]
		if !ok || e.Status != "completed" {
			continue
		}
		var result literatureScreening
		if json.Unmarshal(e.Output, &result) != nil {
			continue
		}
		for _, n := range result.EvidenceNotes {
			notes[n.CandidateID] = n
			executions[n.CandidateID] = id
		}
	}
	result := []literatureEvidenceRecord{}
	for _, c := range candidates {
		a, ok := prior[c.ID]
		if !ok {
			continue
		}
		hash := hashJSON(mustJSON(c))
		if f := fingerprints[c.ID]; f != "" && f != hash {
			continue
		}
		r := literatureEvidenceRecord{CandidateID: c.ID, Title: c.Title, SourceSHA256: hash, ExecutionID: executions[c.ID], Assessment: a}
		if note, ok := notes[c.ID]; ok && fingerprints[c.ID] == hash {
			r.Note = &note
		}
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CandidateID < result[j].CandidateID })
	return result
}

func completedLiteratureSummaries(detail RunDetail, state literatureCheckpoint) map[string]literatureSummary {
	keys := map[string]string{}
	for _, event := range detail.Events {
		if event.Type != literatureCheckpointEvent {
			continue
		}
		var p struct {
			ExecutionID string `json:"executionId"`
			Key         string `json:"synthesisKey"`
		}
		if json.Unmarshal(event.Payload, &p) == nil && p.Key != "" {
			keys[p.ExecutionID] = p.Key
		}
	}
	wanted := map[string]bool{}
	for _, id := range state.SynthesisExecutions {
		wanted[id] = true
	}
	result := map[string]literatureSummary{}
	for _, e := range detail.AIExecutions {
		if !wanted[e.ID] || e.Status != "completed" || keys[e.ID] == "" {
			continue
		}
		var output literatureScreening
		if json.Unmarshal(e.Output, &output) != nil {
			continue
		}
		result[keys[e.ID]] = literatureSummary{Key: keys[e.ID], ExecutionID: e.ID, Summary: output.Summary, Findings: output.Findings, Uncertainties: output.Uncertainties}
	}
	return result
}

func prepareLiteratureSynthesis(detail RunDetail, candidates []literatureCandidate, prior map[string]literatureAssessment, fingerprints map[string]string, args map[string]any, info literatureInput) (json.RawMessage, error) {
	contextKey := hashJSON(mustJSON(map[string]any{"researchContext": args["researchContext"], "publicationYears": frozenLiteratureYears(detail), "userRevision": args["_userRevision"]}))
	records := literatureRecords(detail, info.State, candidates, prior, fingerprints)
	for i := range records {
		// Detailed exclusion quotes remain in the original execution; the
		// title, decision and reason suffice to surface possible misexclusions.
		if records[i].Assessment.Decision == "exclude" {
			records[i].Note = nil
		}
	}
	info.Fingerprints = map[string]string{}
	for id := range info.State.RecheckedSources {
		if fingerprint := fingerprints[id]; fingerprint != "" {
			info.Fingerprints[id] = fingerprint
		}
	}
	delete(args, "candidates")
	delete(args, "excludedAssessments")
	delete(args, "priorAssessments")
	info.Phase = "coverage"
	args["_literature"] = info
	args["evidenceRecords"] = records
	args["partialDiscovery"] = info.State.SourceFailures
	if len(mustJSON(args)) <= 120*1024 {
		return literatureCoverageInput(args, info)
	}
	delete(args, "evidenceRecords")
	completed := completedLiteratureSummaries(detail, info.State)
	summaries := []literatureSummary{}
	groups, membership := stableLiteratureGroups(records, info.State.SynthesisGroups)
	info.State.SynthesisGroups = membership
	for _, batch := range groups {
		key := hashJSON(mustJSON(map[string]any{"context": contextKey, "records": batch}))
		ids := []string{}
		for _, r := range batch {
			ids = append(ids, r.CandidateID)
		}
		if summary, ok := completed[key]; ok {
			summary.CandidateIDs = ids
			for _, r := range batch {
				if r.Assessment.Decision == "exclude" {
					summary.ExcludedCandidateIDs = append(summary.ExcludedCandidateIDs, r.CandidateID)
				}
				if r.Assessment.Decision != "exclude" && (r.Assessment.Decision == "support" || r.Note == nil || r.Note.Uncertainty != "none") {
					summary.UncertainCandidateIDs = appendUnique(summary.UncertainCandidateIDs, r.CandidateID)
				}
			}
			summaries = append(summaries, summary)
		} else {
			info.Phase = "synthesis"
			info.Level = 1
			info.SynthesisKey = key
			args["_literature"] = info
			args["evidenceRecords"] = batch
			return boundedLiteratureInput(args)
		}
	}
	for level := 2; ; level++ {
		args["childSummaries"] = summaries
		info.Phase = "coverage"
		info.Level = level - 1
		info.SynthesisKey = ""
		args["_literature"] = info
		if len(mustJSON(args)) <= 160*1024 {
			return literatureCoverageInput(args, info)
		}
		if len(summaries) <= 1 {
			return nil, fmt.Errorf("literature synthesis record exceeds context safety budget; evidence remains saved")
		}
		parents := []literatureSummary{}
		for start := 0; start < len(summaries); {
			end, size := start, 0
			for end < len(summaries) && end-start < 4 {
				n := len(mustJSON(summaries[end]))
				if end-start >= 2 && size+n > 100*1024 {
					break
				}
				size += n
				end++
			}
			batch := summaries[start:end]
			key := hashJSON(mustJSON(map[string]any{"context": contextKey, "children": batch}))
			ids := []string{}
			for _, child := range batch {
				ids = appendUnique(ids, child.CandidateIDs...)
			}
			if summary, ok := completed[key]; ok {
				summary.CandidateIDs = ids
				for _, child := range batch {
					summary.ExcludedCandidateIDs = appendUnique(summary.ExcludedCandidateIDs, child.ExcludedCandidateIDs...)
					summary.UncertainCandidateIDs = appendUnique(summary.UncertainCandidateIDs, child.UncertainCandidateIDs...)
					for _, u := range child.Uncertainties {
						summary.UncertainCandidateIDs = appendUnique(summary.UncertainCandidateIDs, u.CandidateIDs...)
					}
				}
				parents = append(parents, summary)
			} else {
				info.Phase = "synthesis"
				info.Level = level
				info.SynthesisKey = key
				args["_literature"] = info
				args["childSummaries"] = batch
				return boundedLiteratureInput(args)
			}
			start = end
		}
		summaries = parents
	}
}

func (s *RuntimeService) finalizeLiteratureEvidence(ctx context.Context, detail RunDetail, info literatureInput, analysis *map[string]any) error {
	values, err := s.literature.LiteratureCandidates(ctx, detail.Run.ProjectID, info.State.QueryIDs)
	if err != nil {
		return err
	}
	candidates := literatureCandidates(values)
	prior, fingerprints := literaturePrior(detail, info.State)
	for _, c := range candidates {
		if _, ok := prior[c.ID]; !ok {
			if reason := automaticLiteratureExclusion(c, frozenLiteratureYears(detail)); reason != "" {
				prior[c.ID] = literatureAssessment{CandidateID: c.ID, Decision: "exclude", Relevance: "low", Reason: reason}
			}
		}
	}
	records := literatureRecords(detail, info.State, candidates, prior, fingerprints)
	recommended := map[string]bool{}
	for _, v := range stringSliceValue((*analysis)["recommendedCandidateIds"]) {
		recommended[v] = true
	}
	kept := []literatureCandidate{}
	assessments := []literatureAssessment{}
	for _, c := range candidates {
		a, ok := prior[c.ID]
		if fingerprints[c.ID] != "" && fingerprints[c.ID] != hashJSON(mustJSON(c)) {
			ok = false
		}
		if !ok {
			if recommended[c.ID] {
				return fmt.Errorf("recommended candidate was not screened")
			}
			continue
		}
		if recommended[c.ID] && a.Decision == "exclude" {
			return fmt.Errorf("excluded candidate must be reread before recommendation")
		}
		if a.Decision != "exclude" {
			kept = append(kept, c)
			assessments = append(assessments, a)
		}
	}
	// Selection cards need metadata, not another copy of every long abstract.
	for i := range kept {
		kept[i].SourceSHA256 = hashJSON(mustJSON(kept[i]))
		kept[i].Abstract = ""
	}
	(*analysis)["candidateAssessments"] = assessments
	manifest := []map[string]any{}
	for _, r := range records {
		manifest = append(manifest, map[string]any{"candidateId": r.CandidateID, "sourceSha256": r.SourceSHA256, "executionId": r.ExecutionID, "decision": r.Assessment.Decision, "reason": r.Assessment.Reason})
	}
	(*analysis)["evidenceManifest"] = manifest
	(*analysis)["synthesisExecutions"] = info.State.SynthesisExecutions
	(*analysis)["selectionCandidates"] = kept
	return nil
}

func stringSliceValue(v any) []string {
	result := []string{}
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		for _, v := range values {
			if s, ok := v.(string); ok {
				result = append(result, s)
			}
		}
	}
	return result
}

func (s *RuntimeService) ReadLiteratureCandidate(ctx context.Context, projectID, runID, stepID, candidateID string) (json.RawMessage, error) {
	detail, err := s.repository.GetRun(ctx, projectID, runID)
	if err != nil {
		return nil, err
	}
	step := findStep(detail.Steps, stepID)
	if step == nil || step.NodeID != "candidate_review" {
		return nil, fmt.Errorf("literature selection step not found")
	}
	var input struct {
		Candidates []literatureCandidate `json:"candidates"`
	}
	if json.Unmarshal(step.Input, &input) != nil {
		return nil, fmt.Errorf("selection snapshot invalid")
	}
	hash := ""
	for _, c := range input.Candidates {
		if c.ID == candidateID {
			if c.Abstract != "" {
				return mustJSON(c), nil
			}
			hash = c.SourceSHA256
		}
	}
	if hash == "" {
		return nil, fmt.Errorf("candidate not offered by this selection snapshot")
	}
	if s.literature == nil {
		return nil, fmt.Errorf("literature reader unavailable")
	}
	values, err := s.literature.LiteratureCandidates(ctx, projectID, literatureState(detail).QueryIDs)
	if err != nil {
		return nil, err
	}
	for _, c := range literatureCandidates(values) {
		if c.ID == candidateID && hashJSON(mustJSON(c)) == hash {
			return mustJSON(c), nil
		}
	}
	return nil, fmt.Errorf("original candidate snapshot no longer available; no newer content substituted")
}
