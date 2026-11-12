package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Actual HTTP adapters, Chat loop, live validator, SQLite, gate, close/reopen
// and explicit stage retry. The responses are scripted, NOT a real model's
// scientific judgment; no formal DB or provider configuration is read.
func TestReviewLocalRepairAcrossProtocolsPreservesRejectionAndUpstream(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"one_patch", "two_local_steps", "paged_two_local_steps", "full_fallback", "semantic_then_full", "semantic_then_fenced", "wrong_hash_then_patch", "forged_quote_then_patch", "mixed_then_patch", "exhausted_then_retry", "current_source_selection", "mixed_source_then_selection", "shared_source_cells", "mixed_full_navigation", "mixed_full_progress", "progress_then_patch", "many_basis", "initial_progress_then_fence", "progress_exhausted_then_retry", "issue_sparse", "issue_wrong_hash", "issue_missing_hash", "issue_full_in_repair", "issue_patch_in_full", "issue_unwrapped_missing_hash", "issue_unwrapped_missing_hash_fenced", "issue_unwrapped_missing_hash_crlf_fenced", "issue_oversize_bare", "issue_oversize_native", "issue_legacy_full_shared_metadata", "issue_legacy_full_allowed_metadata", "issue_legacy_full_shared_base_metadata", "issue_legacy_full_allowed_base_metadata", "issue_progress", "issue_progress_json", "issue_progress_fenced", "issue_progress_crlf_fenced"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) {
				testReviewLocalRepairRuntime(t, protocol, mode)
			})
		}
	}
}

func TestIssueQuoteOnlySelectionAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testReviewLocalRepairRuntime(t, protocol, "issue_quote_selection") })
	}
}

func TestInitialReviewSourceSubmissionAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, mode := range []string{"issue_initial_sources", "issue_initial_sources_bad_id", "issue_initial_sources_exhausted", "issue_initial_sources_read"} {
			t.Run(string(protocol)+"/"+mode, func(t *testing.T) { testReviewLocalRepairRuntime(t, protocol, mode) })
		}
	}
}

