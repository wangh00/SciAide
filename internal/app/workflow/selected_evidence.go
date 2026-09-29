package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/tool"
	"regexp"
	"strings"
)

const selectedEvidenceVersion = "selected-evidence-v6"

var evidenceMarkerPattern = regexp.MustCompile(`\[K-[A-Za-z0-9_-]+\]`)

const selectedEvidenceOverviewInstruction = "整体分析已确认纳入的材料，直接形成报告可复用的发现、冲突和局限。不要求逐篇填固定研究要素表。按研究问题区分直接证据、背景、重复报告和待核验材料，所有文档须保留去向记录。只对关键结论、冲突或信息缺失按需调用只读资源工具深入核验，不逐篇机械调用。不把摘要或全文片段称为全文精读，不把多个报告算成独立试验。不得引用当前candidates之外的标记；工具读取用于核验，无法由当前签发依据支持的结论记为待核验而非伪造引用。direct=true时同时提交documentAnalyses，每篇只写简短发现或未使用原因、局限及支持引用；coverage使用已有分组分析，避免重复逐篇提取。证据足够推荐proceed，否则明确proceed_limited或范围变化，不自动联网补搜。"
const selectedEvidenceInstruction = "用户已确认纳入材料。本阶段按文献分析宿主提供的摘录，不是全文精读。逐篇填写documentAnalyses：documentId、finding（与本课题有关的发现或无法判断）、applicability（适用条件/方法及与课题的关系）、limitations（局限或缺失信息）、references（支持分析的真实签发引用）。不强制人群/干预/对照字段，按学科与课题需要分析。每份文档都必须记录，包括没有摘录的文档；无摘录时不得推断结果。按摘录sourceLevel区分全文片段、摘要、元数据，不把全文片段称为通读全文。同一研究的多篇报告不能重复计数。batch只分析当前文档；coverage依据逐篇分析跨文献综合冲突和限制，填写引用判定和覆盖；存在未解决的无摘录文档时不能声称完整覆盖。"

func selectedEvidenceSchema() json.RawMessage {
	s := decodeObject(evidenceScreeningSchema())
	p := s["properties"].(map[string]any)
	p["supplementalQueries"] = map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}}
	p["evidenceGaps"] = evidenceGapSchema()
	p["documentAnalyses"] = map[string]any{"type": "array", "maxItems": 5, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"documentId", "finding", "applicability", "limitations", "references"}, "properties": map[string]any{
		"documentId": map[string]any{"type": "string", "minLength": 1}, "finding": map[string]any{"type": "string", "minLength": 1, "maxLength": 900}, "applicability": map[string]any{"type": "string", "minLength": 1, "maxLength": 600}, "limitations": map[string]any{"type": "string", "minLength": 1, "maxLength": 600}, "references": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string"}},
	}}}
	p["citationAssessments"].(map[string]any)["maxItems"] = 300
	assessment := p["citationAssessments"].(map[string]any)["items"].(map[string]any)
	assessment["properties"].(map[string]any)["supportingQuote"] = map[string]any{"type": "string", "maxLength": 700}
	assessment["required"] = append(stringSliceValue(assessment["required"]), "supportingQuote")
	p["recommendedReferences"].(map[string]any)["maxItems"] = 100
	p["documentAnalyses"].(map[string]any)["maxItems"] = 100
	p["documentAnalyses"].(map[string]any)["items"].(map[string]any)["required"] = []string{"documentId", "finding", "limitations", "references"}
	return mustJSON(s)
}

type selectedEvidenceInput struct {
	Direct    bool              `json:"direct,omitempty"`
	Phase     string            `json:"phase"`
	Documents []string          `json:"documents"`
	Analyses  []json.RawMessage `json:"analyses"`
}

