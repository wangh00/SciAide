package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

type reviewSourceSubmission struct {
	catalog    *workflow.ReviewSourceCatalog
	fullSchema json.RawMessage
	materials  *stageMaterialLedger
}
type reviewSourceSubmissionError struct{ cause error }

func (e *reviewSourceSubmissionError) Error() string {
	return "invalid current-source selection (submission/locator error, not scientific rejection): " + e.cause.Error()
}
func (e *reviewSourceSubmissionError) Unwrap() error { return e.cause }

func (s *reviewSourceSubmission) initialGrowth() int {
	if s == nil {
		return 0
	}
	r := model.ChatRequest{Messages: []model.Message{{Role: model.RoleSystem, Content: stageSubmissionFormatNotice + workflow.ReviewSourceInstruction(s.catalog.Binding.PromptVersion) + "\n<review_submission_sources>" + string(s.catalog.SubmissionMenu()) + "</review_submission_sources>"}}}
	return estimateRequestTokens(r)
}

func (l *Loop) loadReviewSourceSubmission(ctx context.Context, projectID string, run chat.Run, contract chat.WorkflowAIExecution, found bool) (*reviewSourceSubmission, error) {
	if !found || !workflow.IsReviewSourceProtocol(contract.PromptVersion) {
		return nil, nil
	}
	fail := func(err error) (*reviewSourceSubmission, error) {
		return nil, &apperr.Error{Code: "WORKFLOW_REVIEW_SOURCE_BINDING_INVALID", UserMessage: "审查原文选择与当前冻结执行绑定不一致，已停止，未调用模型；原材料与历史保留。", Cause: err}
	}
	if contract.NodeID != "independent_review" || contract.NodeKind != string(workflow.NodeAgentStage) && contract.NodeKind != string(workflow.NodeAIAnalysis) || !workflow.SupportsIssueReview(contract.OutputSchema) || contract.ID == "" || contract.InputSHA256 != fmt.Sprintf("%x", sha256.Sum256(contract.Input)) || contract.PromptSHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(contract.PromptText))) {
		return fail(fmt.Errorf("invalid frozen review protocol identity"))
	}
	reader, ok := l.research.(interface {
		StageReviewSources(context.Context, workflow.StageMaterialRequest) (*workflow.ReviewSourceCatalog, error)
	})
	if !ok {
		return fail(fmt.Errorf("host review source reader unavailable"))
	}
	binding := workflow.StageMaterialRequest{ProjectID: projectID, ConversationID: run.ConversationID, WorkflowRunID: contract.WorkflowRunID, WorkflowStepID: contract.WorkflowStepID, ChatRunID: run.ID, Attempt: contract.Attempt, InputSHA256: contract.InputSHA256}
	c, err := reader.StageReviewSources(ctx, binding)
	if err != nil {
		return fail(err)
	}
	if c == nil || c.Version != workflow.ReviewSourceVersion(contract.PromptVersion) || c.Binding.StageMaterialRequest != binding || c.Binding.ExecutionID != contract.ID || c.Binding.PromptVersion != contract.PromptVersion || c.Binding.OutputSchemaSHA256 != contract.OutputSchemaSHA256 {
		return fail(fmt.Errorf("review source receipt mismatch"))
	}
	full := workflow.ReviewSourceSubmissionSchema(workflow.ReviewSubmissionSchema(contract.OutputSchema))
	if len(full) == 0 {
		return fail(fmt.Errorf("unsupported review source schema"))
	}
	return &reviewSourceSubmission{catalog: c, fullSchema: full, materials: newStageMaterialLedger(contract, run.ID)}, nil
}

