package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/modelutil"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type selectedBatchConnector struct{ researchClosureConnector }

func (selectedBatchConnector) Search(ctx context.Context, _ research.SearchOptions) ([]research.Work, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	works := []research.Work{}
	for i := 0; i < 18; i++ {
		work := researchClosureWork()
		work.SourceRecordID = fmt.Sprintf("10.5555/sciaide-batch-%d", i)
		work.Identifiers.DOI = work.SourceRecordID
		work.LandingURL = "https://doi.org/" + work.SourceRecordID
		work.RawSnapshot = json.RawMessage(fmt.Sprintf(`{"DOI":%q,"fixture":true}`, work.SourceRecordID))
		work.Title = fmt.Sprintf("Bounded evidence comparison fixture %d", i)
		work.Abstract = strings.Repeat("Epsilon fixture evidence concerns a bounded comparison. This is synthetic research, not an actual trial. ", 55)
		works = append(works, work)
	}
	return works, nil
}

func (selectedBatchConnector) Fetch(context.Context, string) (research.Work, error) {
	return research.Work{}, fmt.Errorf("fixture full text unavailable")
}

// Actual HTTP provider, resource-read tool, temporary SQLite and restart. The
// model deliberately follows documentIds instead of the internal batch list,
// reproducing the real failure when those inventories disagree. A new read in
// the accepted first batch rotates the window; a later batch is cancelled and
// resumed, without repeating accepted batches or the search/import stages.
func TestSelectedEvidenceHTTPBatchesCancelRestartAndCitationSelection(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "")
}

func TestSelectedEvidenceHTTPMixedDefectsCorrectTogether(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "correct")
}

func TestSelectedEvidenceHTTPMixedDefectsStopAndRetryKeepsBatches(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "retry")
}

func TestSelectedEvidenceHTTPSourceSelectionAfterNoOpRepair(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "segments")
}

func TestSelectedEvidenceHTTPRepeatedNoOpStopsAndRetryKeepsBatches(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "segments_retry")
}

func TestSelectedEvidenceHTTPReviewGenerationCancelRestartConverges(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "review_generation")
}

func TestSelectedEvidenceHTTPReviewReplacesOnlyOneDocument(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "document_review_generation")
}

func TestSelectedEvidenceHTTPLargeWorkProductsPageAcrossRestartAndReview(t *testing.T) {
	testSelectedEvidenceHTTPBatches(t, "paged_review_generation")
}

// The fixture can inspect only the provider-visible prompt and tool replies.
// It remembers complete read pages like a model retaining earlier observations;
// it never reads stage inputs from SQLite or enlarges the model window.
type selectedBatchPages struct {
	content  map[string]map[int]string
	complete map[string]any
}

func (p *selectedBatchPages) observe(body []byte) {
	var request struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	if json.Unmarshal(body, &request) != nil {
		return
	}
	for _, message := range request.Messages {
		if message.Role != "tool" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) != nil {
			continue
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
		var reply struct {
			Structured struct {
				Section, Content, SHA256 string
				Offset, NextOffset       int
			}
		}
		if json.Unmarshal([]byte(text), &reply) != nil {
			continue
		}
		page := reply.Structured
		if page.Section != "stage_input" || page.SHA256 == "" || page.NextOffset <= page.Offset || utf8.RuneCountInString(page.Content) != page.NextOffset-page.Offset {
			continue
		}
		if p.content == nil {
			p.content = map[string]map[int]string{}
		}
		if p.content[page.SHA256] == nil {
			p.content[page.SHA256] = map[int]string{}
		}
		if utf8.RuneCountInString(page.Content) > utf8.RuneCountInString(p.content[page.SHA256][page.Offset]) {
			p.content[page.SHA256][page.Offset] = page.Content
		}
	}
}

// Resolve a host-issued $read value from the original stage_input character
// range. A nonnegative pending offset requests exactly the first unseen page.
func (p *selectedBatchPages) resolve(value any) (any, int, error) {
	object, _ := value.(map[string]any)
	read, _ := object["$read"].(map[string]any)
	if read == nil {
		return value, -1, nil
	}
	start, startOK := read["offset"].(float64)
	end, endOK := read["endOffset"].(float64)
	sha, shaOK := read["sha256"].(string)
	pointer, pointerOK := read["pointer"].(string)
	if !startOK || !endOK || !shaOK || start < 0 || end < start || sha == "" {
		return nil, -1, fmt.Errorf("invalid host read navigation")
	}
	cacheKey := fmt.Sprintf("%s:%s:%d:%d", sha, pointer, int(start), int(end))
	if !pointerOK || pointer == "" {
		return nil, -1, fmt.Errorf("missing host read pointer")
	}
	if cached, ok := p.complete[cacheKey]; ok {
		return cached, -1, nil
	}
	offset := int(start)
	var full strings.Builder
	for offset < int(end) {
		page, ok := p.content[sha][offset]
		if !ok {
			for from, candidate := range p.content[sha] {
				if from <= offset && from+utf8.RuneCountInString(candidate) > offset && utf8.RuneCountInString(candidate)-offset+from > utf8.RuneCountInString(page) {
					page = string([]rune(candidate)[offset-from:])
					ok = true
				}
			}
		}
		if !ok {
			return nil, offset, nil
		}
		remaining := int(end) - offset
		runes := []rune(page)
		if len(runes) == 0 {
			return nil, -1, fmt.Errorf("read page made no progress")
		}
		full.WriteString(string(runes[:min(len(runes), remaining)]))
		offset += len(runes)
	}
	var decoded any
	if err := json.Unmarshal([]byte(full.String()), &decoded); err != nil {
		return nil, -1, fmt.Errorf("read page value is incomplete: %w", err)
	}
	if p.complete == nil {
		p.complete = map[string]any{}
	}
	p.complete[cacheKey] = decoded
	return decoded, -1, nil
}