func prepareSelectedEvidence(detail RunDetail, input json.RawMessage) (json.RawMessage, error) {
	args := decodeObject(input)
	_, rounds := supplementaryEvidenceQueries(detail)
	args["localEvidenceSearchRoundsRemaining"] = max(0, 2-rounds)
	args["localEvidenceSearchQueries"], _ = supplementaryEvidenceQueries(detail)
	docs := stringSliceValue(args["documentIds"])
	docs = appendUnique(nil, docs...)
	if len(docs) == 0 || len(docs) > 100 {
		return nil, fmt.Errorf("selected evidence requires 1-100 indexed documents")
	}
	step := findNodeStep(detail.Steps, "evidence_screening")
	done := map[string]bool{}
	analyses := []json.RawMessage{}
	if step != nil {
		for _, e := range detail.AIExecutions {
			if e.WorkflowStepID != step.ID || e.Status != "completed" {
				continue
			}
			// Only outputs explicitly checkpointed for this step and input identity are reused.
			valid := false
			for _, ev := range detail.Events {
				var frozen map[string]json.RawMessage
				if ev.Type != "workflow.selected_evidence_batch" {
					continue
				}
				json.Unmarshal(ev.Payload, &frozen)
				var id string
				json.Unmarshal(frozen["executionId"], &id)
				var key string
				json.Unmarshal(frozen["sourceKey"], &key)
				if id == e.ID && key == hashJSON(input) {
					valid = true
				}
			}
			if !valid {
				continue
			}
			var out struct {
				Documents []json.RawMessage `json:"documentAnalyses"`
			}
			json.Unmarshal(e.Output, &out)
			for _, a := range out.Documents {
				var n struct {
					ID string `json:"documentId"`
				}
				json.Unmarshal(a, &n)
				if !done[n.ID] {
					done[n.ID] = true
					analyses = append(analyses, a)
				}
			}
		}
	}
	pending := []string{}
	for _, id := range docs {
		if !done[id] {
			pending = append(pending, id)
		}
	}
	info := selectedEvidenceInput{Phase: "coverage", Documents: docs, Analyses: analyses}
	// Small collections are analyzed once, with original excerpts and a complete
	// document inventory. No fixed per-document model request is scheduled.
	if len(done) == 0 && len(input) <= 120*1024 {
		info.Direct = true
		args["_selectedEvidence"] = info
		args["sourceKey"] = hashJSON(input)
		return boundedLiteratureInput(args)
	}
	if len(pending) > 0 {
		info.Phase = "batch"
		info.Documents = nil
		info.Analyses = nil
		var citations []tool.CitationRef
		json.Unmarshal(mustJSON(args["candidates"]), &citations)
		kept := []tool.CitationRef{}
		for _, id := range pending {
			addition := []tool.CitationRef{}
			for _, c := range citations {
				if c.DocumentID == id {
					addition = append(addition, c)
				}
			}
			trial := append(append([]tool.CitationRef{}, kept...), addition...)
			info.Documents = append(info.Documents, id)
			args["candidates"] = trial
			args["_selectedEvidence"] = info
			if len(mustJSON(args)) > 120*1024 {
				info.Documents = info.Documents[:len(info.Documents)-1]
				break
			}
			kept = trial
		}
		if len(info.Documents) == 0 {
			return nil, fmt.Errorf("one selected document exceeds analysis context budget; source retained")
		}
		args["candidates"] = kept
	}
	args["_selectedEvidence"] = info
	if info.Phase == "coverage" {
		var citations []tool.CitationRef
		json.Unmarshal(mustJSON(args["candidates"]), &citations)
		compact := []map[string]any{}
		for _, c := range citations {
			compact = append(compact, map[string]any{"reference": c.Reference, "documentId": c.DocumentID, "sourceName": c.SourceName, "quote": c.Quote, "title": c.Title})
		}
		args["candidates"] = compact
	}
	args["sourceKey"] = hashJSON(input)
	return boundedLiteratureInput(args)
}

func findNodeStep(steps []Step, id string) *Step {
	for i := range steps {
		if steps[i].NodeID == id {
			return &steps[i]
		}
	}
	return nil
}