func (s *reviewSourceSubmission) request(admitted model.ChatRequest, observation *chat.ContextUsageObservation, limit int) (model.ChatRequest, error) {
	if s == nil {
		return admitted, nil
	}
	r, err := fitFinalRequest(admitted, nil, observation, limit, func(r model.ChatRequest) model.ChatRequest {
		r.Tools = append([]model.ToolDefinition(nil), r.Tools...)
		full := false
		for i, def := range r.Tools {
			if def.Name == stageSubmissionTool {
				r.Tools[i].InputSchema = append(json.RawMessage(nil), s.fullSchema...)
				full = true
			}
			if def.Name == stageRepairTool {
				r.Tools[i].InputSchema = workflow.ReviewSourceSubmissionSchema(def.InputSchema)
			}
		}
		if !full {
			r = stageExplorationRequest(r, s.fullSchema)
		}
		r.Messages = cloneModelMessages(r.Messages)
		for i, m := range r.Messages {
			if m.Role == model.RoleSystem && m.Content == stageSubmissionFormatNotice+stageActionInstruction {
				r.Messages[i].Content = stageSubmissionFormatNotice + "Current independent-review action protocol: choose an actually necessary offered research action within its frozen permissions, or call submit_stage_result exactly once with your complete source-selection review. Do not mix completion with research IO. No text or fenced-JSON substitute is accepted in this version. Full original scientific and Skill validation remains mandatory."
			}
		}
		if r.ForcedTool == "" {
			r.RequireTool = true
		}
		menu := s.catalog.SubmissionMenu()
		formatReminder := "\nStrict result-shape reminder: submit_stage_result uses the frozen schema with ALL required top-level keys, even when arrays are empty: approved, reviewedInputSha256, verifiedClaims, unsupportedClaims, citationIssues, numericIssues, methodIssues, requiredCorrections, confidence, limitations, acceptanceChecks, reviewFindings, suggestions, revisionPlan. Because additionalProperties is false, reviewFindings items may contain only their schema keys (id,status,category,summary,location,basis,reason,origin,changeReason,correctionIndex); do NOT add correction or revision objects there. Put correction text/targets only in requiredCorrections and revisionPlan. Do not omit required empty arrays."
		r.Messages = append(cloneModelMessages(r.Messages), model.Message{Role: model.RoleSystem, Content: stageSubmissionFormatNotice + formatReminder + workflow.ReviewSourceInstruction(s.catalog.Binding.PromptVersion) + "\n<review_submission_sources>" + string(menu) + "</review_submission_sources>"})
		return r
	})
	if err != nil {
		return admitted, &apperr.Error{Code: "WORKFLOW_REVIEW_SOURCE_CONTEXT_LIMIT", UserMessage: "当前模型窗口无法同时保留审查证据与原文选择契约，已在调用模型前停止；未缩短材料、未降为重抄原文。", Details: fmt.Sprintf("source_navigation_bytes=%d source_segments=%d admitted_estimate=%d input_limit=%d: %v", len(s.catalog.SubmissionMenu()), len(s.catalog.Segments), estimateRequestTokens(admitted), limit, err), Cause: err}
	}
	return r, nil
}

func (s *reviewSourceSubmission) resolve(text, control string, request model.ChatRequest, calls []tool.Call) (json.RawMessage, error) {
	for _, def := range request.Tools {
		if def.Name == control {
			value, err := s.catalog.ResolveVisibleSubmission(text, def.InputSchema, s.visibleSources(request, calls, s.catalog.SelectedPointers(text)))
			if err != nil {
				if workflow.IsAIStageOutputFormatError(err) {
					return nil, err // Complete wire/field error, not source authority or science.
				}
				return nil, &reviewSourceSubmissionError{cause: err}
			}
			return value, nil
		}
	}
	return nil, &reviewSourceSubmissionError{cause: fmt.Errorf("completion control was not offered")}
}

