package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

const selectedEvidenceVersion = "selected-evidence-v6"

var evidenceMarkerPattern = regexp.MustCompile(`\[K-[A-Za-z0-9_-]+\]`)

const selectedEvidenceOverviewInstruction = "整体分析已确认纳入的材料，直接形成报告可复用的发现、冲突和局限。不要求逐篇填固定研究要素表。按研究问题区分直接证据、背景、重复报告和待核验材料，所有文档须保留去向记录。只对关键结论、冲突或信息缺失按需调用只读资源工具深入核验，不逐篇机械调用。不把摘要或全文片段称为全文精读，不把多个报告算成独立试验。不得引用当前candidates之外的标记；工具读取用于核验，无法由当前签发依据支持的结论记为待核验而非伪造引用。direct=true时同时提交documentAnalyses，每篇只写简短发现或未使用原因、局限及支持引用；coverage使用已有分组分析，避免重复逐篇提取。证据足够推荐proceed，否则明确proceed_limited或范围变化，不自动联网补搜。"
const selectedEvidenceInstruction = "用户已确认纳入材料。本阶段按文献分析宿主提供的摘录，不是全文精读。逐篇填写documentAnalyses：documentId、finding（与本课题有关的发现或无法判断）、applicability（适用条件/方法及与课题的关系）、limitations（局限或缺失信息）、references（支持分析的真实签发引用）。不强制人群/干预/对照字段，按学科与课题需要分析。每份文档都必须记录，包括没有摘录的文档；无摘录时不得推断结果。按摘录sourceLevel区分全文片段、摘要、元数据，不把全文片段称为通读全文。同一研究的多篇报告不能重复计数。batch只分析当前文档；coverage依据逐篇分析跨文献综合冲突和限制，填写引用判定和覆盖；存在未解决的无摘录文档时不能声称完整覆盖。"
const selectedEvidenceBatchInstruction = "本次只整理当前分组 _selectedEvidence.documents 中的文档，documentIds、documentCoverage 和 importedMaterials 也仅供本组使用，不是全部纳入清单。documentAnalyses 必须为本组每个 documentId 各记录一次，逐字使用宿主 ID，不添加其他文档、不重复此前分组。每篇仅记录与课题有关的简短发现或未用于结论的原因、局限和支持引用；不强制统一研究要素表。没有摘录的文档仍须记录，references 可为空，但不得推断其研究结果。引用只能来自当前 candidates 或本轮成功核读工具实际签发的原文标记，且必须属于该文档。按实际原文区分摘要、全文片段和题录，不把片段称为通读全文，不把同一试验的多篇报告计为独立试验。只提交 documentAnalyses；全局综合、推荐引用、覆盖判断和补查请求在全部分组完成后处理，本组不输出这些字段。"

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
	sourceKey := selectedEvidenceSourceKey(detail, input)
	args["sourceKey"] = sourceKey
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
		analyses = acceptedEvidenceAnalyses(detail, *step, input, sourceKey)
		for _, row := range analyses {
			var doc struct {
				ID string `json:"documentId"`
			}
			_ = json.Unmarshal(row, &doc)
			done[doc.ID] = true
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
	if len(done) == 0 {
		info.Direct = true
		args["_selectedEvidence"] = info
		if len(mustJSON(args)) <= 120*1024 {
			return boundedLiteratureInput(args)
		}
		info.Direct = false
	}
	if len(pending) > 0 {
		info.Phase = "batch"
		info.Documents = nil
		info.Analyses = nil
		var citations []tool.CitationRef
		json.Unmarshal(mustJSON(args["candidates"]), &citations)
		kept := []tool.CitationRef{}
		var batch map[string]any
		for _, id := range pending {
			addition := []tool.CitationRef{}
			for _, c := range citations {
				if c.DocumentID == id {
					addition = append(addition, c)
				}
			}
			trial := append(append([]tool.CitationRef{}, kept...), addition...)
			trialInfo := info
			trialInfo.Documents = append(append([]string{}, info.Documents...), id)
			projected := projectSelectedEvidenceBatch(detail, args, trialInfo.Documents, trial)
			projected["_selectedEvidence"] = trialInfo
			if len(mustJSON(projected)) > 120*1024 {
				break
			}
			info, batch = trialInfo, projected
			kept = trial
		}
		if len(info.Documents) == 0 {
			return nil, fmt.Errorf("one selected document exceeds analysis context budget; source retained")
		}
		args = batch
	}
	args["_selectedEvidence"] = info
	if info.Phase == "coverage" {
		if canReadEvidenceWork(compilationNodeMap(detail.Run.Compilation)["evidence_screening"]) {
			args["_evidenceWorkPaging"] = evidenceWorkPagingVersion
		}
		var citations []tool.CitationRef
		json.Unmarshal(mustJSON(args["candidates"]), &citations)
		// Window rotation must not leave accepted analyses referring to absent
		// sources. Restore only their exact, previously issued source excerpts;
		// the normal seed resolver still verifies them against successful tools.
		used, present := map[string]bool{}, map[string]bool{}
		for _, analysis := range analyses {
			for _, marker := range evidenceMarkerPattern.FindAllString(string(analysis), -1) {
				used[marker] = true
			}
		}
		for _, ref := range citations {
			present[ref.Reference] = true
		}
		sources := indexedEvidenceSnapshots(detail)
		if search := findNodeStep(detail.Steps, "evidence_search"); search != nil && search.Status == StepCompleted {
			var original struct {
				Candidates []tool.CitationRef `json:"citations"`
			}
			_ = json.Unmarshal(search.Output, &original)
			sources = append(sources, original.Candidates...)
		}
		for _, ref := range sources {
			if used[ref.Reference] && !present[ref.Reference] {
				citations = append(citations, ref)
				present[ref.Reference] = true
			}
		}
		if len(citations) > citation.MaxSeededKnowledgeCitations {
			kept := []tool.CitationRef{}
			for _, ref := range citations {
				if used[ref.Reference] {
					kept = append(kept, ref)
				}
			}
			if len(kept) > citation.MaxSeededKnowledgeCitations {
				return nil, fmt.Errorf("accepted document analyses exceed the existing citation seed capacity; sources and analyses remain saved")
			}
			for _, ref := range citations {
				if !used[ref.Reference] && len(kept) < citation.MaxSeededKnowledgeCitations {
					kept = append(kept, ref)
				}
			}
			citations = kept
		}
		compact := []map[string]any{}
		for _, c := range citations {
			compact = append(compact, map[string]any{"reference": c.Reference, "documentId": c.DocumentID, "sourceName": c.SourceName, "quote": c.Quote, "title": c.Title})
		}
		args["candidates"] = compact
	}
	return boundedSelectedEvidenceInput(args)
}