func selectedEvidencePhaseNode(node CompiledNode, input json.RawMessage) CompiledNode {
	if node.ID != "evidence_screening" || node.PromptVersion != selectedEvidenceVersion {
		return node
	}
	var in struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	json.Unmarshal(input, &in)
	s := decodeObject(node.OutputSchema)
	p := s["properties"].(map[string]any)
	if in.Info.Phase == "batch" {
		s["properties"] = map[string]any{"documentAnalyses": p["documentAnalyses"]}
		s["required"] = []string{"documentAnalyses"}
	} else if in.Info.Direct {
		s["required"] = appendUnique(stringSliceValue(s["required"]), "documentAnalyses")
	} else {
		delete(p, "documentAnalyses")
	}
	node.Prompt = selectedEvidenceOverviewInstruction
	node.Prompt += "\n对于已经索引的本地全文，当前检索片段未出现不代表原文未报告。关键结果、方法、适用人群或可信度缺失时，提供 supplementalQueries（1至4个简短学术检索式，针对缺项且保留研究主题）；宿主最多两轮在同一批所选文档中补查并重新签发引用，再进入人工确认。不要请求重新上传已存在的文件。补查额度耗尽后明确尚未核验和具体缺口，不得编造数据或声称已完成原始任务。单篇文献任务不以文献篇数不足判定失败，按要求的字段覆盖判断。"
	if in.Info.Phase != "batch" && schemaDeclaresProperty(node.OutputSchema, "evidenceGaps") {
		node.Prompt += "\n" + evidenceGapInstruction
	}
	node.Prompt += "\nmaterialOrigin=user_selected 表示用户指定的资料，不代表相关、可信或已获得全文。按原文判断采用、背景或排除，并在documentAnalyses中说明。没有candidateId的资料不能调用builtin.research.full_text.read；仅使用当前索引签发的引用，不能凭文件名编造题录。"
	node.Prompt += "\n初始材料可能是摘要，也可能是已经上传的全文；按材料来源与实际原文判断，不预设只有摘要。仅关键结论、冲突或缺失方法/结果需要时调用builtin.research.full_text.read，candidateId从importedMaterials逐字取，query指定要核验的具体结果词。fullTextAvailability=unavailable的材料不要调用；requestable_not_verified仅表示存在可尝试入口，不表示已获得全文。单篇一次，返回status=unavailable时核验未完成，保留原证据限制并继续其他材料，不重复请求。来源失败后继续其他材料并披露限制，不循环下载。全文成功后保存为本任务新材料，旧附件和冻结引用不覆盖。宿主会自动从材料同步与引用检索重新进入综合，不重新检索候选。当前工具片段尚未签发新[K]引用，不用旧摘要引用支持全文新增结论；等待更新后提供的新引用。importKind=full_text表示已经保存全文，改用当前资源读取，不重复下载。"
	node.Prompt += "\n每条推荐引用的supportingQuote必须逐字摘录当前candidate.quote中直接支持reason的连续原句，不能用同文档其他块替代。仅题名作者DOI等题录不得推荐为效果依据。缺关键统计先核查已提供的完整块，不把界面短片段的截断当来源缺失。sourceLevel按当前片段实际内容判断，不按PDF扩展名。独立研究数需要跨文档试验身份核对；文档数不是独立试验数。"
	node.Prompt += "\n最终citationAssessments只评估实际推荐的引用及需要指出问题的引用，不逐条重写全部检索摘录。推荐标记必须有对应非exclude判定。无引用的文档记录明确说明未用于结论的原因。"
	if in.Info.Phase == "batch" {
		node.Prompt += "\n本次材料超出单次预算，只整理当前分组；documentAnalyses逐文档仅记录相关发现或未使用原因、局限及引用，不必填写统一研究要素表。"
	}
	node.OutputSchema = mustJSON(s)
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	return node
}