func testSelectedEvidenceHTTPBatches(t *testing.T, repairMode string) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]bool{}
	batchReplies, readRequests, coverageReplies := 0, 0, 0
	reviewGeneration := strings.HasSuffix(repairMode, "review_generation")
	pagedWork := repairMode == "paged_review_generation"
	localCorrection := repairMode == "document_review_generation"
	correctedDocument := ""
	workPages := 0
	pages := &selectedBatchPages{}
	repairBatches, reviews := 0, 0
	keys := map[bool]string{}
	allowRetryCorrection := false
	sourceSelection := strings.HasPrefix(repairMode, "segments")
	blockedOnce := false
	blocked, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		prompt := workflowAITestPrompt(body)
		schema, err := workflowAITestSchema(prompt)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid stage prompt", 400)
			return
		}
		input := workflowAITestStageInput(prompt)
		if pagedWork && strings.Contains(prompt, "Host submission validation failed:") {
			at := strings.LastIndex(prompt, "Host submission validation failed:")
			t.Log("paged stage correction", strings.ReplaceAll(prompt[at:min(len(prompt), at+600)], "\n", " "))
		}
		if pagedWork {
			pages.observe(body)
			var request struct {
				Tools []struct{ Function struct{ Name string } }
			}
			_ = json.Unmarshal(body, &request)
			readTool := ""
			for _, offered := range request.Tools {
				if offered.Function.Name == modelutil.ProviderToolName(workflow.ResearchTaskReadTool) {
					readTool = offered.Function.Name
					break
				}
			}
			// Resolve only the fields this model fixture actually needs. The
			// original frozen JSON is reached solely by its existing task-read
			// tool, never by directly querying the test database.
			for _, path := range [][]string{{"candidates"}, {"documentIds"}, {"_selectedEvidence"}, {"researchRecordAudit"}, {"researchRecordAudit", "queries"}, {"evidenceWork"}} {
				parent := input
				for _, key := range path[:len(path)-1] {
					parent, _ = parent[key].(map[string]any)
				}
				key, field := path[len(path)-1], strings.Join(path, ".")
				value, exists := parent[key]
				if !exists {
					continue
				}
				resolved, pending, resolveErr := pages.resolve(value)
				if resolveErr != nil {
					t.Error("stage read", field, resolveErr)
					http.Error(w, "invalid saved stage page", 400)
					return
				}
				if pending >= 0 {
					if readTool == "" {
						names := []string{}
						for _, offered := range request.Tools {
							names = append(names, offered.Function.Name)
						}
						t.Error("existing task reader not exposed for", field, "stage", len(input), "prompt", len(prompt), "tools", names, "phase", strings.Contains(prompt, "Host stage phase: SUBMIT"), "correction", strings.Contains(prompt, "Host submission validation failed:"), "coverageReplies", coverageReplies, "batchReplies", batchReplies)
						http.Error(w, "missing reader", 400)
						return
					}
					mu.Lock()
					workPages++
					mu.Unlock()
					reportEfficiencyToolReply(w, readTool, string(mustBootstrapJSON(t, map[string]any{"section": "stage_input", "offset": pending})), fmt.Sprintf("stage-page-%s-%d-%d", field, pending, len(request.Tools)))
					return
				}
				parent[key] = resolved
			}
		}
		if pagedWork {
			var rows []any
			for _, pair := range [][2]string{{"_selectedEvidence", "analyses"}, {"evidenceWork", "documentAnalyses"}} {
				container, _ := input[pair[0]].(map[string]any)
				if values, ok := container[pair[1]].([]any); ok {
					rows = values
				}
			}
			if len(rows) > 0 {
				first := rows[0].(map[string]any)
				work, _ := first["workProduct"].(map[string]any)
				read, _ := work["$read"].(map[string]any)
				if read == nil {
					t.Error("large work not projected")
					http.Error(w, "unpaged work", 400)
					return
				}
				start, end := int(read["offset"].(float64)), int(read["endOffset"].(float64))
				offset := start
				var req struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
					Tools []struct{ Function struct{ Name string } }
				}
				_ = json.Unmarshal(body, &req)
				var recovered strings.Builder
				for _, message := range req.Messages {
					if message.Role != "tool" {
						continue
					}
					var text string
					if json.Unmarshal(message.Content, &text) != nil {
						continue
					}
					text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
					var envelope struct {
						Structured struct {
							Section, Content, SHA256 string
							Offset, NextOffset       int
						}
					}
					if json.Unmarshal([]byte(text), &envelope) != nil {
						continue
					}
					p := envelope.Structured
					if p.Section != "stage_input" || p.Offset != offset {
						continue
					}
					if p.SHA256 != read["sha256"] {
						t.Error("page identity differs from navigation")
					}
					chars := []rune(p.Content)
					recovered.WriteString(string(chars[:min(len(chars), end-offset)]))
					offset = p.NextOffset
				}
				if offset < end {
					for _, candidate := range req.Tools {
						if candidate.Function.Name == "research_task_read" {
							mu.Lock()
							workPages++
							mu.Unlock()
							reportEfficiencyToolReply(w, candidate.Function.Name, string(mustBootstrapJSON(t, map[string]any{"section": "stage_input", "offset": offset})), fmt.Sprintf("work-page-%d-%d", offset, len(req.Messages)))
							return
						}
					}
					t.Error("existing task reader not exposed")
					http.Error(w, "missing reader", 400)
					return
				}
				var value string
				if json.Unmarshal([]byte(recovered.String()), &value) != nil || !strings.HasPrefix(value, "Actual bounded appraisal") || !strings.HasSuffix(value, "END OF SAVED WORK") {
					t.Error("HTTP model could not reconstruct complete saved work")
				}
			}
		}
		if reviewGeneration && input["_selectedEvidence"] == nil {
			audit, _ := input["researchRecordAudit"].(map[string]any)
			queries, _ := audit["queries"].([]any)
			if audit["status"] != "available" || len(queries) != 1 {
				t.Error("saved query record audit lost", audit)
			} else {
				q := queries[0].(map[string]any)
				if q["sourceRecordCount"] != float64(18) || q["candidateCount"] != float64(18) {
					t.Error("query record totals changed", q)
				}
			}
			work, _ := input["evidenceWork"].(map[string]any)
			analyses, _ := work["documentAnalyses"].([]any)
			if len(analyses) != 18 {
				t.Error("downstream lost persisted per-document work", len(analyses))
			}
			for _, value := range analyses {
				if !pagedWork && !strings.Contains(fmt.Sprint(value.(map[string]any)["workProduct"]), "Actual bounded appraisal") {
					t.Error("workProduct lost across provider/persistence")
				}
			}
			if localCorrection {
				mu.Lock()
				corrected := correctedDocument
				mu.Unlock()
				updated := 0
				for _, value := range analyses {
					row := value.(map[string]any)
					if row["documentId"] == corrected {
						updated++
						if row["finding"] != "Corrected single-document comparison" {
							t.Error("downstream lost accepted local correction")
						}
					} else if row["finding"] != "Epsilon fixture evidence concerns a bounded comparison" {
						t.Error("local repair rewrote an unrelated document")
					}
				}
				if corrected != "" && updated != 1 {
					t.Error("correction was not merged exactly once", updated)
				}
			}
			out := workflowAISchemaFixture(schema).(map[string]any)
			if _, review := schema["properties"].(map[string]any)["reviewedInputSha256"]; review {
				mu.Lock()
				reviews++
				first := reviews == 1
				mu.Unlock()
				out["approved"], out["reviewedInputSha256"] = !first, workflowAITestStageInputSHA256(prompt)
				out["unsupportedClaims"], out["citationIssues"], out["numericIssues"], out["methodIssues"], out["requiredCorrections"], out["revisionPlan"] = []any{}, []any{}, []any{}, []any{}, []any{}, []any{}
				if first {
					out["methodIssues"], out["requiredCorrections"] = []string{"Recheck the synthetic source comparison"}, []string{"Recheck the synthetic source comparison"}
					out["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "evidence_screening", "reason": "The reusable evidence comparison needs source rechecking", "whyNotLater": "Editing report wording cannot replace the upstream comparison", "needsUserInput": false, "requiredInput": ""}}
				}
			} else {
				out["markdown"], out["claimSummary"], out["limitations"], out["confidence"] = "# Synthetic fixture\nA bounded comparison, not a scientific result.", []string{}, []string{"Synthetic fixture"}, "low"
			}
			emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIChat, 1, "```json\n"+string(mustBootstrapJSON(t, out))+"\n```", "", "")
			return
		}
		info := input["_selectedEvidence"].(map[string]any)
		phase := info["phase"].(string)
		repairing := reviewGeneration && input["_reviewRevision"] != nil
		mu.Lock()
		if phase == "batch" || repairing {
			key, _ := input["sourceKey"].(string)
			if old := keys[repairing]; old != "" && old != key {
				t.Error("batch source identity changed within the confirmed generation")
			}
			keys[repairing] = key
		}
		shouldBlock := batchReplies > 0
		if reviewGeneration {
			// A synthesis-only repair reuses the accepted per-document work.
			// Cancel its pending coverage request, then reopen from SQLite.
			shouldBlock = repairing && phase == "coverage"
		}
		if (phase == "batch" || reviewGeneration && phase == "coverage") && shouldBlock && !blockedOnce {
			blockedOnce = true
			close(blocked)
			mu.Unlock()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		mu.Unlock()
		if phase == "batch" {
			var req struct {
				Tools []struct {
					Function struct{ Name, Description string }
				}
			}
			_ = json.Unmarshal(body, &req)
			mu.Lock()
			needRead := batchReplies == 0 && readRequests == 0
			mu.Unlock()
			if needRead {
				name, action := "", ""
				for _, t := range req.Tools {
					if strings.Contains(t.Function.Description, "Operate on a host-issued resource action") {
						name = t.Function.Name
					}
				}
				start := strings.LastIndex(prompt, "<resource_actions>\n")
				if start >= 0 {
					value := prompt[start+len("<resource_actions>\n"):]
					end := strings.Index(value, "\n</resource_actions>")
					var menu struct{ Actions []resource.Option }
					if end >= 0 && json.Unmarshal([]byte(value[:end]), &menu) == nil {
						for _, option := range menu.Actions {
							if option.Kind == "document_evidence" {
								action = option.ID
								break
							}
						}
					}
				}
				if name == "" || action == "" {
					t.Error("missing issued indexed-read action")
					http.Error(w, "missing read action", 400)
					return
				}
				mu.Lock()
				readRequests++
				mu.Unlock()
				args, _ := json.Marshal(map[string]string{"actionId": action})
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "selected-batch-read", "function": map[string]any{"name": name, "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
				return
			}
		}
		var candidates []tool.CitationRef
		_ = json.Unmarshal(mustBootstrapJSON(t, input["candidates"]), &candidates)
		out := workflowAISchemaFixture(schema).(map[string]any)
		if phase == "batch" {
			if string(mustBootstrapJSON(t, input["documentIds"])) != string(mustBootstrapJSON(t, info["documents"])) {
				t.Error("provider received conflicting inventories")
			}
			notes := []map[string]any{}
			for _, rawID := range input["documentIds"].([]any) {
				id := rawID.(string)
				seenID := id
				if repairing {
					seenID = "repair:" + id
				}
				mu.Lock()
				if seen[seenID] {
					t.Error("accepted document repeated in another provider batch", id)
				}
				seen[seenID] = true
				mu.Unlock()
				refs := []string{}
				for _, ref := range candidates {
					if ref.DocumentID == id && strings.Contains(ref.Quote, "Epsilon fixture evidence") {
						refs = append(refs, ref.Reference)
						break
					}
				}
				work := "Actual bounded appraisal for " + id + ": allocation concealment cannot be determined from the supplied excerpt; no low-risk rating inferred."
				if pagedWork {
					work = "Actual bounded appraisal " + strings.Repeat("未知领域😀；原文未提供分配过程\n", 200) + "END OF SAVED WORK"
				}
				notes = append(notes, map[string]any{"documentId": id, "finding": "Epsilon fixture evidence concerns a bounded comparison", "limitations": "Synthetic fixture, not scientific validation", "references": refs, "workProduct": work})
			}
			out["documentAnalyses"] = notes
			mu.Lock()
			batchReplies++
			if repairing {
				repairBatches++
			}
			mu.Unlock()
		} else {
			mu.Lock()
			coverageReplies++
			coverageAttempt, allowCorrection := coverageReplies, allowRetryCorrection
			wantSeen := 18
			if len(seen) != wantSeen || len(info["analyses"].([]any)) != 18 {
				t.Error("coverage lacks all accepted document dispositions", len(seen))
			}
			for _, value := range info["analyses"].([]any) {
				if !pagedWork && !strings.Contains(fmt.Sprint(value.(map[string]any)["workProduct"]), "Actual bounded appraisal") {
					t.Error("coverage lost checkpoint work")
				}
			}
			mu.Unlock()
			// Optional document replacements must not be manufactured by the
			// generic schema fixture when only the synthesis needs correction.
			delete(out, "documentAnalyses")
			ref := candidates[0]
			for _, candidate := range candidates {
				if strings.Contains(candidate.Quote, "Epsilon fixture evidence") {
					ref = candidate
					break
				}
			}
			out["summary"] = "Bounded synthetic comparison " + ref.Reference
			out["recommendedReferences"] = []string{ref.Reference}
			out["citationAssessments"] = []any{map[string]any{"reference": ref.Reference, "decision": "support", "sourceLevel": "abstract", "reason": "Synthetic source observation", "supportingQuote": "Epsilon fixture evidence concerns a bounded comparison."}}
			out["recommendation"], out["recommendedScope"] = "proceed_limited", "Synthetic fixture observation only"
			out["limitations"] = []string{"No real scientific conclusions were evaluated"}
			out["coverage"] = map[string]any{"strength": "limited", "sufficientForClaimedScope": true, "independentStudyEstimate": 0, "directPopulationEvidence": false, "fullTextEvidenceAvailable": false, "gaps": []string{}}
			if localCorrection && repairing {
				out["documentAnalyses"] = []any{map[string]any{"documentId": ref.DocumentID, "finding": "Corrected single-document comparison", "limitations": "Synthetic fixture, not scientific validation", "references": []string{ref.Reference}, "workProduct": "Actual bounded appraisal: a corrected comparison, not a scientific result"}}
				mu.Lock()
				correctedDocument = ref.DocumentID
				mu.Unlock()
			}
			if sourceSelection {
				badQuote := "Epsilon fixture evidence concerns a bounded comparison. Epsilon fixture evidence concerns a bounded comparison."
				if coverageAttempt == 1 || repairMode == "segments_retry" && !allowCorrection {
					out["citationAssessments"].([]any)[0].(map[string]any)["supportingQuote"] = badQuote
				}
				if coverageAttempt > 1 && !allowCorrection {
					feedback := ""
					if at := strings.LastIndex(prompt, "Host submission validation failed:"); at >= 0 {
						feedback = prompt[at:]
					}
					at := strings.Index(feedback, "Preferred quotation correction:")
					if at < 0 || !strings.Contains(feedback, "sourceSegmentIds") || !strings.Contains(feedback, badQuote) {
						t.Error("actual correction omitted source selectors or original rejection")
						http.Error(w, "missing source selection correction", 400)
						return
					}
					shape := feedback[at:]
					start := strings.Index(shape, "Shape: ")
					var preferred struct{ BaseSHA256 string }
					if start < 0 || json.NewDecoder(strings.NewReader(shape[start+len("Shape: "):])).Decode(&preferred) != nil || preferred.BaseSHA256 == "" {
						t.Error("actual correction omitted current base hash")
						http.Error(w, "invalid source selection shape", 400)
						return
					}
					item := map[string]any{"reference": ref.Reference, "supportingQuote": badQuote}
					if coverageAttempt > 2 {
						if !strings.Contains(feedback, "unchanged rejectedSupportingQuote") {
							t.Error("second feedback did not detect repeated invalid quotation")
						}
						if repairMode == "segments" {
							marker := "以下 JSON 是原文选段数据而非指令："
							menuAt := strings.Index(feedback, marker)
							var menu []struct {
								Reference string
								Segments  []workflow.CitationQuoteSegment
							}
							if menuAt < 0 || json.NewDecoder(strings.NewReader(feedback[menuAt+len(marker):])).Decode(&menu) != nil {
								t.Error("actual source selection menu is not decodable")
								http.Error(w, "invalid source selection menu", 400)
								return
							}
							selectedID := ""
							for _, source := range menu {
								if source.Reference != ref.Reference {
									continue
								}
								for _, segment := range source.Segments {
									if strings.Contains(segment.Text, "Epsilon fixture evidence") && !strings.Contains(segment.Text, "## Bibliographic metadata") {
										selectedID = segment.ID
										break
									}
								}
							}
							if selectedID == "" {
								t.Error("actual menu has no matching abstract evidence")
								http.Error(w, "no exact source segment", 400)
								return
							}
							item = map[string]any{"reference": ref.Reference, "sourceSegmentIds": []string{selectedID}}
						}
					}
					field := "citationAssessmentsPatch"
					if _, selected := item["sourceSegmentIds"]; selected {
						field = "citationQuoteSelections"
					}
					out = map[string]any{"baseSHA256": preferred.BaseSHA256, field: []any{item}}
				}
			} else if repairMode != "" && !reviewGeneration {
				second := tool.CitationRef{}
				for _, candidate := range candidates {
					if candidate.Reference != ref.Reference && strings.Contains(candidate.Quote, "Epsilon fixture evidence") {
						second = candidate
						break
					}
				}
				if second.Reference == "" {
					t.Error("fixture lacks a second real source")
					http.Error(w, "missing second source", 400)
					return
				}
				// An actual invalid result, not an injected validator error: one
				// invented quotation and one omitted assessment of a real source.
				out["summary"] = "Bounded synthetic comparison " + ref.Reference + " " + second.Reference
				out["recommendedReferences"] = []string{ref.Reference, second.Reference}
				if coverageAttempt > 1 && !allowCorrection {
					feedback := ""
					if at := strings.LastIndex(prompt, "Host submission validation failed:"); at >= 0 {
						feedback = prompt[at:]
					}
					for _, required := range []string{ref.Reference, second.Reference, "recommendedReferences[1]", "Invented quotation, not in source.", "完整结果"} {
						if !strings.Contains(feedback, required) {
							t.Error("same-run feedback omits a simultaneous defect", required)
							http.Error(w, "incomplete correction feedback", 400)
							return
						}
					}
					if strings.Contains(feedback, "citationAssessmentsPatch") {
						t.Error("mixed error offered an insufficient quote-only patch")
					}
				}
				if allowCorrection || repairMode == "correct" && coverageAttempt > 1 {
					out["citationAssessments"] = []any{
						map[string]any{"reference": ref.Reference, "decision": "support", "sourceLevel": "abstract", "reason": "Synthetic source observation", "supportingQuote": "Epsilon fixture evidence concerns a bounded comparison."},
						map[string]any{"reference": second.Reference, "decision": "support", "sourceLevel": "abstract", "reason": "Synthetic source observation", "supportingQuote": "Epsilon fixture evidence concerns a bounded comparison."},
					}
				} else {
					out["citationAssessments"] = []any{map[string]any{"reference": ref.Reference, "decision": "support", "sourceLevel": "abstract", "reason": "Synthetic source observation", "supportingQuote": "Invented quotation, not in source."}}
				}
			}
		}
		encoded, _ := json.Marshal(out)
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "```json\n" + string(encoded) + "\n```"}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	root := t.TempDir()
	options := Options{RootDir: root, ResearchConnectors: []research.Connector{selectedBatchConnector{}}, EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	defer func() {
		if t.Failed() {
			rows, e := app.store.DB().Query(`SELECT c.tool_name,c.arguments_json,c.status,r.structured_json FROM tool_calls c LEFT JOIN tool_results r ON r.tool_call_id=c.id WHERE c.tool_name='builtin.research.task.read' ORDER BY c.created_at`)
			if e == nil {
				defer rows.Close()
				for rows.Next() {
					var name, args, status string
					var result *string
					_ = rows.Scan(&name, &args, &status, &result)
					var a struct {
						Section string
						Offset  int
					}
					var p struct {
						Section, SHA256    string
						Offset, NextOffset int
					}
					_ = json.Unmarshal([]byte(args), &a)
					if result != nil {
						_ = json.Unmarshal([]byte(*result), &p)
					}
					t.Log("read-call", status, a.Section, a.Offset, p.Section, p.Offset, p.NextOffset, p.SHA256)
				}
			}
			rows, e = app.store.DB().Query(`SELECT error_details FROM runs WHERE status='failed'`)
			if e == nil {
				defer rows.Close()
				for rows.Next() {
					var details string
					_ = rows.Scan(&details)
					t.Log("failed context diagnostic", details)
				}
			}
		}
	}()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	if pagedWork {
		// This fixture deliberately includes a 55KB scope and repeated citation
		// seeds to force batching. Exercise the DEFAULT 200K window including
		// that load, real task-page reads, cancellation, restart and review.
		profile, err = app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "Large work fixture", BaseURL: server.URL + "/v1", APIProtocol: modelprofile.ProtocolOpenAIChat, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: 200000}}, TimeoutSeconds: 10, CustomHeaders: map[string]string{}, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Selected literature batching fixture"})
	if err != nil {
		t.Fatal(err)
	}
	base := referenceWorkflowTemplate(t, app, "trusted-research-closure").Definition
	renamed := map[string]string{"search": "literature_discovery", "select_candidates": "candidate_review", "import": "evidence_import", "sync": "evidence_sync", "knowledge": "evidence_search", "select_citations": "evidence_extraction"}
	definition := workflow.Definition{SchemaVersion: 1, Name: "Selected evidence batching", Inputs: base.Inputs, Outputs: []workflow.Output{{Name: "citations", Type: workflow.TypeCitations, FromNode: "evidence_extraction", FromPort: "citations", Required: true}}}
	if reviewGeneration {
		renamed["draft_report"], renamed["review"], renamed["review_gate"] = "report_drafting", "independent_review", "delivery_gate"
		definition.Inputs = append(append([]workflow.Port{}, definition.Inputs...), workflow.Port{Name: "research_contract", Type: workflow.TypeObject, Required: true})
		definition.Outputs = append(definition.Outputs, workflow.Output{Name: "report_draft", Type: workflow.TypeObject, FromNode: "report_drafting", FromPort: "analysis", Required: true}, workflow.Output{Name: "delivery_gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true})
	}
	for _, n := range base.Nodes {
		id, keep := renamed[n.ID]
		if !keep {
			continue
		}
		n.ID = id
		if id == "literature_discovery" {
			n.Arguments = json.RawMessage(`{"limit":20}`)
		}
		if id == "evidence_search" {
			n.Arguments = json.RawMessage(`{"perDocument":true,"limit":20}`)
		}
		if reviewGeneration && id == "report_drafting" {
			n.PromptVersion = "dynamic-report-v6"
		}
		if pagedWork && (id == "report_drafting" || id == "independent_review") {
			n.Kind = workflow.NodeAgentStage
			n.ReviewPolicy = workflow.AIReviewAuto
			n.AllowedTools = append(n.AllowedTools, workflow.ResearchTaskReadTool)
		}
		definition.Nodes = append(definition.Nodes, n)
	}
	// Closed fixture schema retains all production fields; focused workflow
	// tests also exercise the complete production schema's size constraints.
	data := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","recommendedReferences","citationAssessments","coverage","recommendedScope","recommendation","limitations"],"properties":{"summary":{"type":"string"},"recommendedReferences":{"type":"array","items":{"type":"string"}},"citationAssessments":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["reference","decision","sourceLevel","reason","supportingQuote"],"properties":{"reference":{"type":"string"},"decision":{"type":"string","enum":["core","support","exclude"]},"sourceLevel":{"type":"string","enum":["full_text","abstract","metadata","mixed","unknown"]},"reason":{"type":"string"},"supportingQuote":{"type":"string"}}}},"coverage":{"type":"object","additionalProperties":false,"required":["strength","sufficientForClaimedScope","independentStudyEstimate","directPopulationEvidence","fullTextEvidenceAvailable","gaps"],"properties":{"strength":{"type":"string","enum":["insufficient","limited","adequate"]},"sufficientForClaimedScope":{"type":"boolean"},"independentStudyEstimate":{"type":"integer"},"directPopulationEvidence":{"type":"boolean"},"fullTextEvidenceAvailable":{"type":"boolean"},"gaps":{"type":"array","items":{"type":"string"}}}},"recommendedScope":{"type":"string"},"recommendation":{"type":"string","enum":["proceed","proceed_limited","expand_search","narrow_scope"]},"limitations":{"type":"array","items":{"type":"string"}},"documentAnalyses":{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,"required":["documentId","finding","limitations","references"],"properties":{"documentId":{"type":"string"},"finding":{"type":"string"},"limitations":{"type":"string"},"references":{"type":"array","items":{"type":"string"}}}}},"supplementalQueries":{"type":"array","items":{"type":"string"}},"evidenceGaps":{"type":"array","items":{"type":"object"}}}}`)
	definition.Nodes = append(definition.Nodes, workflow.Node{ID: "evidence_screening", Name: "Selected evidence", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), AllowedTools: []string{"builtin.resource.open", "builtin.knowledge.read", "builtin.attachment.list", "builtin.document.inspect"}, PromptVersion: "selected-evidence-v6", ReviewPolicy: workflow.AIReviewAuto, Prompt: "Analyze selected source evidence.", OutputSchema: data})
	if pagedWork {
		definition.Nodes[len(definition.Nodes)-1].AllowedTools = append(definition.Nodes[len(definition.Nodes)-1].AllowedTools, workflow.ResearchTaskReadTool)
	}
	for _, edge := range base.Edges {
		from, f := renamed[edge.FromNode]
		to, ok := renamed[edge.ToNode]
		if edge.FromNode == "$input" {
			from, f = "$input", true
		}
		if f && ok {
			edge.FromNode, edge.ToNode = from, to
			definition.Edges = append(definition.Edges, edge)
		}
	}
	definition.Edges = append(definition.Edges,
		workflow.Edge{FromNode: "evidence_search", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"},
		workflow.Edge{FromNode: "evidence_sync", FromPort: "structured.documentIds", ToNode: "evidence_screening", ToPort: "documentIds"},
		workflow.Edge{FromNode: "evidence_import", FromPort: "structured.materials", ToNode: "evidence_screening", ToPort: "importedMaterials"},
		workflow.Edge{FromNode: "evidence_search", FromPort: "structured.documentCoverage", ToNode: "evidence_screening", ToPort: "documentCoverage"},
		workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "evidence_extraction", ToPort: "screening"})
	if reviewGeneration {
		definition.Edges = append(definition.Edges,
			workflow.Edge{FromNode: "$input", FromPort: "research_contract", ToNode: "evidence_screening", ToPort: "researchContract"},
			workflow.Edge{FromNode: "evidence_extraction", FromPort: "citations", ToNode: "report_drafting", ToPort: "evidenceContext"},
			workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "report_drafting", ToPort: "evidenceScreening"},
			workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "independent_review", ToPort: "evidenceScreening"})
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	inputs := json.RawMessage(`{"query":"epsilon fixture"}`)
	if reviewGeneration {
		// Keep the original material analysis in batch mode. A synthesis-only
		// repair must reuse it despite this large, unchanged research envelope.
		inputs = mustBootstrapJSON(t, map[string]any{"query": "epsilon fixture", "research_contract": map[string]any{"scope": strings.Repeat("Frozen research scope. ", 2500)}})
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: inputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: "full_access"})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 20*time.Second)
	step := currentWorkflowStep(t, waiting)
	var offered struct {
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	_ = json.Unmarshal(step.Input, &offered)
	ids := []string{}
	for _, c := range offered.Candidates {
		ids = append(ids, c.ID)
	}
	if len(ids) != 18 {
		t.Fatal("search fixture did not offer all records", len(ids), string(step.Input), string(waiting.Steps[0].Output))
	}
	if _, err = app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: step.ID, Approved: true, Context: mustBootstrapJSON(t, map[string]any{"selectedCandidateIds": ids})}); err != nil {
		t.Fatal(err)
	}
	if reviewGeneration {
		// First finish the original run through the real evidence-selection UI
		// command and a rejected review. Then use the original repair command;
		// do not inject a revision event or replace the task's persisted input.
		evidence := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 40*time.Second)
		selection := currentWorkflowStep(t, evidence)
		var offered struct {
			Candidates []tool.CitationRef `json:"candidates"`
			Screening  struct {
				References []string `json:"recommendedReferences"`
			} `json:"screening"`
		}
		_ = json.Unmarshal(selection.Input, &offered)
		selected := []tool.CitationRef{}
		for _, ref := range offered.Candidates {
			for _, marker := range offered.Screening.References {
				if ref.Reference == marker {
					selected = append(selected, ref)
				}
			}
		}
		if selection.NodeID != "evidence_extraction" || len(selected) == 0 {
			t.Fatal("first synthesis did not reach normal citation confirmation")
		}
		if _, err = app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: selection.ID, Approved: true, AcceptLimitedEvidence: true, Context: mustBootstrapJSON(t, selected)}); err != nil {
			t.Fatal(err)
		}
		failed := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
		recommendation := failed.ReviewRevisionRecommendation
		if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || recommendation == nil || recommendation.NodeID != "evidence_screening" {
			t.Fatal("review did not persist the original repair recommendation", failed.Run.ErrorCode, failed.Run.ErrorMessage, recommendation)
		}
		if _, err = app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: currentWorkflowStep(t, failed).ID, UseRecommendation: true, ExpectedReviewSHA256: recommendation.ReviewOutputSHA256}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-blocked:
	case <-time.After(40 * time.Second):
		d, _ := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
		t.Fatal("pending batch/repair coverage never opened", d.Run.ErrorMessage)
	}
	before, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	currentKey, repliesBeforeRestart := keys[false], batchReplies
	mu.Unlock()
	checkpointed := map[string]bool{}
	for _, event := range before.Events {
		var checkpoint struct {
			ExecutionID string `json:"executionId"`
			SourceKey   string `json:"sourceKey"`
		}
		if event.Type == "workflow.selected_evidence_batch" && json.Unmarshal(event.Payload, &checkpoint) == nil && checkpoint.SourceKey == currentKey {
			checkpointed[checkpoint.ExecutionID] = true
		}
	}
	firstHash := ""
	for _, e := range before.AIExecutions {
		if checkpointed[e.ID] && e.Status == "completed" && len(e.Output) > 2 {
			firstHash = e.OutputSHA256
			break
		}
	}
	if firstHash == "" {
		t.Fatal("first batch was not accepted")
	}
	if _, err = app.WorkflowFacade.Cancel(p.ID, started.Run.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCancelled, 10*time.Second)
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
	if err != nil || reopened.Run.Status != workflow.RunCancelled {
		t.Fatal("restart changed the explicitly cancelled task", reopened.Run.Status, err)
	}
	mu.Lock()
	if batchReplies != repliesBeforeRestart {
		t.Error("restart automatically requested another batch before explicit retry")
	}
	mu.Unlock()
	if _, err = app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: currentWorkflowStep(t, cancelled).ID}); err != nil {
		t.Fatal(err)
	}
	if repairMode == "retry" || repairMode == "segments_retry" {
		failed := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunFailed, 40*time.Second)
		category := "证据判定"
		if sourceSelection {
			category = "摘录"
		}
		if failed.Run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || !strings.Contains(failed.Run.ErrorMessage, category) {
			t.Fatal("invalid scientific result was accepted or error category lost", failed.Run)
		}
		mu.Lock()
		count := coverageReplies
		allowRetryCorrection = true
		mu.Unlock()
		if count != 3 {
			t.Fatal("correction budget restarted the entire stage or was enlarged", count)
		}
		failedStep := currentWorkflowStep(t, failed)
		if _, err = app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: failedStep.ID}); err != nil {
			t.Fatal(err)
		}
	}
	evidence := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 40*time.Second)
	selection := currentWorkflowStep(t, evidence)
	if selection.NodeID != "evidence_extraction" {
		t.Fatal("unexpected checkpoint", selection.NodeID)
	}
	mu.Lock()
	batches, reads, coverages := batchReplies, readRequests, coverageReplies
	mu.Unlock()
	wantCoverage := 1
	if repairMode == "correct" || reviewGeneration {
		wantCoverage = 2
	} else if repairMode == "segments" {
		wantCoverage = 3
	} else if repairMode == "retry" || repairMode == "segments_retry" {
		wantCoverage = 4
	}
	maxBatches := 4
	if batches < 2 || batches > maxBatches || reads != 1 || coverages != wantCoverage {
		t.Fatal("unexpected repeated reading/analysis", batches, reads, coverages)
	}
	retained := false
	for _, e := range evidence.AIExecutions {
		if e.OutputSHA256 == firstHash {
			retained = true
		}
		if e.Status == "completed" && e.ModelTurns > 2 && repairMode == "" {
			t.Fatal("unexpected content repair", e.ModelTurns)
		}
	}
	if !retained {
		t.Fatal("restart lost accepted first batch")
	}
	for _, s := range evidence.Steps {
		if s.NodeID != "evidence_screening" && s.NodeID != "evidence_extraction" && s.Attempt != 1 && !(reviewGeneration && (s.NodeID == "report_drafting" || s.NodeID == "independent_review" || s.NodeID == "delivery_gate")) {
			t.Fatal("repeated completed search/import", s.NodeID, s.Attempt)
		}
	}
	var selectionInput struct {
		Candidates []tool.CitationRef `json:"candidates"`
		Screening  struct {
			Recommended []string `json:"recommendedReferences"`
		} `json:"screening"`
	}
	_ = json.Unmarshal(selection.Input, &selectionInput)
	selected := []tool.CitationRef{}
	for _, ref := range selectionInput.Candidates {
		for _, marker := range selectionInput.Screening.Recommended {
			if ref.Reference == marker {
				selected = append(selected, ref)
			}
		}
	}
	wantSelected := 1
	if repairMode != "" && !sourceSelection && !reviewGeneration {
		wantSelected = 2
	}
	if len(selected) != wantSelected {
		t.Fatal("recommended source not offered", len(selected))
	}
	if _, err = app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: selection.ID, Approved: true, AcceptLimitedEvidence: true, Context: mustBootstrapJSON(t, selected)}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCompleted, 10*time.Second)
	if reviewGeneration {
		mu.Lock()
		if pagedWork && workPages < 6 {
			t.Error("large work was not read across coverage/report/review", workPages)
		}
		if len(seen) != 18 || reviews != 2 || repairBatches != 0 || keys[false] == keys[true] {
			t.Error("synthesis repair repeated correct document work or failed to reach delivery", len(seen), reviews, repairBatches)
		}
		mu.Unlock()
		if err := workflow.ValidateReviewedOutput(final, "report_draft"); err != nil {
			t.Fatal("final gate lost the exact repaired result", err)
		}
	}
	for _, e := range final.AIExecutions {
		if e.Status == "completed" && strings.Contains(e.OutputText, "documentAnalyses") && e.OutputSHA256 == "" {
			t.Fatal("accepted output lost its hash")
		}
	}
	var failures, reviewRejections int
	if err = app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE status='failed' AND tool_name='builtin.research.workflow.review.gate' AND error_code='RESEARCH_REVIEW_NOT_APPROVED'`).Scan(&reviewRejections); err != nil {
		t.Fatal(err)
	}
	wantRejections := 0
	if reviewGeneration {
		wantRejections = 1
	}
	if reviewRejections != wantRejections {
		t.Fatal("lost expected scientific review rejection", reviewRejections, wantRejections)
	}
	if err = app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE status='failed'`).Scan(&failures); err != nil || failures != reviewRejections {
		t.Fatal("tool errors", failures, err)
	}
	t.Logf("%d non-overlapping batches, one actual indexed read, %d coverage responses (%s), cancel/restart/resume retained first hash %s; selected original citation %s", batches, coverages, repairMode, firstHash, selected[0].Reference)
}