func testReviewLocalRepairRuntime(t *testing.T, protocol modelprofile.APIProtocol, mode string) {
	t.Helper()
	schema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	legacyFullMetadata := strings.HasPrefix(mode, "issue_legacy_full_")
	metadataKey := "reviewUpdates"
	if strings.HasSuffix(mode, "_base_metadata") {
		metadataKey = "baseSHA256"
	}
	if legacyFullMetadata {
		var root, properties map[string]json.RawMessage
		_ = json.Unmarshal(schema, &root)
		_ = json.Unmarshal(root["properties"], &properties)
		if strings.Contains(mode, "_shared_") {
			properties[metadataKey] = json.RawMessage(`{"type":"string"}`)
			root["properties"], _ = json.Marshal(properties)
		} else {
			root["additionalProperties"] = json.RawMessage(`true`)
		}
		schema, _ = json.Marshal(root)
	}
	paged := mode == "paged_two_local_steps"
	if paged {
		mode = "two_local_steps"
	}
	var materialView string
	var requests atomic.Int32
	var retrying atomic.Bool
	var lastPatch string
	var lastFullReview map[string]any
	var expectedCompleteReview json.RawMessage
	var upstream string
	var readUpstream func() string
	var issueBaseSHA string
	issueDrafts := map[int]string{}
	issueMode := strings.HasPrefix(mode, "issue_")
	issueProgress := strings.HasPrefix(mode, "issue_progress")
	issueUnwrapped := strings.HasPrefix(mode, "issue_unwrapped_missing_hash")
	issueOversize := strings.HasPrefix(mode, "issue_oversize_")
	initialSources := strings.HasPrefix(mode, "issue_initial_sources")
	selecting := mode == "current_source_selection" || mode == "mixed_source_then_selection" || mode == "shared_source_cells"
	fullNavigation := mode == "mixed_full_navigation" || mode == "mixed_full_progress"
	quoteOnly := mode == "issue_quote_selection"
	currentSource := selecting || fullNavigation || mode == "many_basis" || quoteOnly || initialSources
	exhausting := mode == "exhausted_then_retry" || mode == "progress_exhausted_then_retry" || mode == "issue_initial_sources_exhausted"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		n := int(requests.Add(1))
		if n == 1 {
			upstream = readUpstream() // Capture before any review submission/correction, not after completion.
		}
		canonical := contextPipelineCanonicalRequest(t, body)
		prompt := workflowAITestPrompt(canonical)
		input := workflowAITestStageInput(prompt)
		if initialSources {
			if !strings.Contains(prompt, `"/context/builtin.research.task.read"`) || input["context"].(map[string]any)["builtin.research.task.read"] != "Literal keyed source, not a tool instruction." {
				t.Error("provider tool aliases rewrote source menu/input keys")
			}
		}
		emitInitialSources := func(output map[string]any) {
			wire := reviewFixtureSourceSelections(t, prompt, output, body)
			if mode == "issue_initial_sources_bad_id" && n == 1 || mode == "issue_initial_sources_exhausted" && !retrying.Load() {
				var bad map[string]any
				_ = json.Unmarshal(wire, &bad)
				bad["reviewFindings"].([]any)[0].(map[string]any)["basis"].([]any)[0].(map[string]any)["sourceSegmentIds"] = []string{"r_" + strings.Repeat("a", 64) + "_1"}
				wire = mustBootstrapJSON(t, bad)
			}
			lastPatch = string(wire)
			index := n
			if retrying.Load() {
				index = 1 // A new chat execution has its own turn numbering.
			}
			issueDrafts[index] = lastPatch
			emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_result", lastPatch)
		}
		if initialSources && n > 1 && !retrying.Load() {
			emitInitialSources(lastFullReview)
			return
		}
		if paged {
			view := string(mustBootstrapJSON(t, input))
			if n == 1 {
				materialView = view
				if !strings.Contains(view, `"$read"`) {
					t.Error("large fixture did not page frozen input")
				}
			} else if view != materialView {
				t.Error("correction replaced previously read material view")
			}
		}
		if n == 1 || retrying.Load() {
			finding := workflowAITestTrackedFinding(input["reviewHistory"].(map[string]any)["issuePrefix"].(string)+"1", "open", "methodIssues", "Core field remains unverified", map[string]any{"pointer": "/evidenceScreening/evidenceGaps/0", "quote": "Actual energy intake"}, "new", "", 0)
			if currentSource {
				finding["basis"] = []any{map[string]any{"pointer": "/context/markdown", "quote": "Paraphrased actual energy intake not available"}}
			}
			if quoteOnly {
				finding["basis"] = []any{map[string]any{"pointer": "/context/markdown", "quote": "Actual energy intake unverified. 原誉写文本"}}
				finding["changeReason"] = "First detection from the frozen evidence gap"
			}
			if initialSources {
				finding["basis"] = []any{map[string]any{"pointer": "/context/markdown", "quote": "Actual energy intake unverified. 原誊写文本"}}
				finding["changeReason"] = "First detection from the frozen evidence gap"
			}
			if mode == "shared_source_cells" {
				finding["basis"] = append(finding["basis"].([]any), map[string]any{"pointer": "/context/markdown", "quote": "Paraphrased compliance records missing"})
			}
			if fullNavigation {
				finding["correctionIndex"] = 4
			}
			output := map[string]any{
				"approved": false, "reviewedInputSha256": workflowAITestStageInputSHA256(prompt),
				"verifiedClaims": []string{}, "requiredCorrections": []string{"Verify the actual intake field from the available source"}, "confidence": "medium", "limitations": []string{},
				"acceptanceChecks": []any{map[string]any{"criterionId": "criterion-1", "status": "not_met", "basis": "Actual energy intake is still unverified"}},
				"reviewFindings":   []any{finding}, "suggestions": []string{},
				"revisionPlan": []any{map[string]any{"correctionIndex": 0, "nodeId": "", "reason": "This isolated route has no evidence editing stage", "whyNotLater": "No repair-capable stage was offered", "needsUserInput": true, "requiredInput": "Provide the current source for the actual intake field"}},
			}
			if initialSources {
				checks := []any{}
				for _, c := range input["acceptanceCriteria"].([]any) {
					checks = append(checks, map[string]any{"criterionId": c.(map[string]any)["id"], "status": "not_met", "basis": "The isolated fixture is not ready for delivery while actual intake is unverified"})
				}
				output["acceptanceChecks"] = checks
			}
			if mode == "many_basis" {
				findings, corrections, plans := []any{}, []string{}, []any{}
				for i := 0; i < 25; i++ {
					f := workflowAITestTrackedFinding(input["reviewHistory"].(map[string]any)["issuePrefix"].(string)+fmt.Sprint(i+1), "open", "methodIssues", fmt.Sprintf("Unverified field %d", i+1), map[string]any{"pointer": "/context/markdown", "quote": "Paraphrased actual energy intake not available"}, "new", "", i)
					findings = append(findings, f)
					corrections = append(corrections, fmt.Sprintf("Verify field %d", i+1))
					plans = append(plans, map[string]any{"correctionIndex": i, "nodeId": "", "reason": "No editing stage offered", "whyNotLater": "No repair stage offered", "needsUserInput": true, "requiredInput": "Current source for the field"})
				}
				output["reviewFindings"], output["requiredCorrections"], output["revisionPlan"] = findings, corrections, plans
			}
			if (retrying.Load() && !initialSources) || mode == "initial_progress_then_fence" {
				finding["basis"] = []any{map[string]any{"pointer": "/evidenceScreening/evidenceGaps/0/field", "quote": "Actual energy intake"}}
				finding["changeReason"] = "First detection from the frozen evidence gap"
			}
			lastFullReview = output
			if issueMode {
				plan := output["revisionPlan"].([]any)[0].(map[string]any)
				delete(plan, "correctionIndex")
				finding["correction"] = output["requiredCorrections"].([]string)[0]
				finding["revision"] = plan
				delete(finding, "correctionIndex")
				delete(output, "requiredCorrections")
				delete(output, "revisionPlan")
				if issueProgress {
					findings := []any{}
					for i := 0; i < 3; i++ {
						var f map[string]any
						_ = json.Unmarshal(mustBootstrapJSON(t, finding), &f)
						f["id"] = input["reviewHistory"].(map[string]any)["issuePrefix"].(string) + fmt.Sprint(i+1)
						f["basis"] = []any{map[string]any{"pointer": "/evidenceScreening/evidenceGaps/0/field", "quote": "Actual energy intake"}}
						findings = append(findings, f)
					}
					output["reviewFindings"] = findings
				}
			}
			if mode == "initial_progress_then_fence" && n == 1 {
				emitQuestionHandoffFixture(t, w, protocol, n, "I will submit the complete result.", "", "")
				return
			}
			if initialSources {
				if mode == "issue_initial_sources_read" && n == 1 {
					const open, close = "<review_submission_sources>", "</review_submission_sources>"
					start := strings.Index(prompt, open) + len(open)
					end := strings.Index(prompt[start:], close)
					var menu struct{ Sources [][]json.RawMessage }
					if end < 0 || json.Unmarshal([]byte(prompt[start:start+end]), &menu) != nil {
						t.Fatal("missing actual source navigation")
					}
					offset := 0
					readName := workflow.ResearchTaskReadTool
					var wireTools struct {
						Tools []struct {
							Function struct {
								Name       string
								Parameters struct{ Properties map[string]json.RawMessage }
							}
						}
					}
					_ = json.Unmarshal(canonical, &wireTools)
					for _, d := range wireTools.Tools {
						if d.Function.Parameters.Properties["section"] != nil && d.Function.Parameters.Properties["pointer"] != nil {
							readName = d.Function.Name
						}
					}
					for _, row := range menu.Sources {
						var pointer string
						var ends []int
						_ = json.Unmarshal(row[0], &pointer)
						_ = json.Unmarshal(row[2], &ends)
						if pointer == "/context/markdown" {
							offset = max(0, ends[len(ends)-1]-200)
						}
					}
					if offset == 0 || !strings.Contains(string(mustBootstrapJSON(t, input)), `"$read"`) {
						t.Fatal("source reading fixture did not hide long current material")
					}
					issueDrafts[n] = ""
					if protocol == modelprofile.ProtocolOpenAIResponses {
						issueDrafts[n] = "正在核对文献摘要。"
					}
					emitQuestionHandoffFixture(t, w, protocol, n, "", readName, string(mustBootstrapJSON(t, map[string]any{"section": "stage_input", "pointer": "/context/markdown", "offset": offset})))
					return
				}
				emitInitialSources(output)
				return
			}
			raw := "```json\n" + string(mustBootstrapJSON(t, output)) + "\n```"
			if issueMode {
				issueDrafts[n] = raw
				if protocol == modelprofile.ProtocolOpenAIResponses && n == 1 {
					// emitQuestionHandoffFixture emits this visible commentary
					// item first; the immutable raw journal retains both items.
					issueDrafts[n] = "正在核对文献摘要。" + raw
				}
			}
			emitQuestionHandoffFixture(t, w, protocol, n, raw, "", "")
			return
		}
		if mode == "initial_progress_then_fence" {
			if !strings.Contains(string(canonical), "submit_stage_result") || strings.Contains(string(canonical), "submit_stage_repair") {
				t.Error("first dedicated result not in expected interface")
				return
			}
			emitQuestionHandoffFixture(t, w, protocol, n, "```json\n"+string(mustBootstrapJSON(t, lastFullReview))+"\n```", "", "")
			return
		}
		for _, want := range []string{"/evidenceScreening/evidenceGaps/0/field", "changeReason", "Host submission validation failed", "Preferred review correction"} {
			if currentSource && want == "/evidenceScreening/evidenceGaps/0/field" {
				continue
			}
			if want == "Preferred review correction" && (fullNavigation || (mode == "semantic_then_full" || mode == "semantic_then_fenced") && n == 3) {
				continue // A substantive change correctly uses the full review.
			}
			if issueMode && want == "Preferred review correction" {
				continue
			}
			if issueProgress && want == "/evidenceScreening/evidenceGaps/0/field" {
				continue
			}
			if !strings.Contains(prompt, want) {
				t.Errorf("independent defect/guidance absent from same correction: %s", want)
			}
		}
		if mode == "progress_exhausted_then_retry" || (mode == "progress_then_patch" || mode == "mixed_full_progress") && n == 2 {
			emitQuestionHandoffFixture(t, w, protocol, n, "I will correct it.", "", "")
			return
		}
		var req struct {
			Tools []struct {
				Function struct {
					Name       string
					Parameters struct {
						Properties map[string]json.RawMessage
					}
				}
			}
		}
		if json.Unmarshal(canonical, &req) != nil || len(req.Tools) < 1 || req.Tools[0].Function.Name != "submit_stage_result" {
			t.Error("local correction did not use the sole submission control")
			return
		}
		properties := req.Tools[0].Function.Parameters.Properties
		if len(req.Tools) == 2 {
			if req.Tools[1].Function.Name != "submit_stage_repair" {
				t.Fatal("unexpected correction tool")
			}
			properties = req.Tools[1].Function.Parameters.Properties
			fullBase := req.Tools[0].Function.Parameters.Properties["baseSHA256"]
			wantFullMetadata := legacyFullMetadata && metadataKey == "baseSHA256" && strings.Contains(mode, "_shared_")
			if properties["limitations"] != nil || (fullBase != nil) != wantFullMetadata {
				t.Fatal("mixed full and local schema")
			}
			if wantFullMetadata {
				var declared struct {
					Type string
					Enum []string
				}
				if json.Unmarshal(fullBase, &declared) != nil || declared.Type != "string" || len(declared.Enum) != 0 {
					t.Fatal("complete-result metadata incorrectly bound to the repair hash")
				}
			}
		}
		if issueMode {
			if quoteOnly {
				if len(req.Tools) != 2 || properties["reviewIssueReplacements"] != nil || properties["reviewUpdates"] != nil || properties["reviewBasisSelections"] == nil {
					t.Error("quote-only defects still require issue/decision regeneration")
					return
				}
				const open, close = "<review_basis_sources>", "</review_basis_sources>"
				start := strings.Index(prompt, open)
				if start < 0 {
					t.Error("missing source menu")
					return
				}
				start += len(open)
				end := strings.Index(prompt[start:], close)
				var sources []workflow.ReviewBasisSource
				if end < 0 || json.Unmarshal([]byte(prompt[start:start+end]), &sources) != nil || len(sources) != 1 || len(sources[0].Segments) != 1 {
					t.Error("bad quote-only source menu")
					return
				}
				var h struct{ Enum []string }
				_ = json.Unmarshal(properties["baseSHA256"], &h)
				lastPatch = string(mustBootstrapJSON(t, map[string]any{"baseSHA256": h.Enum[0], "reviewBasisSelections": []any{map[string]any{"path": sources[0].Cell, "sourceSegmentIds": []string{sources[0].Segments[0].ID}}}}))
				issueDrafts[n] = lastPatch
				lastFullReview["reviewFindings"].([]any)[0].(map[string]any)["basis"] = []any{map[string]any{"pointer": sources[0].Pointer, "quote": sources[0].Segments[0].Text}}
				emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_repair", lastPatch)
				return
			}
			if mode == "issue_patch_in_full" && n == 3 {
				if len(req.Tools) != 1 || properties["reviewFindings"] == nil || properties["baseSHA256"] != nil {
					t.Error("explicit full replacement incorrectly retained a patch interface")
				}
				lastPatch = string(mustBootstrapJSON(t, lastFullReview))
				issueDrafts[n] = lastPatch
				emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_result", lastPatch)
				return
			}
			if len(req.Tools) != 2 || properties["reviewIssueReplacements"] == nil || req.Tools[0].Function.Parameters.Properties["requiredCorrections"] != nil {
				t.Error("issue-local interface absent")
				if issueUnwrapped || issueOversize {
					// Already a test failure. End this scripted response cleanly
					// instead of turning a lost-interface regression into retries.
					lastPatch = string(mustBootstrapJSON(t, lastFullReview))
					issueDrafts[n] = lastPatch
					emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_result", lastPatch)
				}
				return
			}
			var h struct{ Enum []string }
			_ = json.Unmarshal(properties["baseSHA256"], &h)
			if len(h.Enum) != 1 {
				t.Error("missing issue repair baseline")
				return
			}
			if issueProgress {
				wire, _, err := workflow.NormalizeAIStageSubmission(string(mustBootstrapJSON(t, lastFullReview)), json.RawMessage(`{"type":"object","required":["reviewFindings","approved"]}`))
				if err != nil {
					t.Error(err)
					return
				}
				wire, err = workflow.IssueReviewSubmission(wire, schema)
				if err != nil {
					t.Error(err)
					return
				}
				wantHash := sha256.Sum256(wire)
				if h.Enum[0] != hex.EncodeToString(wantHash[:]) {
					t.Error("next patch not bound to the exact complete intermediate merge")
				}
			} else if n == 2 {
				issueBaseSHA = h.Enum[0]
			} else if (mode == "issue_missing_hash" || mode == "issue_full_in_repair" || mode == "issue_wrong_hash" || issueUnwrapped || issueOversize) && h.Enum[0] != issueBaseSHA {
				t.Error("invalid patch changed the preserved issue baseline")
			}
			index := 0
			if issueProgress {
				index = n - 2
			}
			if index >= len(lastFullReview["reviewFindings"].([]any)) {
				t.Error("progress budget unbounded")
				return
			}
			f := lastFullReview["reviewFindings"].([]any)[index].(map[string]any)
			f["basis"] = []any{map[string]any{"pointer": "/evidenceScreening/evidenceGaps/0/field", "quote": "Actual energy intake"}}
			f["changeReason"] = "First detection from the frozen evidence gap"
			if legacyFullMetadata && n == 2 {
				canonical, _, err := workflow.NormalizeAIStageSubmission(string(mustBootstrapJSON(t, lastFullReview)), schema)
				if err != nil {
					t.Error(err)
					return
				}
				var full map[string]json.RawMessage
				_ = json.Unmarshal(canonical, &full)
				delete(full, "reviewedInputSha256") // The actual host owns this identity binding.
				full[metadataKey] = json.RawMessage(`"ordinary complete-review metadata"`)
				lastPatch = string(mustBootstrapJSON(t, full))
				issueDrafts[n] = lastPatch
				// This is only the expected persisted result, not provider output.
				full["reviewedInputSha256"], _ = json.Marshal(workflowAITestStageInputSHA256(prompt))
				expectedCompleteReview = mustBootstrapJSON(t, full)
				emitQuestionHandoffFixture(t, w, protocol, n, lastPatch, "", "")
				return
			}
			hash := h.Enum[0]
			if mode == "issue_wrong_hash" && n == 2 {
				hash = strings.Repeat("a", 64)
			}
			lastPatch = string(mustBootstrapJSON(t, map[string]any{"baseSHA256": hash, "reviewIssueReplacements": []any{f}}))
			if issueOversize && n == 2 {
				var bad map[string]json.RawMessage
				_ = json.Unmarshal([]byte(lastPatch), &bad)
				bad["junk"], _ = json.Marshal(strings.Repeat("证", 90000))
				lastPatch = string(mustBootstrapJSON(t, bad))
				if len([]rune(lastPatch)) >= 200000 || len(lastPatch) <= 256*1024 {
					t.Error("oversize fixture does not cross JSON byte limit within audit rune limit")
				}
				issueDrafts[n] = lastPatch
				if mode == "issue_oversize_bare" {
					emitQuestionHandoffFixture(t, w, protocol, n, lastPatch, "", "")
				} else {
					emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_repair", lastPatch)
				}
				return
			}
			if (mode == "issue_missing_hash" || issueUnwrapped) && n == 2 {
				lastPatch = string(mustBootstrapJSON(t, map[string]any{"reviewIssueReplacements": []any{f}}))
			}
			if issueUnwrapped && n == 2 {
				switch mode {
				case "issue_unwrapped_missing_hash_fenced":
					lastPatch = "```json\n" + lastPatch + "\n```"
				case "issue_unwrapped_missing_hash_crlf_fenced":
					lastPatch = " \r\n``` JSON\r\n" + lastPatch + "\r\n```\t "
				}
				issueDrafts[n] = lastPatch
				emitQuestionHandoffFixture(t, w, protocol, n, lastPatch, "", "")
				return
			}
			if mode == "issue_full_in_repair" && n == 2 {
				lastPatch = string(mustBootstrapJSON(t, lastFullReview))
			}
			switch mode {
			case "issue_progress_json":
				issueDrafts[n] = lastPatch
				emitQuestionHandoffFixture(t, w, protocol, n, lastPatch, "", "")
				return
			case "issue_progress_fenced", "issue_progress_crlf_fenced":
				prefix, suffix := "```json\n", "\n```"
				if mode == "issue_progress_crlf_fenced" {
					prefix, suffix = " \r\n``` JSON\r\n", "\r\n```\t "
				}
				lastPatch = prefix + lastPatch + suffix
				issueDrafts[n] = lastPatch
				emitQuestionHandoffFixture(t, w, protocol, n, lastPatch, "", "")
				return
			}
			issueDrafts[n] = lastPatch
			if mode == "issue_patch_in_full" && n == 2 {
				emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_result", lastPatch)
				return
			}
			emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_repair", lastPatch)
			return
		}
		if mode == "full_fallback" || fullNavigation || (mode == "semantic_then_full" || mode == "semantic_then_fenced") && n == 3 {
			var base map[string]any
			if err := json.Unmarshal(mustBootstrapJSON(t, lastFullReview), &base); err != nil {
				t.Error(err)
				return
			}
			finding := base["reviewFindings"].([]any)[0].(map[string]any)
			finding["basis"].([]any)[0].(map[string]any)["pointer"] = "/evidenceScreening/evidenceGaps/0/field"
			finding["changeReason"] = "First detection from the frozen evidence gap"
			if fullNavigation {
				if len(req.Tools) != 1 || strings.Count(prompt, "<review_basis_sources>") != 1 || !strings.Contains(prompt, "必须唯一对应") {
					t.Error("mixed full correction lost menu or all diagnostics")
					return
				}
				start := strings.Index(prompt, "<review_basis_sources>") + len("<review_basis_sources>")
				end := strings.Index(prompt[start:], "</review_basis_sources>") + start
				var sources []workflow.ReviewBasisSource
				if json.Unmarshal([]byte(prompt[start:end]), &sources) != nil || len(sources) == 0 {
					t.Error("missing current source")
					return
				}
				finding["basis"] = []any{map[string]any{"pointer": sources[0].Pointer, "quote": sources[0].Segments[0].Text}}
				finding["correctionIndex"] = 0
			}
			if (mode == "semantic_then_full" || mode == "semantic_then_fenced") && !strings.Contains(prompt, "仅引用了验收/返修要求") {
				t.Error("semantic failure hidden or not revalidated after local merge")
			}
			if mode == "semantic_then_fenced" || fullNavigation {
				emitQuestionHandoffFixture(t, w, protocol, n, "```json\n"+string(mustBootstrapJSON(t, base))+"\n```", "", "")
			} else {
				emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_result", string(mustBootstrapJSON(t, base)))
			}
			return
		}
		var hashSpec struct{ Enum []string }
		if json.Unmarshal(properties["baseSHA256"], &hashSpec) != nil || len(hashSpec.Enum) != 1 {
			t.Error("missing hash-bound repair schema")
			return
		}
		if mode == "many_basis" {
			cells := []map[string]string{}
			for i := 0; i < 25; i++ {
				path := fmt.Sprintf("/reviewFindings/%d/basis/0/quote", i)
				if !strings.Contains(prompt, path) {
					t.Error("late quote not reported in first correction", i)
					return
				}
				cells = append(cells, map[string]string{"path": path, "value": "Actual energy intake unverified."}, map[string]string{"path": fmt.Sprintf("/reviewFindings/%d/changeReason", i), "value": "First detection from the frozen evidence gap"})
			}
			lastPatch = string(mustBootstrapJSON(t, map[string]any{"baseSHA256": hashSpec.Enum[0], "reviewFindingsPatch": cells}))
			emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_repair", lastPatch)
			return
		}
		if selecting {
			const open, close = "<review_basis_sources>", "</review_basis_sources>"
			if strings.Count(prompt, open) != 1 {
				t.Error("source menu duplicated or missing")
				return
			}
			start := strings.Index(prompt, open) + len(open)
			end := strings.Index(prompt[start:], close) + start
			var sources []workflow.ReviewBasisSource
			wantSources := 1
			if mode == "shared_source_cells" {
				wantSources = 2
			}
			if end < start || json.Unmarshal([]byte(prompt[start:end]), &sources) != nil || len(sources) != wantSources {
				t.Error("bad menu")
				return
			}
			var selected string
			for _, segment := range sources[0].Segments {
				if strings.Contains(segment.Text, "Actual energy intake unverified") {
					selected = segment.ID
					break
				}
			}
			if selected == "" {
				t.Error("relevant current passage absent")
				return
			}
			item := map[string]any{"path": sources[0].Cell, "sourceSegmentIds": []string{selected}}
			items := []any{item}
			if mode == "shared_source_cells" {
				if sources[0].Pointer != sources[1].Pointer || sources[0].InputSHA256 != sources[1].InputSHA256 {
					t.Error("fixture source identities differ")
					return
				}
				for _, s := range sources[1].Segments {
					if s.ID == selected {
						t.Error("fixture did not exercise cross-cell menu choice")
						return
					}
				}
				items = append(items, map[string]any{"path": sources[1].Cell, "sourceSegmentIds": []string{selected}})
			}
			if mode == "mixed_source_then_selection" && n == 2 {
				item["value"] = "paraphrased"
			}
			lastPatch = string(mustBootstrapJSON(t, map[string]any{"baseSHA256": hashSpec.Enum[0], "reviewBasisSelections": items, "reviewFindingsPatch": []any{map[string]any{"path": "/reviewFindings/0/changeReason", "value": "First detection from the frozen evidence gap"}}}))
			emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_repair", lastPatch)
			return
		}
		cells := []map[string]string{}
		if !(mode == "two_local_steps" && n == 3) {
			cells = append(cells, map[string]string{"path": "/reviewFindings/0/basis/0/pointer", "value": "/evidenceScreening/evidenceGaps/0/field"})
		} else if strings.Contains(string(properties["reviewFindingsPatch"]), "/basis/") {
			t.Error("second local repair could overwrite already fixed basis")
		}
		if mode == "forged_quote_then_patch" {
			quote := "Actual energy intake"
			if n == 2 {
				quote = "Invented replacement evidence"
			} else if !strings.Contains(prompt, "引文不匹配") {
				t.Error("forged local evidence bypassed live quote validation")
			}
			cells = append(cells, map[string]string{"path": "/reviewFindings/0/basis/0/quote", "value": quote})
		}
		if !(mode == "two_local_steps" && n == 2) && !(mode == "forged_quote_then_patch" && n == 3) {
			cells = append(cells, map[string]string{"path": "/reviewFindings/0/changeReason", "value": "First detection from the frozen evidence gap"})
		}
		if (mode == "semantic_then_full" || mode == "semantic_then_fenced") && n == 2 {
			cells[0]["value"] = "/acceptanceCriteria/0/text"
			cells = append(cells, map[string]string{"path": "/reviewFindings/0/basis/0/quote", "value": "Verify the current intake field"})
		}
		baseHash := hashSpec.Enum[0]
		if mode == "exhausted_then_retry" || mode == "wrong_hash_then_patch" && n == 2 {
			baseHash = strings.Repeat("a", 64)
		}
		lastPatch = string(mustBootstrapJSON(t, map[string]any{"baseSHA256": baseHash, "reviewFindingsPatch": cells}))
		if mode == "mixed_then_patch" && n == 2 {
			var mixed map[string]any
			_ = json.Unmarshal([]byte(lastPatch), &mixed)
			mixed["limitations"] = []string{"retained complete-result field"}
			lastPatch = string(mustBootstrapJSON(t, mixed))
		}
		emitQuestionHandoffFixture(t, w, protocol, n, "", "submit_stage_repair", lastPatch)
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	window, automatic := 272000, 244800
	if paged {
		window, automatic = 150000, 135000
	}
	profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "local review fixture", BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true, ContextWindowTokens: window, AutoCompactTokenLimit: automatic}}, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "local review correction isolated"})
	if err != nil {
		t.Fatal(err)
	}
	allowedTools := []string{workflow.ResearchTaskReadTool}
	reviewVersion := "local-review-fixture-v1"
	if initialSources {
		reviewVersion = "dynamic-independent-review-v9-focused-v1"
	}
	if mode == "initial_progress_then_fence" {
		allowedTools = append(allowedTools, "builtin.resource.open")
	}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: workflow.Definition{
		SchemaVersion: 1, Name: "Local review correction",
		Inputs: []workflow.Port{{Name: "context", Type: workflow.TypeObject, Required: true}, {Name: "research_contract", Type: workflow.TypeObject, Required: true}, {Name: "screening", Type: workflow.TypeObject, Required: true}},
		Nodes: []workflow.Node{
			{ID: "preflight", Name: "Read workspace once", Kind: workflow.NodeTool, ToolName: "builtin.workspace.list", Arguments: json.RawMessage(`{"path":".","limit":10}`)},
			{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: reviewVersion, Prompt: "Independently assess the frozen report.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: allowedTools, OutputSchema: schema},
			{ID: "delivery_gate", Name: "Delivery gate", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{"subject":{}}`)},
		},
		Edges: []workflow.Edge{
			{FromNode: "preflight", FromPort: "structured", ToNode: "independent_review", ToPort: "methodContext"},
			{FromNode: "$input", FromPort: "context", ToNode: "independent_review", ToPort: "context"},
			{FromNode: "$input", FromPort: "research_contract", ToNode: "independent_review", ToPort: "researchContract"},
			{FromNode: "$input", FromPort: "screening", ToNode: "independent_review", ToPort: "evidenceScreening"},
			{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"},
		},
		Outputs: []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "independent_review", FromPort: "analysis", Required: true}},
	}})
	if err != nil {
		var detail *apperr.Error
		if errors.As(err, &detail) {
			t.Fatalf("save route: %v; details=%s", err, detail.Details)
		}
		t.Fatal(err)
	}
	inputs := mustBootstrapJSON(t, map[string]any{"context": map[string]any{"markdown": "Actual energy intake unverified. Limited scope disclosed."}, "research_contract": map[string]any{"successCriteria": []string{"Verify the current intake field"}}, "screening": map[string]any{"evidenceGaps": []any{map[string]any{"field": "Actual energy intake", "required": true, "status": "not_retrieved"}}}})
	if quoteOnly || initialSources {
		var fields map[string]any
		_ = json.Unmarshal(inputs, &fields)
		fields["context"].(map[string]any)["markdown"] = "Actual energy intake unverified. 原誊写文本"
		if initialSources {
			fields["context"].(map[string]any)["builtin.research.task.read"] = "Literal keyed source, not a tool instruction."
		}
		if mode == "issue_initial_sources_read" {
			fields["context"].(map[string]any)["markdown"] = strings.Repeat("Current report background. ", 7000) + "Actual energy intake unverified. 原誊写文本"
		}
		inputs = mustBootstrapJSON(t, fields)
	}
	if selecting || fullNavigation {
		inputs = mustBootstrapJSON(t, map[string]any{"context": map[string]any{"markdown": strings.Repeat("Unrelated report background. ", 200) + "Actual energy intake unverified. Limited scope disclosed. " + strings.Repeat("Unrelated appendix text. ", 200)}, "research_contract": map[string]any{"successCriteria": []string{"Verify the current intake field"}}, "screening": map[string]any{"evidenceGaps": []any{map[string]any{"field": "Actual energy intake", "required": true, "status": "not_retrieved"}}}})
	}
	if mode == "shared_source_cells" {
		inputs = mustBootstrapJSON(t, map[string]any{"context": map[string]any{"markdown": strings.Repeat("0123456789. ", 250) + "Actual energy intake unverified. Limited scope disclosed. " + strings.Repeat("0123456789. ", 400) + "Compliance records are incomplete; adherence evidence is missing. " + strings.Repeat("0123456789. ", 250)}, "research_contract": map[string]any{"successCriteria": []string{"Verify the current intake field"}}, "screening": map[string]any{"evidenceGaps": []any{map[string]any{"field": "Actual energy intake", "required": true, "status": "not_retrieved"}}}})
	}
	if paged {
		var large map[string]any
		_ = json.Unmarshal(inputs, &large)
		large["context"].(map[string]any)["markdown"] = strings.Repeat("Current report background. ", 3000) + "Actual energy intake unverified. Limited scope disclosed."
		large["screening"].(map[string]any)["notes"] = strings.Repeat("Saved supporting analysis. ", 6000)
		inputs = mustBootstrapJSON(t, large)
	}
	readUpstream = func() string {
		runs, err := app.WorkflowFacade.ListRuns(project.ID, saved.Workflow.ID, 2)
		if err != nil || len(runs) != 1 {
			t.Error("cannot capture upstream before review", err, len(runs))
			return ""
		}
		detail, err := app.WorkflowFacade.GetRun(project.ID, runs[0].ID)
		if err != nil || len(detail.Steps) == 0 || detail.Steps[0].Status != workflow.StepCompleted || detail.Steps[0].Attempt != 1 {
			t.Error("upstream was not completed exactly once before review", err)
			return ""
		}
		return string(mustBootstrapJSON(t, detail.Steps[0]))
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: inputs, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 25*time.Second)
	if mode == "issue_oversize_native" {
		// Native arguments are rejected by the HTTP adapter's earlier byte
		// limit. Do not relax that limit or pretend the stage saw this patch.
		if requests.Load() != 2 || final.Run.ErrorCode != "MODEL_TOOL_CALL_INVALID" || final.Steps[1].Status != workflow.StepFailed || final.Steps[2].Attempt != 0 || string(mustBootstrapJSON(t, final.Steps[0])) != upstream {
			t.Fatal("oversize native arguments bypassed adapter boundary or changed upstream", final.Run.ErrorCode, requests.Load())
		}
		execution := final.AIExecutions[len(final.AIExecutions)-1]
		var count int
		var firstDraft string
		if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=1`, execution.ChatRunID).Scan(&firstDraft); err != nil || firstDraft != issueDrafts[1] {
			t.Fatal("adapter rejection changed original audited review", err)
		}
		var failedDraft, failedStatus string
		if err := app.store.DB().QueryRow(`SELECT draft_text,status FROM model_turn_journal WHERE run_id=? AND turn_index=2`, execution.ChatRunID).Scan(&failedDraft, &failedStatus); err != nil || failedDraft != "" || failedStatus != "failed" {
			t.Fatal("adapter rejection published invalid native arguments", failedStatus, err)
		}
		if err := app.store.DB().QueryRow(`SELECT count(*) FROM model_turn_journal WHERE run_id=?`, execution.ChatRunID).Scan(&count); err != nil || count != 2 {
			t.Fatal("adapter rejection renewed correction budget", count, err)
		}
		if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE tool_name='builtin.workspace.list'`).Scan(&count); err != nil || count != 1 {
			t.Fatal("adapter rejection repeated upstream", count, err)
		}
		before := string(mustBootstrapJSON(t, final.Steps))
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		app, err = New(options)
		if err != nil {
			t.Fatal(err)
		}
		app.Startup(context.Background())
		reopened, err := app.WorkflowFacade.GetRun(project.ID, final.Run.ID)
		if err != nil || requests.Load() != 2 || before != string(mustBootstrapJSON(t, reopened.Steps)) {
			t.Fatal("restart changed adapter failure or invoked model", err)
		}
		return
	}
	wantRequests := int32(2)
	if initialSources {
		wantRequests = 1
		if mode == "issue_initial_sources_bad_id" {
			wantRequests = 2
		}
		if mode == "issue_initial_sources_read" {
			wantRequests = 2
		}
	}
	if issueProgress {
		wantRequests = 4
	}
	if issueUnwrapped || issueOversize {
		wantRequests = 3
	}
	if mode == "two_local_steps" || mode == "wrong_hash_then_patch" || mode == "forged_quote_then_patch" || mode == "semantic_then_full" || mode == "semantic_then_fenced" || mode == "mixed_then_patch" || mode == "mixed_source_then_selection" || mode == "progress_then_patch" || mode == "mixed_full_progress" || mode == "issue_wrong_hash" || mode == "issue_missing_hash" || mode == "issue_full_in_repair" || mode == "issue_patch_in_full" {
		wantRequests = 3
	}
	if exhausting {
		if requests.Load() != 3 || final.Run.ErrorCode != "WORKFLOW_AI_CONTENT_REPAIR_EXHAUSTED" || final.Steps[1].Status != workflow.StepFailed || final.Steps[2].Attempt != 0 {
			t.Fatal("invalid patch accepted or correction budget changed", final.Run, requests.Load())
		}
		if initialSources {
			for index := 1; index <= 3; index++ {
				var draft string
				if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=?`, final.AIExecutions[len(final.AIExecutions)-1].ChatRunID, index).Scan(&draft); err != nil || draft != issueDrafts[index] {
					t.Fatal("original rejected selectors were not retained", index, err)
				}
			}
			issueDrafts = map[int]string{}
		}
		before := string(mustBootstrapJSON(t, final.Steps))
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		app, err = New(options)
		if err != nil {
			t.Fatal(err)
		}
		app.Startup(context.Background())
		reopened, err := app.WorkflowFacade.GetRun(project.ID, final.Run.ID)
		if err != nil || requests.Load() != 3 || before != string(mustBootstrapJSON(t, reopened.Steps)) {
			t.Fatal("restart changed failed stage or invoked model", err)
		}
		retrying.Store(true)
		if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: final.Run.ID, StepID: final.Steps[1].ID}); err != nil {
			t.Fatal(err)
		}
		final = waitForWorkflowState(t, app, project.ID, final.Run.ID, workflow.RunFailed, 25*time.Second)
		wantRequests = 4
	}
	wantTerminal := "RESEARCH_REVIEW_NOT_APPROVED"
	if legacyFullMetadata {
		// The custom AI schema permits this metadata, but the unchanged stock
		// gate does not. Prove host acceptance/binding without weakening it or
		// claiming the custom extension is a scientific delivery approval.
		wantTerminal = "WORKFLOW_TOOL_PROPOSAL_FAILED"
	}
	if requests.Load() != wantRequests || final.Run.ErrorCode != wantTerminal || final.Steps[1].Status != workflow.StepCompleted || string(mustBootstrapJSON(t, final.Steps[0])) != upstream {
		var details string
		if len(final.AIExecutions) > 0 {
			_ = app.store.DB().QueryRow(`SELECT error_details FROM runs WHERE id=?`, final.AIExecutions[len(final.AIExecutions)-1].ChatRunID).Scan(&details)
		}
		t.Fatalf("review invalid, decision changed, repeated stage or upstream changed: code=%s message=%s requests=%d details=%.4000s", final.Run.ErrorCode, final.Run.ErrorMessage, requests.Load(), details)
	}
	execution := final.AIExecutions[len(final.AIExecutions)-1]
	var output map[string]any
	if json.Unmarshal(execution.Output, &output) != nil || output["approved"] != false {
		t.Fatal("scientific rejection not retained", string(execution.Output))
	}
	if issueProgress {
		findings := output["reviewFindings"].([]any)
		if len(findings) != 3 {
			t.Fatal("sparse corrections dropped findings", len(findings))
		}
		for _, raw := range findings {
			if raw.(map[string]any)["changeReason"] != "First detection from the frozen evidence gap" {
				t.Fatal("earlier correction lost from merged review", raw)
			}
		}
	}
	finding := output["reviewFindings"].([]any)[0].(map[string]any)
	wantPointer := "/evidenceScreening/evidenceGaps/0/field"
	if currentSource {
		wantPointer = "/context/markdown"
	}
	if finding["basis"].([]any)[0].(map[string]any)["pointer"] != wantPointer || finding["changeReason"] != "First detection from the frozen evidence gap" {
		t.Fatal("model's corrected cells lost", finding)
	}
	var journal string
	if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=?`, execution.ChatRunID, execution.ModelTurns).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if mode != "full_fallback" && mode != "semantic_then_full" && mode != "semantic_then_fenced" && mode != "initial_progress_then_fence" && !exhausting && !fullNavigation && journal != lastPatch {
		t.Fatal("raw patch journal replaced by merged result", journal)
	}
	if issueMode {
		rows, err := app.store.DB().Query(`SELECT turn_index,draft_text FROM model_turn_journal WHERE run_id=? ORDER BY turn_index`, execution.ChatRunID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			var index int
			var raw string
			if err := rows.Scan(&index, &raw); err != nil || raw != issueDrafts[index] {
				_ = rows.Close()
				t.Fatalf("earlier original submission/patch was rewritten or lost: turn=%d error=%v expectedPrefix=%.90q gotPrefix=%.90q", index, err, issueDrafts[index], raw)
			}
			count++
		}
		err = rows.Err()
		_ = rows.Close()
		wantTurns := int(wantRequests)
		if initialSources && exhausting {
			wantTurns = 1
		}
		if err != nil || count != len(issueDrafts) || count != wantTurns {
			t.Fatal("incomplete raw issue submission audit", count, len(issueDrafts), err)
		}
	}
	var persisted string
	if err := app.store.DB().QueryRow(`SELECT text_content FROM message_parts WHERE message_id=(SELECT assistant_message_id FROM runs WHERE id=?) AND part_type='text' ORDER BY ordinal LIMIT 1`, execution.ChatRunID).Scan(&persisted); err != nil || strings.Contains(persisted, "reviewFindingsPatch") || !strings.Contains(persisted, "reviewFindings") {
		t.Fatal("partial patch leaked as a completed review", persisted, err)
	}
	if issueMode {
		expected := mustBootstrapJSON(t, lastFullReview)
		if len(expectedCompleteReview) > 0 {
			expected = expectedCompleteReview
		}
		want, _, err := workflow.NormalizeAIStageSubmission(string(expected), schema)
		if err != nil {
			t.Fatal(err)
		}
		messageProjection := persisted
		if legacyFullMetadata {
			// The assistant keeps the ORIGINAL legacy body without the host
			// identity. Bind only this readback comparison, never the DB/text.
			decoded, _, err := workflow.NormalizeAIStageSubmission(persisted, json.RawMessage(`{"type":"object"}`))
			if err != nil {
				t.Fatal(err)
			}
			var messageFields, expectedFields map[string]json.RawMessage
			_ = json.Unmarshal(decoded, &messageFields)
			_ = json.Unmarshal(expected, &expectedFields)
			if supplied, ok := messageFields["reviewedInputSha256"]; ok && string(supplied) != string(expectedFields["reviewedInputSha256"]) {
				t.Fatal("raw message supplied a different host identity")
			}
			messageFields["reviewedInputSha256"] = expectedFields["reviewedInputSha256"]
			messageProjection = string(mustBootstrapJSON(t, messageFields))
		}
		got, _, err := workflow.NormalizeAIStageSubmission(messageProjection, schema)
		if err != nil || string(got) != string(want) || string(execution.Output) != string(want) {
			t.Fatal("completed message or workflow output differs from the exact merged full review", err)
		}
	}
	digest := sha256.Sum256([]byte(persisted))
	t.Logf("HTTP requests=%d; raw correction bytes=%d; merged result sha256=%s; upstream unchanged", requests.Load(), len(journal), hex.EncodeToString(digest[:]))
	var readCalls int
	wantReads := 0
	if mode == "issue_initial_sources_read" {
		wantReads = 1
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE tool_name=?`, workflow.ResearchTaskReadTool).Scan(&readCalls); err != nil || readCalls != wantReads {
		t.Fatal("local correction reread supplied input", readCalls, err)
	}
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM tool_calls WHERE tool_name='builtin.workspace.list'`).Scan(&readCalls); err != nil || readCalls != 1 || upstream == "" {
		t.Fatal("corrections repeated upstream execution or lacked a pre-review snapshot", readCalls, err)
	}
	before := string(mustBootstrapJSON(t, final.Steps))
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reopened, err := app.WorkflowFacade.GetRun(project.ID, final.Run.ID)
	if err != nil || requests.Load() != wantRequests || before != string(mustBootstrapJSON(t, reopened.Steps)) {
		t.Fatal("restart changed persisted review or invoked model", err)
	}
}