func validateSelectedEvidence(output, input json.RawMessage) error {
	var in struct {
		Info       selectedEvidenceInput `json:"_selectedEvidence"`
		Candidates []tool.CitationRef    `json:"candidates"`
	}
	json.Unmarshal(input, &in)
	if in.Info.Direct {
		copyInput := decodeObject(input)
		info := in.Info
		info.Direct = false
		info.Phase = "batch"
		copyInput["_selectedEvidence"] = info
		if err := validateSelectedEvidence(output, mustJSON(copyInput)); err != nil {
			return err
		}
		var records struct {
			Analyses []json.RawMessage `json:"documentAnalyses"`
		}
		json.Unmarshal(output, &records)
		in.Info.Analyses = records.Analyses
	}
	allowedMarkers := map[string]bool{}
	for _, c := range in.Candidates {
		allowedMarkers[c.Reference] = true
	}
	for _, marker := range evidenceMarkerPattern.FindAllString(string(output), -1) {
		if !allowedMarkers[marker] {
			return fmt.Errorf("unissued evidence marker: %s", marker)
		}
	}
	if in.Info.Phase != "batch" {
		if err := validateEvidenceGaps(output, input); err != nil {
			return err
		}
		// Unused background material need not block a claim supported by other
		// evidence; every document must still disclose its disposition.
		var result struct {
			References  []string `json:"recommendedReferences"`
			Assessments []struct {
				Reference       string `json:"reference"`
				Decision        string `json:"decision"`
				SupportingQuote string `json:"supportingQuote"`
				SourceLevel     string `json:"sourceLevel"`
			} `json:"citationAssessments"`
			Recommendation string `json:"recommendation"`
		}
		if json.Unmarshal(output, &result) != nil {
			return fmt.Errorf("invalid evidence overview")
		}
		offered := map[string]bool{}
		for _, c := range in.Candidates {
			offered[c.Reference] = true
		}
		assessed := map[string]string{}
		byRef := map[string]tool.CitationRef{}
		for _, c := range in.Candidates {
			byRef[c.Reference] = c
		}
		for _, a := range result.Assessments {
			if !offered[a.Reference] || assessed[a.Reference] != "" {
				return fmt.Errorf("unknown or duplicate citation assessment")
			}
			assessed[a.Reference] = a.Decision
			if a.Decision != "exclude" {
				q := strings.TrimSpace(a.SupportingQuote)
				c := byRef[a.Reference]
				if q == "" || !strings.Contains(c.Quote, q) {
					return &CitationQuoteError{Reference: a.Reference, SupportingQuote: a.SupportingQuote, CandidateQuote: c.Quote}
				}
				if strings.Contains(c.Quote, "## Bibliographic metadata") && !strings.Contains(c.Quote, "## Abstract") {
					return fmt.Errorf("citation %s contains bibliographic metadata, not result evidence", a.Reference)
				}
				if strings.Contains(strings.ToLower(c.SourceName), "-metadata.") && a.SourceLevel == "full_text" {
					return fmt.Errorf("metadata/abstract material cannot be classified as full text")
				}
				if at := strings.Index(c.Quote, "## Abstract"); at >= 0 && strings.Contains(c.Quote, "## Bibliographic metadata") && !strings.Contains(c.Quote[at+len("## Abstract"):], q) {
					return fmt.Errorf("supporting quote is bibliographic, not abstract content")
				}
			}
		}
		seen := map[string]bool{}
		for _, r := range result.References {
			if !offered[r] || seen[r] || assessed[r] == "" || assessed[r] == "exclude" {
				return fmt.Errorf("recommended citation must have an eligible source assessment")
			}
			seen[r] = true
		}
		if result.Recommendation == "proceed" && len(seen) == 0 {
			return fmt.Errorf("proceed requires supported citations")
		}
		return nil
	}
	var out struct {
		Documents []struct {
			ID         string   `json:"documentId"`
			References []string `json:"references"`
		} `json:"documentAnalyses"`
	}
	if json.Unmarshal(output, &out) != nil {
		return fmt.Errorf("invalid document analyses")
	}
	offered := map[string]bool{}
	for _, id := range in.Info.Documents {
		offered[id] = true
	}
	refs := map[string]string{}
	for _, c := range in.Candidates {
		refs[c.Reference] = c.DocumentID
	}
	for _, a := range out.Documents {
		if !offered[a.ID] {
			return fmt.Errorf("unknown or duplicate document analysis")
		}
		delete(offered, a.ID)
		for _, r := range a.References {
			if refs[r] != a.ID {
				return fmt.Errorf("analysis citation belongs to another document")
			}
		}
	}
	if len(offered) > 0 {
		return fmt.Errorf("every selected document requires analysis")
	}
	return nil
}

func (s *RuntimeService) completeSelectedEvidenceBatch(ctx context.Context, detail RunDetail, step Step, e AIExecution) error {
	var in struct {
		Key string `json:"sourceKey"`
	}
	json.Unmarshal(step.Input, &in)
	repo, ok := s.repository.(literatureContinuationRepository)
	if !ok {
		return fmt.Errorf("continuation unavailable")
	}
	event, err := s.event(detail.Run.ID, "workflow.selected_evidence_batch", map[string]any{"stepId": step.ID, "executionId": e.ID, "sourceKey": in.Key}, s.now())
	if err != nil {
		return err
	}
	return repo.QueueLiteratureContinuation(ctx, detail.Run.ID, step.ID, "", step.Attempt, s.now(), event)
}