// Progress belongs to the frozen research question, corrections and source
// dependencies, not to the rotating model window. Tool-read events may reorder
// excerpts/ranges or evict old chunks without invalidating accepted analyses.
// A new search/index output or changed research/revision context still changes
// this identity. Original inputs, reads and model outputs remain untouched.
func selectedEvidenceSourceKey(detail RunDetail, input json.RawMessage) string {
	args := decodeObject(input)
	for _, key := range []string{"_selectedEvidence", "_evidenceWorkPaging", "researchRecordFacts", "sourceKey", "indexedEvidenceWorkingSet", "indexedReadRanges", "localEvidenceSearchQueries", "localEvidenceSearchRoundsRemaining"} {
		delete(args, key)
	}
	if search := findNodeStep(detail.Steps, "evidence_search"); search != nil && search.Status == StepCompleted {
		var original struct {
			Candidates []tool.CitationRef `json:"citations"`
		}
		if json.Unmarshal(search.Output, &original) == nil {
			args["candidates"] = original.Candidates
		}
		args["_searchSourceSha256"] = hashJSON(search.Output)
	}
	if sync := findNodeStep(detail.Steps, "evidence_sync"); sync != nil && sync.Status == StepCompleted {
		args["_indexedSourceSha256"] = hashJSON(sync.Output)
	}
	return hashJSON(mustJSON(args))
}