// Inspect the FINAL request, never stored-but-hidden pages or prior exposure.
// Only exact current values / hash-bound visible pages prove literal coverage;
// this is provenance protection, not a scientific reading/completion judgment.
func (s *reviewSourceSubmission) visibleSources(request model.ChatRequest, calls []tool.Call, pointers map[string]bool) map[string][][2]int {
	spans := map[string][]readSpan{}
	result := map[string][][2]int{}
	g := s.materials
	if g == nil || len(pointers) == 0 {
		return result
	}
	keys := []string{}
	for key := range s.catalog.CitationAliases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := []string{}
	for _, key := range keys {
		pairs = append(pairs, key, s.catalog.CitationAliases[key])
	}
	restore := strings.NewReplacer(pairs...)
	canonicalMessage := func(m model.Message) model.Message {
		m.Content = restore.Replace(m.Content)
		return m
	}
	request.Messages = cloneModelMessages(request.Messages)
	for i, m := range request.Messages {
		m = canonicalMessage(m)
		if m.Role == model.RoleUser && m.EvidenceToolCallID != "" && m.EvidencePrefix != "" && strings.HasPrefix(m.Content, restore.Replace(m.EvidencePrefix)) {
			m.Role, m.ToolCallID, m.Content = model.RoleTool, m.EvidenceToolCallID, strings.TrimPrefix(m.Content, restore.Replace(m.EvidencePrefix))
		}
		request.Messages[i] = m
	}
	request.ProviderTurns = append([]model.ProviderTurn(nil), request.ProviderTurns...)
	for i, turn := range request.ProviderTurns {
		request.ProviderTurns[i].ToolResults = append([]model.Message(nil), turn.ToolResults...)
		for j, m := range turn.ToolResults {
			request.ProviderTurns[i].ToolResults[j] = canonicalMessage(m)
		}
	}
	calls = append([]tool.Call(nil), calls...)
	for i := range calls {
		calls[i].Arguments = json.RawMessage(restore.Replace(string(calls[i].Arguments)))
	}
	global := []readSpan{}
	for _, m := range request.Messages {
		if m.Role != model.RoleUser {
			continue
		}
		if m.HostToolReferences {
			start, end := strings.LastIndex(m.Content, "<stage_input>\n"), strings.LastIndex(m.Content, "\n</stage_input>")
			if start >= 0 && end > start {
				view := json.RawMessage(m.Content[start+len("<stage_input>\n") : end])
				paths := []string{}
				if strings.Contains(m.Content, `<stage_input_encoding version="source-sharing-v1">`) {
					a, b := strings.LastIndex(m.Content, "<stage_input_shared_paths>\n"), strings.LastIndex(m.Content, "\n</stage_input_shared_paths>")
					if a >= 0 && b > a {
						_ = json.Unmarshal([]byte(m.Content[a+len("<stage_input_shared_paths>\n"):b]), &paths)
					}
				}
				for pointer := range pointers {
					original, err := g.material(pointer)
					visible, viewErr := workflow.ResolveReviewVisibleMaterial(g.contract.Input, view, paths, pointer)
					if err == nil && viewErr == nil && visible.Encoding == original.Encoding && visible.ValueSHA256 == original.ValueSHA256 {
						spans[pointer] = append(spans[pointer], readSpan{0, utf8.RuneCountInString(original.Content)})
					}
				}
			}
		}
		// Retained convergence text is visible in this request and must match
		// the frozen bytes/ranges. The heading alone grants no authority.
		if pos := strings.Index(m.Content, stageMaterialEvidencePrefix); pos >= 0 {
			var fragments []struct {
				InputSHA256, Pointer, Encoding, ValueSHA256, Content string
				Offset, EndOffset                                    int
			}
			if json.NewDecoder(strings.NewReader(m.Content[pos+len(stageMaterialEvidencePrefix):])).Decode(&fragments) == nil {
				for _, f := range fragments {
					v, err := g.material(f.Pointer)
					runes := []rune(v.Content)
					if err == nil && f.InputSHA256 == g.contract.InputSHA256 && f.Encoding == v.Encoding && f.ValueSHA256 == v.ValueSHA256 && f.Offset >= 0 && f.EndOffset > f.Offset && f.EndOffset <= len(runes) && f.Content == string(runes[f.Offset:f.EndOffset]) {
						if f.Encoding == "json" {
							if r, ok := workflow.ReviewSourceJSONPageRange(g.contract.Input, v, f.Offset, f.EndOffset); ok {
								global = append(global, readSpan{r[0], r[1]})
							}
						} else {
							spans[f.Pointer] = append(spans[f.Pointer], readSpan{f.Offset, f.EndOffset})
						}
					}
				}
			}
		}
		if pos := strings.Index(m.Content, convergenceEvidencePrefix); pos >= 0 {
			var fragments []struct {
				Section, SHA256, Content string
				Offset, EndOffset        int
			}
			if json.NewDecoder(strings.NewReader(m.Content[pos+len(convergenceEvidencePrefix):])).Decode(&fragments) == nil {
				runes := []rune(string(g.contract.Input))
				for _, f := range fragments {
					if f.Section == "stage_input" && f.SHA256 == g.contract.InputSHA256 && f.Offset >= 0 && f.EndOffset > f.Offset && f.EndOffset <= len(runes) && f.Content == string(runes[f.Offset:f.EndOffset]) {
						global = append(global, readSpan{f.Offset, f.EndOffset})
					}
				}
			}
		}
	}
	for _, page := range g.visiblePages(request, calls) {
		if page.Encoding == "text" {
			spans[page.Pointer] = append(spans[page.Pointer], readSpan{page.Offset, page.NextOffset})
		} else if value, err := g.material(page.Pointer); err == nil && value.Encoding == "json" {
			if r, ok := workflow.ReviewSourceJSONPageRange(g.contract.Input, value, page.Offset, page.NextOffset); ok {
				global = append(global, readSpan{r[0], r[1]})
			}
		}
	}
	if raw := newStageReadConvergence(g.contract, g.runID); raw != nil {
		for _, span := range raw.visiblePages(request, calls) {
			global = append(global, span)
		}
	}
	globalRanges := [][2]int{}
	for _, r := range mergeReadSpans(global) {
		globalRanges = append(globalRanges, [2]int{r.start, r.end})
	}
	for pointer := range pointers {
		v, err := g.material(pointer)
		if err == nil {
			for _, r := range workflow.ReviewSourceRangesFromJSON(g.contract.Input, v, globalRanges) {
				spans[pointer] = append(spans[pointer], readSpan{r[0], r[1]})
			}
		}
	}
	for pointer, ranges := range spans {
		for _, r := range mergeReadSpans(ranges) {
			result[pointer] = append(result[pointer], [2]int{r.start, r.end})
		}
	}
	return result
}
