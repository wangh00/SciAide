package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

type citationSourceSubmissionError struct{ cause error }

func (e *citationSourceSubmissionError) Error() string {
	return "invalid current citation selection (submission/locator error, not scientific rejection): " + e.cause.Error()
}
func (e *citationSourceSubmissionError) Unwrap() error { return e.cause }

type citationSourceNotVisibleError struct{ reference string }

func (e *citationSourceNotVisibleError) Error() string {
	return "selected source is not fully visible in the actual request: " + e.reference
}

type citationSourceChoice struct {
	reference, quote, pointer string
	parts                     []citationRepairSegment
}
type citationSourceSubmission struct {
	contract         chat.WorkflowAIExecution
	runID, projectID string
	schema           json.RawMessage
	limit            int
	sources          map[string]citationSourceChoice
	aliases          map[string]string
	menu             json.RawMessage
	materials        *stageMaterialLedger
	fieldRepair      *citationFieldRepair
}

func loadCitationSourceSubmission(projectID string, run chat.Run, contract chat.WorkflowAIExecution, found bool, calls []tool.Call) (*citationSourceSubmission, error) {
	if !found || !workflow.IsCitationSourceProtocol(contract.PromptVersion) {
		return nil, nil
	}
	fail := func(err error) (*citationSourceSubmission, error) {
		return nil, &apperr.Error{Code: "WORKFLOW_CITATION_SOURCE_BINDING_INVALID", UserMessage: "文献原文选择与当前冻结执行绑定不一致，已停止，未调用模型；原材料与历史保留。", Cause: err}
	}
	var annotation struct{ Title string }
	_ = json.Unmarshal(contract.OutputSchema, &annotation)
	if contract.NodeID != "evidence_screening" || contract.NodeKind != string(workflow.NodeAgentStage) || contract.ID == "" || contract.WorkflowRunID == "" || contract.WorkflowStepID == "" || contract.Attempt < 1 || !strings.HasSuffix(contract.PromptVersion, "|"+annotation.Title) || (annotation.Title != workflow.CitationSourceProtocolVersion && annotation.Title != workflow.CitationSourceLegacyProtocolVersion) || contract.InputSHA256 != fmt.Sprintf("%x", sha256.Sum256(contract.Input)) || contract.PromptSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(contract.PromptText))) || contract.OutputSchemaSHA256 != fmt.Sprintf("%x", sha256.Sum256(contract.OutputSchema)) {
		return fail(fmt.Errorf("invalid frozen citation selection identity"))
	}
	schema := workflow.CitationSourceSubmissionSchema(contract.OutputSchema)
	if len(schema) == 0 {
		return fail(fmt.Errorf("unsupported citation selection schema"))
	}
	s := &citationSourceSubmission{contract: contract, runID: run.ID, projectID: projectID, schema: schema, limit: citationRepairQuoteLimit(contract.OutputSchema), materials: newStageMaterialLedger(contract, run.ID)}
	if s.materials == nil || run.ID == "" || projectID == "" {
		return fail(fmt.Errorf("missing current execution authority"))
	}
	if err := s.refresh(calls); err != nil {
		return fail(err)
	}
	return s, nil
}