// Keep every model-facing inventory on the same batch. The complete source
// inventory lives in dependencies/audit and returns for final coverage; no
// material is excluded and no original quotation is shortened here.
func projectSelectedEvidenceBatch(detail RunDetail, args map[string]any, documents []string, citations []tool.CitationRef) map[string]any {
	projected := make(map[string]any, len(args))
	for key, value := range args {
		projected[key] = value
	}
	allowed := stringSetOf(documents)
	attachments := map[string]bool{}
	for _, ref := range citations {
		attachments[ref.AttachmentID] = true
	}
	if sync := findNodeStep(detail.Steps, "evidence_sync"); sync != nil && sync.Status == StepCompleted {
		var indexed struct {
			Structured struct {
				Documents []struct {
					ID           string `json:"id"`
					AttachmentID string `json:"attachmentId"`
				} `json:"documents"`
			} `json:"structured"`
		}
		_ = json.Unmarshal(sync.Output, &indexed)
		for _, document := range indexed.Structured.Documents {
			if allowed[document.ID] {
				attachments[document.AttachmentID] = true
			}
		}
	}
	projected["documentIds"], projected["candidates"] = documents, citations
	for _, key := range []string{"documentCoverage", "indexedReadRanges"} {
		if rows, exists := args[key]; exists {
			var decoded []map[string]any
			_ = json.Unmarshal(mustJSON(rows), &decoded)
			kept := []map[string]any{}
			for _, row := range decoded {
				if id, ok := row["documentId"].(string); ok && allowed[id] {
					kept = append(kept, row)
				}
			}
			projected[key] = kept
		}
	}
	if rows, exists := args["importedMaterials"]; exists {
		var decoded []map[string]any
		_ = json.Unmarshal(mustJSON(rows), &decoded)
		kept, candidates := []map[string]any{}, []string{}
		for _, row := range decoded {
			attachmentID, _ := row["attachmentId"].(string)
			documentID, _ := row["documentId"].(string)
			if attachmentID != "" && attachments[attachmentID] || documentID != "" && allowed[documentID] {
				kept = append(kept, row)
				if id, ok := row["candidateId"].(string); ok && id != "" {
					candidates = appendUnique(candidates, id)
				}
			}
		}
		projected["importedMaterials"] = kept
		if _, exists := args["selectedCandidateIds"]; exists {
			projected["selectedCandidateIds"] = candidates
		}
	}
	if working, exists := args["indexedEvidenceWorkingSet"]; exists {
		value := decodeObject(mustJSON(working))
		value["offeredChunkCount"] = len(citations)
		projected["indexedEvidenceWorkingSet"] = value
	}
	return projected
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
	// Optional, discipline-neutral work product. Ordinary evidence screening
	// stays short; an assigned appraisal/mapping correction can now carry its
	// actual table rather than compressing it into a 600-character limitation.
	if doc, ok := p["documentAnalyses"].(map[string]any); ok {
		if item, ok := doc["items"].(map[string]any); ok {
			if props, ok := item["properties"].(map[string]any); ok {
				props["workProduct"] = map[string]any{"type": "string", "maxLength": 12000, "description": "仅按本轮要求填写已实际完成的评价、关系核对或其他研究工作，可用Markdown表格；不是未来计划。未知部分逐项说明及给出依据，不要求每篇都填。"}
			}
		}
	}
	if in.Info.Phase == "batch" {
		s["properties"] = map[string]any{"documentAnalyses": p["documentAnalyses"]}
		s["required"] = []string{"documentAnalyses"}
	} else if in.Info.Direct {
		s["required"] = appendUnique(stringSliceValue(s["required"]), "documentAnalyses")
	}
	node.Prompt = strings.Replace(selectedEvidenceOverviewInstruction, "不得引用当前candidates之外的标记；工具读取用于核验，无法由当前签发依据支持的结论记为待核验而非伪造引用。", "引用只能来自当前candidates或同一轮成功原文核读工具新签发的标记；新工具结果已实际读取，可直接用于本轮完整综合，宿主将验证并冻结其来源。无法由这些实际签发依据支持的结论记为待核验而非伪造引用。", 1)
	if in.Info.Phase == "batch" {
		node.Prompt = selectedEvidenceBatchInstruction
	} else {
		node.Prompt += "\n_selectedEvidence.analyses 是此前已保存的分组分析，不是已经验证的科学事实。结合当前原文引用以及本轮新核读处理冲突、缺口或错误，不因复用分组记录忽略新证据；最终整体判断仍接受独立审查。"
		node.Prompt += "\n对于已经索引的本地全文，当前检索片段未出现不代表原文未报告。关键结果、方法、适用人群或可信度缺失时，提供 supplementalQueries（1至4个简短学术检索式，针对缺项且保留研究主题）；宿主最多两轮在同一批所选文档中补查并重新签发引用，再进入人工确认。不要请求重新上传已存在的文件。补查额度耗尽后明确尚未核验和具体缺口，不得编造数据或声称已完成原始任务。单篇文献任务不以文献篇数不足判定失败，按要求的字段覆盖判断。"
	}
	if in.Info.Phase != "batch" && schemaDeclaresProperty(node.OutputSchema, "evidenceGaps") {
		gapInstruction := evidenceGapInstruction
		if strings.Contains(string(node.OutputSchema), `"not_discussed_in_checked_scope"`) {
			gapInstruction = strings.Replace(gapInstruction, "或 explicitly_not_reported（原文明确说未报告）", "、explicitly_not_reported（原文明确说未报告）或 not_discussed_in_checked_scope（实际核读块范围内未讨论）", 1)
			gapInstruction = strings.Replace(gapInstruction, "补查后仍缺少核心字段时 coverage 必须不足", "核心字段尚未核验时 coverage 必须不足；比较维度在核读范围内未讨论不等于任务必然失败，是否满足原始要求由有边界结论和独立审查判定", 1)
		}
		node.Prompt += "\n" + gapInstruction
		node.Prompt += "\n本次输出schema若支持not_discussed_in_checked_scope，可对已核读的明确页章范围填写checkedScope（documentId、locators）和assessment（该维度为何在范围内未讨论及核查限制）。这不是作者明示缺失，不需要编造‘我们未报告’原句；不可据零命中、旧摘录或补查额度耗尽使用该状态。比较维度不等于要求每篇都有该机制；对不适用/未讨论的维度给有边界结论，由独立审查核对实际核读记录，不假装通读全文。旧schema不支持该状态时仍按其契约提交，不扩大旧任务权限。"
	}
	node.Prompt += "\nindexedReadRanges是宿主记录的实际原文块范围，checkedScope仅涵盖这些已返回块，不代表整页或整章全部读完。每个documentId/locator都应能在该记录中找到；若需整页核查，继续hasMore提供的操作直到相应页章读取结束。工作集有预算时完整核读记录仍保留，不以候选省略或关键词零命中证明全文缺失。"
	node.Prompt += "\n返修要求具体评价或关系核对时，不以重复摘要/局限或写入未来计划代替。对本组相关文档使用可选documentAnalyses.workProduct保存实际完成的领域判断、关系映射、依据与逐项未知原因；只做要求的工作，不为无关文档机械填表，不把随机设计自动判成低偏倚风险。缺少决定性信息且有可用读取入口时核读相应材料；已明确不可得不反复下载。coverage沿用这些已保存工作，明确哪些整改有实际产物、哪些仍缺材料/需要范围决策，不要求模型复制全套分组记录。覆盖综合中的可选documentAnalyses仅提交确需修正的逐文档记录（完整替换该文档），其他已保存记录保持不变；只修正综合层解释时不要重写正确的逐文档分析。复用不是认定旧判断正确，当前原文或审查指出逐文档错误时必须在这些记录中实际修正，不把错误继续交给下游。"
	node.Prompt += "\nmaterialOrigin=user_selected 表示用户指定的资料，不代表相关、可信或已获得全文。缺项时优先从资源菜单查看已选资料结构，核读相关页章及上下文；本轮成功核读签发的引用可直接与当前候选一起支持完整综合，宿主一同冻结证据与结果，不因候选预算淘汰后重读而重复整阶段。只有不完整采证草稿且确有此前未核读的新块才继续采证。只读相关范围，不机械通读所有附件；两轮关键词补查额度不限制已授权页章核读。没有candidateId的资料不能请求重新下载。不能凭文件名编造题录。"
	node.Prompt += "\n初始材料可能是摘要，也可能是已经上传的全文；按材料来源与实际原文判断，不预设只有摘要。仅关键结论、冲突或缺失方法/结果需要时调用builtin.research.full_text.read，candidateId从importedMaterials逐字取，query指定要核验的具体结果词。fullTextAvailability=unavailable的材料不要调用；requestable_not_verified仅表示存在可尝试入口，不表示已获得全文。单篇一次，返回status=unavailable时核验未完成，保留原证据限制并继续其他材料，不重复请求。来源失败后继续其他材料并披露限制，不循环下载。全文成功后保存为本任务新材料，旧附件和冻结引用不覆盖。宿主会自动从材料同步与引用检索重新进入综合，不重新检索候选。当前工具片段尚未签发新[K]引用，不用旧摘要引用支持全文新增结论；等待更新后提供的新引用。importKind=full_text表示已经保存全文，改用当前资源读取，不重复下载。"
	if in.Info.Phase != "batch" {
		node.Prompt += "\n每条推荐引用的supportingQuote必须逐字摘录当前candidate.quote中直接支持reason的连续原句，不能用同文档其他块替代。仅题名作者DOI等题录不得推荐为效果依据。缺关键统计先核查已提供的完整块，不把界面短片段的截断当来源缺失。sourceLevel按当前片段实际内容判断，不按PDF扩展名。独立研究数需要跨文档试验身份核对；文档数不是独立试验数。"
		node.Prompt += "\n最终citationAssessments只评估实际推荐的引用及需要指出问题的引用，不逐条重写全部检索摘录。推荐标记必须有对应非exclude判定。无引用的文档记录明确说明未用于结论的原因。"
		node.Prompt += "\n提交前对照 recommendedReferences 与 citationAssessments 的实际 reference：推荐的每一个标记都须有且仅有一条同标记的非exclude判定及该摘录的支持原句。同一文档不同标记不是可互换的依据，已有 documentAnalyses 也不能替代最终判定。若某个推荐缺少对应条目，自行检查原文后补齐能支持结论的判定，或删除不成立的推荐并调整相关结论；不要为了补齐数量自动设为support，也不要重复列出同一标记。"
	}
	node.OutputSchema = mustJSON(s)
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	if usesCitationSourceProtocol(node) {
		node.Prompt = strings.Replace(node.Prompt, "每条推荐引用的supportingQuote必须逐字摘录当前candidate.quote中直接支持reason的连续原句", "每条推荐引用须用quoteSelection选择当前精确来源中直接支持reason的连续原句，由宿主复制supportingQuote", 1)
	}
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
		var updates struct {
			Documents []json.RawMessage `json:"documentAnalyses"`
		}
		if !in.Info.Direct && json.Unmarshal(output, &updates) == nil && len(updates.Documents) > 0 {
			ids := []string{}
			allowed := stringSetOf(in.Info.Documents)
			for _, row := range updates.Documents {
				var doc struct {
					ID string `json:"documentId"`
				}
				_ = json.Unmarshal(row, &doc)
				if !allowed[doc.ID] {
					return fmt.Errorf("coverage document analysis is outside the current inventory: %s", doc.ID)
				}
				ids = append(ids, doc.ID)
			}
			batch := decodeObject(input)
			batch["_selectedEvidence"] = selectedEvidenceInput{Phase: "batch", Documents: ids}
			if err := validateSelectedEvidence(output, mustJSON(batch)); err != nil {
				return err
			}
		}
		scopeErr := validateEvidenceGaps(output, input)
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
		quoteIssues := []CitationQuoteError{}
		assessmentIssues := []CitationAssessmentIssue{}
		assessmentCounts := map[string]int{}
		for i, a := range result.Assessments {
			path := fmt.Sprintf("citationAssessments[%d]", i)
			assessmentCounts[a.Reference]++
			if !offered[a.Reference] {
				assessmentIssues = append(assessmentIssues, CitationAssessmentIssue{Path: path + ".reference", Reference: a.Reference, Problem: "reference is not an offered source; use only current issued citations"})
				continue
			}
			if assessmentCounts[a.Reference] > 1 {
				assessmentIssues = append(assessmentIssues, CitationAssessmentIssue{Path: path + ".reference", Reference: a.Reference, Problem: "duplicate citation assessment; keep one evidence-based decision for this exact marker"})
			} else {
				assessed[a.Reference] = a.Decision
			}
			if a.Decision != "exclude" {
				q := strings.TrimSpace(a.SupportingQuote)
				c := byRef[a.Reference]
				quoteMatches := q != "" && strings.Contains(c.Quote, q)
				if !quoteMatches {
					quoteIssues = append(quoteIssues, citationQuoteIssue(c, a.SupportingQuote, in.Candidates))
				}
				if strings.Contains(c.Quote, "## Bibliographic metadata") && !strings.Contains(c.Quote, "## Abstract") {
					assessmentIssues = append(assessmentIssues, CitationAssessmentIssue{Path: path + ".decision", Reference: a.Reference, Problem: "citation contains bibliographic metadata, not result evidence; do not recommend it as result support"})
				}
				if strings.Contains(strings.ToLower(c.SourceName), "-metadata.") && a.SourceLevel == "full_text" {
					assessmentIssues = append(assessmentIssues, CitationAssessmentIssue{Path: path + ".sourceLevel", Reference: a.Reference, Problem: "metadata/abstract material cannot be classified as full text"})
				}
				if at := strings.Index(c.Quote, "## Abstract"); quoteMatches && at >= 0 && strings.Contains(c.Quote, "## Bibliographic metadata") && !strings.Contains(c.Quote[at+len("## Abstract"):], q) {
					assessmentIssues = append(assessmentIssues, CitationAssessmentIssue{Path: path + ".supportingQuote", Reference: a.Reference, Problem: "supporting quote is bibliographic, not abstract content; use actual abstract evidence"})
				}
			}
		}
		seen := map[string]bool{}
		for i, r := range result.References {
			path := fmt.Sprintf("recommendedReferences[%d]", i)
			problem := ""
			switch {
			case !offered[r]:
				problem = "recommended citation is not an offered source"
			case seen[r]:
				problem = "duplicate recommended citation; list this exact marker once"
			case assessed[r] == "":
				problem = "recommended citation has no corresponding citationAssessments entry; assess this exact source with a supported reason and quote, or remove the unsupported recommendation"
			case assessed[r] == "exclude":
				problem = "recommended citation has an exclude decision; remove the recommendation or explicitly reassess the source with actual evidence"
			}
			if problem != "" {
				assessmentIssues = append(assessmentIssues, CitationAssessmentIssue{Path: path, Reference: r, Problem: problem})
			}
			seen[r] = true
		}
		if result.Recommendation == "proceed" && len(seen) == 0 {
			assessmentIssues = append(assessmentIssues, CitationAssessmentIssue{Path: "recommendation", Problem: "proceed requires supported citations; a bounded result without result evidence must not claim proceed"})
		}
		if len(assessmentIssues) > 0 {
			return evidenceSubmissionErrors(scopeErr, &CitationAssessmentErrors{Issues: assessmentIssues, QuoteIssues: quoteIssues})
		}
		if len(quoteIssues) == 1 {
			return evidenceSubmissionErrors(scopeErr, &quoteIssues[0])
		}
		if len(quoteIssues) > 1 {
			return evidenceSubmissionErrors(scopeErr, &CitationQuoteErrors{Issues: quoteIssues})
		}
		return scopeErr
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
	seen := map[string]bool{}
	for i, a := range out.Documents {
		if seen[a.ID] {
			return fmt.Errorf("documentAnalyses[%d].documentId %q is duplicated; keep exactly one analysis for each current batch document", i, a.ID)
		}
		if !offered[a.ID] {
			return fmt.Errorf("documentAnalyses[%d].documentId %q is outside the current analysis batch; only analyze _selectedEvidence.documents=%s and remove off-batch entries", i, a.ID, strings.Join(in.Info.Documents, ","))
		}
		seen[a.ID] = true
		delete(offered, a.ID)
		for _, r := range a.References {
			if refs[r] != a.ID {
				return fmt.Errorf("analysis citation belongs to another document")
			}
		}
	}
	if len(offered) > 0 {
		missing := []string{}
		for _, id := range in.Info.Documents {
			if offered[id] {
				missing = append(missing, id)
			}
		}
		return fmt.Errorf("documentAnalyses is missing current batch documents: %s; record each once even without excerpts, without inventing findings", strings.Join(missing, ","))
	}
	return nil
}

func (s *RuntimeService) completeSelectedEvidenceBatch(ctx context.Context, detail RunDetail, step Step, e AIExecution) error {
	return s.completeSelectedEvidenceBatchWithEvidence(ctx, detail, step, e, nil)
}

func (s *RuntimeService) completeSelectedEvidenceBatchWithEvidence(ctx context.Context, detail RunDetail, step Step, e AIExecution, evidence any) error {
	var in struct {
		Key string `json:"sourceKey"`
	}
	json.Unmarshal(step.Input, &in)
	repo, ok := s.repository.(literatureContinuationRepository)
	if !ok {
		return fmt.Errorf("continuation unavailable")
	}
	payload := map[string]any{"stepId": step.ID, "executionId": e.ID, "sourceKey": in.Key, "analysisCheckpoint": evidenceCheckpointPayload(detail, step, evidence)}
	if evidence != nil {
		payload["indexedEvidence"] = evidence
	}
	event, err := s.event(detail.Run.ID, "workflow.selected_evidence_batch", payload, s.now())
	if err != nil {
		return err
	}
	return repo.QueueLiteratureContinuation(ctx, detail.Run.ID, step.ID, "", step.Attempt, s.now(), event)
}