// Resolution uses the SAME current-run signed evidence policy as terminal
// citation persistence. Neither raw candidate JSON nor a model-generated ID
// can mint a source. Complete catalogs fail closed instead of omitting tails.
func (s *citationSourceSubmission) refresh(calls []tool.Call) error {
	markers := []string{}
	for _, call := range calls {
		if call.Result != nil {
			for _, ref := range call.Result.Citations {
				markers = append(markers, ref.Reference)
			}
		}
	}
	refs := citation.Resolve(s.runID, "citation-source-navigation", strings.Join(markers, " "), calls, time.Time{})
	sort.Slice(refs, func(i, j int) bool { return refs[i].Reference < refs[j].Reference })
	s.sources = map[string]citationSourceChoice{}
	s.aliases = map[string]string{}
	var frozen struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	if err := json.Unmarshal(s.contract.Input, &frozen); err != nil {
		return err
	}
	seeded := map[string]bool{}
	for _, call := range calls {
		if call.RunID == s.runID && call.ToolName == citation.WorkflowSeedToolName && citation.IsTrustedKnowledgeCall(call) {
			for _, ref := range call.Result.Citations {
				seeded[ref.Reference] = true
			}
		}
	}
	rows := []map[string]any{}
	total := 0
	for _, ref := range refs {
		if ref.ProjectID != s.projectID {
			continue
		}
		if seeded[ref.Reference] {
			s.aliases[ref.Reference] = citation.KnowledgeReference(s.contract.WorkflowRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
		}
		choice := citationSourceChoice{reference: ref.Reference, quote: ref.Quote, parts: citationRepairSegments(ref.Reference, ref.Quote, s.limit)}
		for i, original := range frozen.Candidates {
			if original.Quote != ref.Quote || original.DocumentID != ref.DocumentID || original.Reference != citation.KnowledgeReference(s.contract.WorkflowRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256) {
				continue
			}
			choice.pointer = fmt.Sprintf("/candidates/%d/quote", i)
			s.aliases[ref.Reference] = original.Reference
			break
		}
		segments := []map[string]any{}
		start := 0
		for i, part := range choice.parts {
			end := start + utf8.RuneCountInString(part.Text)
			safeEnd, safeCount := part.ID, end-start
			for j := i + 1; j < len(choice.parts); j++ {
				n := utf8.RuneCountInString(choice.parts[j].Text)
				if safeCount+n > s.limit {
					break
				}
				safeEnd, safeCount = choice.parts[j].ID, safeCount+n
			}
			segments = append(segments, map[string]any{"id": part.ID, "start": start, "end": end, "lengthSafeEndId": safeEnd, "lengthSafeCharacters": safeCount})
			start = end
		}
		total += len(segments)
		if total > 16384 {
			return fmt.Errorf("citation source navigation exceeds 16384 segments; no sources were silently omitted")
		}
		row := map[string]any{"reference": ref.Reference, "quoteSha256": ref.QuoteSHA256, "quoteCharacters": utf8.RuneCountInString(ref.Quote), "segments": segments}
		if choice.pointer != "" {
			m, err := s.materials.material(choice.pointer)
			if err != nil {
				return err
			}
			row["pointer"], row["jsonOffset"], row["jsonEndOffset"] = choice.pointer, m.OriginalJSONOffset, m.OriginalJSONEndOffset
		}
		rows = append(rows, row)
		s.sources[ref.Reference] = choice
	}
	var annotation struct{ Title string }
	_ = json.Unmarshal(s.contract.OutputSchema, &annotation)
	encoded, err := json.Marshal(map[string]any{"version": annotation.Title, "inputSha256": s.contract.InputSHA256, "quoteCharactersLimit": s.limit, "sources": rows})
	s.menu = encoded
	return err
}

func (s *citationSourceSubmission) instruction() string {
	return stageSubmissionFormatNotice + workflow.CitationSourceInstruction(s.contract.OutputSchema) + "\n<citation_submission_sources>" + string(s.menu) + "</citation_submission_sources>"
}
func (s *citationSourceSubmission) initialGrowth() int {
	if s == nil {
		return 0
	}
	return estimateRequestTokens(model.ChatRequest{Messages: []model.Message{{Role: model.RoleSystem, Content: s.instruction()}}}) + max(0, estimateRequestTokens(model.ChatRequest{Tools: []model.ToolDefinition{stageResultDefinition(s.schema)}})-estimateRequestTokens(model.ChatRequest{Tools: []model.ToolDefinition{stageResultDefinition(s.contract.OutputSchema)}}))
}

func (s *citationSourceSubmission) request(admitted model.ChatRequest, calls []tool.Call, observation *chat.ContextUsageObservation, limit int) (model.ChatRequest, error) {
	if s == nil {
		return admitted, nil
	}
	if err := s.refresh(calls); err != nil {
		return admitted, err
	}
	result, err := fitFinalRequest(admitted, nil, observation, limit, func(r model.ChatRequest) model.ChatRequest {
		r.Tools = append([]model.ToolDefinition(nil), r.Tools...)
		found := false
		for i, def := range r.Tools {
			if def.Name == stageSubmissionTool {
				r.Tools[i].InputSchema = append(json.RawMessage(nil), s.schema...)
				found = true
			}
			if def.Name == stageRepairTool && s.hasFieldRepair() {
				r.Tools[i].Description = "Explicitly replace selected complete top-level fields of the current rejected synthesis using resultUpdates. All unchanged values are retained; the merged whole result still requires original source, schema and scientific validation."
			}
		}
		if !found {
			r = stageExplorationRequest(r, s.schema)
		}
		r.Messages = cloneModelMessages(r.Messages)
		for i, m := range r.Messages {
			if m.Role == model.RoleSystem && m.Content == stageSubmissionFormatNotice+stageActionInstruction {
				r.Messages[i].Content = stageSubmissionFormatNotice + "Current evidence-synthesis action protocol: choose an actually necessary offered research action or call submit_stage_result once with the complete source-selection result. Never mix completion with research IO. No text/fenced-JSON substitute. Full original scientific and scope validation remains mandatory."
			}
		}
		if r.ForcedTool == "" {
			r.RequireTool = true
		}
		r.Messages = append(r.Messages, model.Message{Role: model.RoleSystem, Content: s.instruction()})
		return r
	})
	if err != nil {
		return admitted, &apperr.Error{Code: "WORKFLOW_CITATION_SOURCE_CONTEXT_LIMIT", UserMessage: "当前模型窗口无法同时保留文献证据与原文选择契约，已在调用前停止；未缩短证据、未降为重抄原文。", Details: fmt.Sprintf("source_navigation_bytes=%d input_limit=%d: %v", len(s.menu), limit, err), Cause: err}
	}
	return result, nil
}

func (s *citationSourceSubmission) resolve(text string, request model.ChatRequest, calls []tool.Call) (json.RawMessage, error) {
	s.rememberCandidate(text)
	pointers := map[string]bool{}
	var selected struct {
		Assessments []struct{ Reference string } `json:"citationAssessments"`
	}
	_ = json.Unmarshal([]byte(text), &selected)
	for _, row := range selected.Assessments {
		if source, ok := s.sources[row.Reference]; ok && source.pointer != "" {
			pointers[source.pointer] = true
		}
	}
	// Share the existing exact frozen-value/page visibility verifier; an index,
	// durable retrieval or old exposure alone never authorizes source copying.
	view := &reviewSourceSubmission{catalog: &workflow.ReviewSourceCatalog{CitationAliases: s.aliases}, materials: s.materials}
	visible := view.visibleSources(request, calls, pointers)
	var literalVisible map[string][][2]int
	copied, err := workflow.ResolveCitationSourceSubmission(text, s.contract.OutputSchema, func(reference string, selection workflow.CitationSourceSelection) (string, error) {
		source, ok := s.sources[reference]
		if !ok {
			return "", fmt.Errorf("unissued current citation: %s", reference)
		}
		positions := map[string]int{}
		starts, ends := make([]int, len(source.parts)), make([]int, len(source.parts))
		end := 0
		for i, p := range source.parts {
			positions[p.ID] = i
			starts[i] = end
			end += utf8.RuneCountInString(p.Text)
			ends[i] = end
		}
		first, last := -1, -1
		for i, id := range selection.SourceSegmentIDs {
			p, ok := positions[id]
			if !ok || i > 0 && p != last+1 {
				return "", &citationRepairSelectionError{cause: fmt.Errorf("%s select consecutive IDs from this exact source, without gaps/repeats/reordering", reference)}
			}
			if i == 0 {
				first = p
			}
			last = p
		}
		a, b := starts[first], ends[last]
		if (selection.StartOffset == nil) != (selection.EndOffset == nil) {
			return "", fmt.Errorf("%s requires both startOffset and endOffset", reference)
		}
		if selection.StartOffset != nil {
			a, b = *selection.StartOffset, *selection.EndOffset
			if a < starts[first] || b > ends[last] || a >= b {
				return "", fmt.Errorf("%s selection offsets are outside the chosen continuous source range", reference)
			}
		}
		if b-a > s.limit {
			return "", &citationRepairLengthError{Reference: reference, Actual: b - a, Limit: s.limit}
		}
		quote := string([]rune(source.quote)[a:b])
		if strings.TrimSpace(quote) == "" {
			return "", fmt.Errorf("%s selected an empty/whitespace source range", reference)
		}
		seen := false
		for _, span := range visible[source.pointer] {
			if span[0] <= a && span[1] >= b {
				seen = true
				break
			}
		}
		// Chat startup rewrites seed markers globally, including marker-shaped
		// source data. Reversing that alias is not proof the original bytes
		// were visible. Require actual unmodified tool text for intersecting
		// ranges; do not silently copy an unseen old marker from frozen data.
		if s.sourceRangeRewritten(source.quote, a, b) {
			seen = false
			if literalVisible == nil {
				literal := &reviewSourceSubmission{catalog: &workflow.ReviewSourceCatalog{}, materials: s.materials}
				literalVisible = literal.visibleSources(request, calls, pointers)
			}
			for _, span := range literalVisible[source.pointer] {
				if span[0] <= a && span[1] >= b {
					seen = true
					break
				}
			}
		}
		if !seen && !s.visibleToolSource(source, request, calls) {
			return "", &citationSourceNotVisibleError{reference: reference}
		}
		return quote, nil
	})
	if err != nil {
		// A non-quotation field can fail either the wire or canonical schema.
		// Keep that ordinary format classification; do not blame source IDs.
		if workflow.IsAIStageOutputFormatError(err) {
			return nil, err
		}
		return nil, &citationSourceSubmissionError{cause: err}
	}
	return copied, nil
}

func (s *citationSourceSubmission) visibleToolSource(source citationSourceChoice, request model.ChatRequest, calls []tool.Call) bool {
	request.Messages = cloneModelMessages(request.Messages)
	for i, m := range request.Messages {
		if m.Role == model.RoleUser && m.EvidenceToolCallID != "" && m.EvidencePrefix != "" && strings.HasPrefix(m.Content, m.EvidencePrefix) {
			m.Role, m.ToolCallID, m.Content = model.RoleTool, m.EvidenceToolCallID, strings.TrimPrefix(m.Content, m.EvidencePrefix)
			request.Messages[i] = m
		}
	}
	byID := convergenceUniqueCalls(request, calls)
	for _, m := range convergenceToolViews(request) {
		call, ok := byID[m.ToolCallID]
		if !ok || call.RunID != s.runID || !citation.IsTrustedKnowledgeCall(call) {
			continue
		}
		owned := false
		for _, ref := range call.Result.Citations {
			if ref.Reference == source.reference && ref.Quote == source.quote && ref.ProjectID == s.projectID {
				owned = true
				break
			}
		}
		if !owned || !strings.Contains(m.Content, source.reference) {
			continue
		}
		var envelope struct {
			Status     tool.ResultStatus
			Text       string
			Structured json.RawMessage
		}
		if json.Unmarshal([]byte(m.Content), &envelope) != nil || envelope.Status != tool.ResultSuccess {
			continue
		}
		if strings.Contains(envelope.Text, source.quote) {
			return true
		}
		var data any
		if json.Unmarshal(envelope.Structured, &data) == nil && citationSourceTextVisible(data, source.quote, 0) {
			return true
		}
	}
	return false
}
func citationSourceTextVisible(value any, quote string, depth int) bool {
	if depth > 64 {
		return false
	}
	switch v := value.(type) {
	case string:
		return strings.Contains(v, quote)
	case []any:
		for _, x := range v {
			if citationSourceTextVisible(x, quote, depth+1) {
				return true
			}
		}
	case map[string]any:
		for _, x := range v {
			if citationSourceTextVisible(x, quote, depth+1) {
				return true
			}
		}
	}
	return false
}

func (s *citationSourceSubmission) sourceRangeRewritten(source string, a, b int) bool {
	for current, old := range s.aliases {
		if current == old {
			continue
		}
		for from := 0; from < len(source); {
			at := strings.Index(source[from:], old)
			if at < 0 {
				break
			}
			at += from
			start := utf8.RuneCountInString(source[:at])
			end := start + utf8.RuneCountInString(old)
			if a < end && b > start {
				return true
			}
			from = at + len(old)
		}
	}
	return false
}

const citationSourceActionContinuation = "The previous response did not submit the required evidence synthesis. Continue the SAME stage with the current native action protocol. If a concrete source gap remains, choose its already-offered research/read action; otherwise call submit_stage_result once with the COMPLETE result and current quoteSelection IDs. Never repeat completed work, return a text/fenced answer, or promise submission without the actual action. Original schema, scientific checks and correction limits remain unchanged."
