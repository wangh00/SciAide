package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/artifact"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Isolated SQLite + controlled HTTP model. Exercises an actual resource call,
// fresh evidence input, formal selection, independent review, publication,
// exports and restart, without using the user's database, models or tasks.
func TestIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, false, false, false, false)
}

func TestIndexedEvidenceHTTPOverlappingSearchReadSelectionPublicationAndRestart(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, false, false, false)
}

func TestIndexedEvidenceHTTPMultipleReadRoundsAndNoProgressReread(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, true, false, false)
}

func TestIndexedEvidenceHTTPSameChatReadSynthesisSelectionPublicationAndRestart(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, false, true, false)
}

func TestIndexedEvidenceHTTPManualSameChatSynthesisPreservesSelectionAndPublication(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, false, true, true)
}

func TestRejectedEvidenceDirectReselectionPreservesSynthesis(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, false, true, false, "direct")
}

func TestLegacyRejectedEvidenceDirectReselectionPreservesReview(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, false, true, false, "legacy-direct")
}

func TestRejectedEvidenceDiscussionConfirmationPreservesSynthesis(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, false, true, false, "discussion")
}

func TestIndependentReviewerCanReadAdoptedSourceWithoutReopeningSynthesis(t *testing.T) {
	testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t, true, false, true, false, "focused-read")
}

type indexedOverlapConnector struct{ researchClosureConnector }

func (indexedOverlapConnector) Search(ctx context.Context, options research.SearchOptions) ([]research.Work, error) {
	works, err := (researchClosureConnector{}).Search(ctx, options)
	for i := range works {
		works[i].Abstract = strings.Repeat("The verified fixture finding is epsilon forty two. This statement is synthetic fixture evidence only. ", 45)
	}
	return works, err
}

func (indexedOverlapConnector) Fetch(ctx context.Context, id string) (research.Work, error) {
	w, err := (researchClosureConnector{}).Fetch(ctx, id)
	w.Abstract = strings.Repeat("The verified fixture finding is epsilon forty two. This statement is synthetic fixture evidence only. ", 45)
	return w, err
}

func testIndexedEvidenceHTTPCheckpointSelectionPublicationAndRestart(t *testing.T, overlap, multipleRounds, sameChat, manual bool, repairModes ...string) {
	repair := ""
	if len(repairModes) > 0 {
		repair = repairModes[0]
	}
	focusedRead := repair == "focused-read"
	if focusedRead {
		repair = ""
	}
	var reads, screenings, reviews, reviewReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		var discussion struct {
			Messages []struct{ Role string }
			Tools    []struct{ Function struct{ Name string } }
		}
		_ = json.Unmarshal(body, &discussion)
		for _, exposed := range discussion.Tools {
			if exposed.Function.Name != "research_revision_propose" {
				continue
			}
			lastTool := len(discussion.Messages) > 0 && discussion.Messages[len(discussion.Messages)-1].Role == "tool"
			delta := map[string]any{"content": "方案尚未执行，请确认引用补选卡片。"}
			finish := "stop"
			if !lastTool {
				args := mustBootstrapJSON(t, workflow.ProposeResearchRevisionCommand{NodeID: "evidence_extraction", Summary: "Adopt existing evidence without resynthesis", Changes: []string{"Recheck all requested comparisons", "Keep valid prior findings"}, Reason: "Existing accepted evidence needs explicit selection"})
				delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "reselection-proposal", "function": map[string]any{"name": "research_revision_propose", "arguments": string(args)}}}}
				finish = "tool_calls"
			}
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		prompt := workflowAITestPrompt(body)
		input := workflowAITestStageInput(prompt)
		schema, err := workflowAITestSchema(prompt)
		if err != nil {
			t.Error(err)
			http.Error(w, "schema", 400)
			return
		}
		properties, _ := schema["properties"].(map[string]any)
		_, screen := properties["recommendedReferences"]
		var req struct {
			Messages []struct{ Role, Content string }
			Tools    []struct {
				Function struct{ Name, Description string }
			}
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
			http.Error(w, "request", 400)
			return
		}
		// Menus are scoped to the current Chat Run. Match issued action IDs,
		// not the shared conversation's older tool results or seed messages.
		currentActions := map[string]bool{}
		if start := strings.LastIndex(prompt, "<resource_actions>\n"); start >= 0 {
			value := prompt[start+len("<resource_actions>\n"):]
			if end := strings.Index(value, "\n</resource_actions>"); end >= 0 {
				var menu struct{ Actions []resource.Option }
				if json.Unmarshal([]byte(value[:end]), &menu) == nil {
					for _, a := range menu.Actions {
						currentActions[a.ID] = true
					}
				}
			}
		}
		currentTools := []string{}
		for _, m := range req.Messages {
			if m.Role != "tool" {
				continue
			}
			value := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(m.Content, "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
			var env struct {
				Structured struct {
					ID      string            `json:"actionId"`
					Actions []resource.Option `json:"actions"`
				} `json:"structured"`
			}
			if json.Unmarshal([]byte(value), &env) != nil || !currentActions[env.Structured.ID] {
				continue
			}
			currentTools = append(currentTools, m.Content)
			for _, a := range env.Structured.Actions {
				currentActions[a.ID] = true
			}
		}
		readAction := screen && lenSlice(input["indexedReadRanges"]) == 0 && reads.Load() == 0
		continuePage := false
		if screen && multipleRounds && lenSlice(input["indexedReadRanges"]) > 0 {
			// Round two rereads the first page, then follows a real issued
			// continuation to new blocks. Round three rereads only an offered
			// page: it must finish without scheduling another synthesis.
			readAction = len(currentTools) == 0 || (reads.Load() == 2 && len(currentTools) == 1)
			continuePage = reads.Load() == 2 && len(currentTools) == 1
		}
		_, reviewing := properties["reviewedInputSha256"]
		if focusedRead && reviewing && len(currentTools) == 0 {
			readAction = true
		}
		if screen && lenSlice(input["indexedReadRanges"]) == 0 && !strings.Contains(prompt, "核读可引用原文 ·") {
			t.Error("new evidence route has no selected-source resource actions")
			http.Error(w, "no resources", 400)
			return
		}
		if readAction {
			name := ""
			for _, d := range req.Tools {
				if strings.Contains(d.Function.Description, "Operate on a host-issued resource action") {
					name = d.Function.Name
				}
			}
			start := strings.LastIndex(prompt, "<resource_actions>\n")
			if start < 0 {
				t.Error("missing resources")
				return
			}
			value := prompt[start+len("<resource_actions>\n"):]
			end := strings.Index(value, "\n</resource_actions>")
			var menu struct{ Actions []resource.Option }
			if end < 0 || json.Unmarshal([]byte(value[:end]), &menu) != nil {
				t.Error("resource menu")
				return
			}
			action := ""
			for _, a := range menu.Actions {
				if a.Kind == "document_evidence" {
					action = a.ID
					break
				}
			}
			if continuePage {
				action = ""
				value := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(currentTools[0], "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
				var env struct {
					Structured struct {
						Actions []resource.Option `json:"actions"`
					} `json:"structured"`
				}
				if err := json.Unmarshal([]byte(value), &env); err != nil {
					t.Error("read continuation envelope", err)
					return
				}
				for _, a := range env.Structured.Actions {
					if a.Kind == "document_evidence" && strings.Contains(a.Label, "继续核读") {
						action = a.ID
						break
					}
				}
			}
			if name == "" || action == "" {
				t.Error("missing actual read action")
				return
			}
			readNumber := reads.Add(1)
			if reviewing {
				reviewReads.Add(1)
				reads.Add(-1)
				readNumber = 100
			}
			args, _ := json.Marshal(map[string]string{"actionId": action})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("indexed-read-%d", readNumber), "function": map[string]any{"name": name, "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		out := workflowAISchemaFixture(schema).(map[string]any)
		if screen {
			screenings.Add(1)
			candidates, _ := input["candidates"].([]any)
			// The collection draft is intentionally only about current source
			// candidates; it must never be published as the accepted result.
			collectionDraft := false
			if !sameChat && (lenSlice(input["indexedReadRanges"]) == 0 || (multipleRounds && reads.Load() == 3)) {
				collectionDraft = true
				// A real model can mention the freshly read marker in its draft.
				// Exercise terminal Chat citation persistence, not only the later
				// Workflow seed and report publication paths.
				readRef := ""
				for _, content := range currentTools {
					start := strings.Index(content, "[K-")
					if start >= 0 && start+16 <= len(content) {
						readRef = content[start : start+16]
					}
				}
				if readRef == "" {
					t.Error("missing actual read citation in tool response")
					http.Error(w, "read citation", 400)
					return
				}
				out["summary"] = "Source collection draft; not final findings " + readRef
				out["recommendedReferences"] = []string{}
				out["citationAssessments"] = []any{}
			} else {
				var c map[string]any
				for _, v := range candidates {
					x := v.(map[string]any)
					if (!overlap && strings.Contains(x["quote"].(string), "## Abstract")) || (overlap && !strings.Contains(x["quote"].(string), "## Bibliographic metadata") && strings.Contains(x["quote"].(string), "The verified fixture finding is epsilon forty two.")) {
						c = x
						break
					}
				}
				if sameChat {
					for _, content := range currentTools {
						var env struct {
							Text string `json:"text"`
						}
						value := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(content, "<untrusted_tool_result>\n"), "\n</untrusted_tool_result>"))
						if json.Unmarshal([]byte(value), &env) != nil {
							continue
						}
						for _, paragraph := range strings.Split(env.Text, "\n\n") {
							if strings.HasPrefix(paragraph, "[K-") && strings.Contains(paragraph, "The verified fixture finding is epsilon forty two.") {
								c = map[string]any{"reference": paragraph[:16]}
							}
						}
					}
				}
				if c == nil {
					t.Error("fresh candidates omitted actual indexed source")
					return
				}
				ref := c["reference"].(string)
				out["summary"] = "Verified fixture finding " + ref
				out["recommendedReferences"] = []string{ref}
				out["citationAssessments"] = []any{map[string]any{"reference": ref, "decision": "support", "sourceLevel": "abstract", "reason": "Fixture source confirms observation", "supportingQuote": "The verified fixture finding is epsilon forty two."}}
			}
			out["recommendation"] = "proceed_limited"
			if collectionDraft {
				out["recommendation"] = "proceed"
			} // Deliberately incomplete: no supporting citations.
			out["coverage"] = map[string]any{"sufficientForClaimedScope": true} // Only the synthetic abstract observation is in scope.
			analyses := []any{}
			for _, id := range input["documentIds"].([]any) {
				analyses = append(analyses, map[string]any{"documentId": id, "finding": "Fixture observation", "limitations": "Synthetic metadata document, not full scientific evidence", "references": []string{}})
			}
			out["documentAnalyses"] = analyses
		}
		if _, draft := properties["markdown"]; draft {
			ref := workflowAIReissuedCitation(prompt)
			out["markdown"] = "# Fixture comparison\n\nThe verified fixture finding is epsilon forty two. " + ref
			out["claimSummary"] = []string{"Fixture observation only"}
			out["limitations"] = []string{"Synthetic fixture; not a real research result"}
			out["confidence"] = "low"
		}
		if _, review := properties["reviewedInputSha256"]; review {
			out["approved"] = true
			out["reviewedInputSha256"] = workflowAITestStageInputSHA256(prompt)
			if repair != "" && reviews.Add(1) == 1 {
				out["approved"] = false
				out["requiredCorrections"] = []string{"Confirm adoption of the existing accepted evidence"}
				if _, ok := properties["revisionPlan"]; ok {
					out["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "evidence_extraction", "reason": "Evidence adoption requires selection", "whyNotLater": "Report cannot adopt evidence itself", "needsUserInput": false, "requiredInput": ""}}
					if repair == "legacy-direct" {
						out["revisionPlan"] = []any{map[string]any{"correctionIndex": 0, "nodeId": "evidence_screening", "reason": "Existing evidence needs citation adoption", "whyNotLater": "Report cannot use unselected evidence", "needsUserInput": true, "requiredInput": "Confirm adoption of existing accepted evidence"}}
					}
				}
			}
		}
		encoded, _ := json.Marshal(out)
		if sameChat {
			// Real models return whitespace/HTML-sensitive JSON. Preserve its
			// exact execution bytes while the containing step compacts it.
			encoded, _ = json.MarshalIndent(out, "", "  ")
		}
		answer := "```json\n" + string(encoded) + "\n```"
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	connector := research.Connector(researchClosureConnector{})
	if overlap {
		connector = indexedOverlapConnector{}
	}
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: []research.Connector{connector}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Indexed evidence isolated fixture"})
	if err != nil {
		t.Fatal(err)
	}
	base := referenceWorkflowTemplate(t, app, "trusted-research-closure").Definition
	definition := base
	definition.Name = "Indexed evidence source-order fixture"
	renamed := map[string]string{"import": "evidence_import", "sync": "evidence_sync", "knowledge": "evidence_search", "select_citations": "evidence_extraction", "draft_report": "report_drafting", "review": "independent_review", "review_gate": "delivery_gate"}
	nodes := []workflow.Node{}
	keep := map[string]bool{"$input": true}
	for _, n := range base.Nodes {
		if n.ID == "environment" || n.ID == "analysis" || n.ID == "interpret" {
			continue
		}
		if id, ok := renamed[n.ID]; ok {
			n.ID = id
		}
		if n.ID == "independent_review" && focusedRead {
			n.Kind = workflow.NodeAgentStage
			n.AllowedTools = []string{"builtin.resource.open", "builtin.attachment.list", "builtin.document.inspect", "builtin.knowledge.read"}
		}
		if n.ID == "evidence_search" {
			n.Arguments = json.RawMessage(`{"perDocument":true,"limit":20}`)
		}
		keep[n.ID] = true
		nodes = append(nodes, n)
	}
	screenSchema := json.RawMessage(`{"type":"object","required":["summary","recommendedReferences","citationAssessments","recommendation","coverage"],"properties":{"summary":{"type":"string"},"recommendedReferences":{"type":"array","items":{"type":"string"}},"citationAssessments":{"type":"array","items":{"type":"object"}},"recommendation":{"type":"string"},"coverage":{"type":"object"},"documentAnalyses":{"type":"array","items":{"type":"object"}}}}`)
	nodes = append(nodes, workflow.Node{ID: "evidence_screening", Name: "Read and synthesize selected evidence", Kind: workflow.NodeAgentStage, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "selected-evidence-v6", Prompt: "Read related sources only when needed.", OutputSchema: screenSchema, AllowedTools: []string{"builtin.resource.open", "builtin.attachment.list", "builtin.document.inspect", "builtin.knowledge.read"}})
	if manual {
		nodes[len(nodes)-1].ReviewPolicy = workflow.AIReviewHuman
	}
	keep["evidence_screening"] = true
	definition.Nodes = nodes
	definition.Edges = nil
	for _, edge := range base.Edges {
		if id, ok := renamed[edge.FromNode]; ok {
			edge.FromNode = id
		}
		if id, ok := renamed[edge.ToNode]; ok {
			edge.ToNode = id
		}
		if keep[edge.FromNode] && keep[edge.ToNode] {
			definition.Edges = append(definition.Edges, edge)
		}
	}
	definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "evidence_search", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"}, workflow.Edge{FromNode: "evidence_sync", FromPort: "structured.documentIds", ToNode: "evidence_screening", ToPort: "documentIds"}, workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "evidence_extraction", ToPort: "screening"}, workflow.Edge{FromNode: "evidence_extraction", FromPort: "citations", ToNode: "report_drafting", ToPort: "context"})
	definition.Outputs = []workflow.Output{{Name: "report", Type: workflow.TypeObject, FromNode: "report", FromPort: "structured", Required: true}}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"epsilon forty two"}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	waiting := driveToCandidateSelection(t, app, p.ID, started.Run.ID, 15*time.Second)
	step := currentWorkflowStep(t, waiting)
	var selected struct {
		Candidates []struct {
			ID string `json:"id"`
		}
	}
	_ = json.Unmarshal(step.Input, &selected)
	if len(selected.Candidates) != 1 {
		t.Fatal("candidate fixture inventory")
	}
	selection, _ := json.Marshal(map[string]any{"selectedCandidateIds": []string{selected.Candidates[0].ID}})
	if _, err = app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: step.ID, Approved: true, Context: selection}); err != nil {
		t.Fatal(err)
	}
	evidence := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 15*time.Second)
	evidenceStep := currentWorkflowStep(t, evidence)
	if manual {
		if evidenceStep.NodeID != "evidence_screening" {
			t.Fatal("missing manual synthesis review")
		}
		if _, err = app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: evidenceStep.ID, Approved: true}); err != nil {
			t.Fatal("manual same-Chat evidence was lost", err)
		}
		evidence = waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 15*time.Second)
		evidenceStep = currentWorkflowStep(t, evidence)
	}
	var offered struct {
		Candidates []tool.CitationRef `json:"candidates"`
		Screening  struct {
			References []string `json:"recommendedReferences"`
		} `json:"screening"`
	}
	if err := json.Unmarshal(evidenceStep.Input, &offered); err != nil || evidenceStep.NodeKind != workflow.NodeCitationSelection {
		t.Fatal("missing formal evidence selection", err)
	}
	var chosen []tool.CitationRef
	for _, c := range offered.Candidates {
		if (!overlap && strings.Contains(c.Quote, "## Abstract")) || (overlap && len(offered.Screening.References) == 1 && c.Reference == offered.Screening.References[0]) {
			chosen = append(chosen, c)
		}
	}
	if len(chosen) != 1 {
		t.Fatal("indexed read not selectable", len(chosen))
	}
	if overlap {
		// No hand-built citation: both snapshots came from actual registered
		// tools and the selected marker must be present in the initial search.
		var search struct {
			Citations []tool.CitationRef `json:"citations"`
		}
		for _, s := range evidence.Steps {
			if s.NodeID == "evidence_search" {
				if err := json.Unmarshal(s.Output, &search); err != nil {
					t.Fatal(err)
				}
			}
		}
		matched := false
		for _, c := range search.Citations {
			if c.Reference == chosen[0].Reference {
				a, _ := json.Marshal(c)
				b, _ := json.Marshal(chosen[0])
				matched = string(a) == string(b)
			}
		}
		if !matched || chosen[0].ID != chosen[0].AttachmentID {
			t.Fatal("fixture did not exercise overlapping complete snapshots", chosen[0].Reference)
		}
	}
	if _, err = app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: evidenceStep.ID, Approved: true, AcceptLimitedEvidence: true, Context: mustBootstrapJSON(t, chosen)}); err != nil {
		t.Fatal(err)
	}
	if repair != "" {
		rejected := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunFailed, 20*time.Second)
		if !rejected.DiscussionRevisionAvailable || currentWorkflowStep(t, rejected).ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" {
			t.Fatal("verified rejection not repairable", rejected.Run.ErrorMessage)
		}
		before := workflow.ResearchRevisionSnapshot(rejected)
		var screening workflow.Step
		for _, s := range rejected.Steps {
			if s.NodeID == "evidence_screening" {
				screening = s
			}
		}
		if repair == "direct" || repair == "legacy-direct" {
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: currentWorkflowStep(t, rejected).ID, RevisionNodeID: "evidence_extraction", ExpectedReviewSHA256: "stale"}); err == nil {
				t.Fatal("stale selection retry accepted")
			}
			var reviewHash string
			for _, s := range rejected.Steps {
				if s.NodeID == "independent_review" {
					var out struct {
						Analysis json.RawMessage `json:"analysis"`
					}
					_ = json.Unmarshal(s.Output, &out)
					h := sha256.Sum256(out.Analysis)
					reviewHash = hex.EncodeToString(h[:])
				}
			}
			useRecommendation := repair == "direct"
			if rejected.ReviewRevisionRecommendation == nil {
				t.Fatal("missing review state")
			}
			if useRecommendation {
				if rejected.ReviewRevisionRecommendation.Status != "recommended" || rejected.ReviewRevisionRecommendation.NodeID != "evidence_extraction" {
					t.Fatal("ordinary citation confirmation blocked the direct recommendation", rejected.ReviewRevisionRecommendation)
				}
			} else if rejected.ReviewRevisionRecommendation.Status != "needs_input" || rejected.ReviewRevisionRecommendation.NodeID != "" {
				t.Fatal("legacy scientific decision silently rewritten", rejected.ReviewRevisionRecommendation)
			}
			if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: currentWorkflowStep(t, rejected).ID, RevisionNodeID: "evidence_extraction", UseRecommendation: useRecommendation, ExpectedReviewSHA256: reviewHash}); err != nil {
				t.Fatal(err)
			}
			var discussionRuns int
			if err := app.store.DB().QueryRow(`SELECT count(*) FROM research_revision_proposals WHERE workflow_run_id=?`, started.Run.ID).Scan(&discussionRuns); err != nil || discussionRuns != 0 {
				t.Fatal("direct review repair created a second proposal", discussionRuns, err)
			}
		} else {
			if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: started.Run.ID, ProposalID: "fake", Confirmed: true}); err == nil {
				t.Fatal("invented proposal accepted")
			}
			cr, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: rejected.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: "Please prepare a full repair plan, do not execute yet"})
			if err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(12 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				snapshot, err := app.ChatFacade.GetRunSnapshot(cr.ID)
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Run.Status == chat.RunCompleted {
					for _, c := range snapshot.ToolCalls {
						if c.Status != tool.CallCompleted {
							t.Fatal("proposal call failed", c.ErrorMessage)
						}
					}
					break
				}
				if snapshot.Run.Status == chat.RunFailed || time.Now().After(deadline) {
					t.Fatal("proposal discussion failed", snapshot.Run.ErrorMessage)
				}
			}
			pending, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if workflow.ResearchRevisionSnapshot(pending) != before || len(pending.RevisionProposals) != 1 || !pending.RevisionProposals[0].CanConfirm {
				t.Fatal("proposal mutated workflow or cannot confirm")
			}
			proposal := pending.RevisionProposals[0]
			if proposal.NodeID != "evidence_extraction" || len(proposal.AffectedStages) != 5 {
				t.Fatal("incorrect selection replay scope", proposal)
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(options)
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			restored, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
			if err != nil || len(restored.RevisionProposals) != 1 || !restored.RevisionProposals[0].CanConfirm || workflow.ResearchRevisionSnapshot(restored) != before {
				t.Fatal("rejected pending proposal lost after restart", err)
			}
			if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: "wrong-project", RunID: started.Run.ID, ProposalID: proposal.ID, Confirmed: true}); err == nil {
				t.Fatal("wrong project accepted")
			}
			if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: started.Run.ID, ProposalID: proposal.ID, Confirmed: true}); err != nil {
				t.Fatal(err)
			}
		}
		fresh := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 15*time.Second)
		if repair == "legacy-direct" {
			found := false
			for _, event := range fresh.Events {
				if event.Type != "workflow.review_revision_queued" {
					continue
				}
				var queued struct {
					Review             json.RawMessage `json:"independentReview"`
					UsedRecommendation bool            `json:"usedRecommendation"`
				}
				if json.Unmarshal(event.Payload, &queued) != nil || queued.UsedRecommendation || !strings.Contains(string(queued.Review), "revisionPlan") || !strings.Contains(string(queued.Review), "Confirm adoption of existing accepted evidence") {
					t.Fatal("legacy direct retry dropped/reinterpreted full review", string(event.Payload))
				}
				found = true
			}
			if !found {
				t.Fatal("legacy repair lacks immutable review context")
			}
		}
		selection := currentWorkflowStep(t, fresh)
		var input struct {
			Candidates []tool.CitationRef `json:"candidates"`
			Previous   []string           `json:"previousSelectedReferences"`
			Repair     struct {
				Hash        string   `json:"reviewOutputSha256"`
				Corrections []string `json:"requiredCorrections"`
			} `json:"citationRepairContext"`
		}
		if json.Unmarshal(selection.Input, &input) != nil || selection.NodeID != "evidence_extraction" || selection.Attempt != evidenceStep.Attempt+1 || len(input.Candidates) != len(offered.Candidates) || len(input.Previous) != 1 || input.Previous[0] != chosen[0].Reference {
			t.Fatal("fresh selection lost prior choices")
		}
		if repair == "direct" || repair == "legacy-direct" {
			if input.Repair.Hash == "" || len(input.Repair.Corrections) == 0 {
				t.Fatal("direct review repair checkpoint lost its already confirmed requirements")
			}
		} else if input.Repair.Hash != "" {
			t.Fatal("user-negotiated revision borrowed stale scientific review requirements")
		}
		for _, s := range fresh.Steps {
			if s.NodeID == "evidence_screening" && (s.Attempt != screening.Attempt || string(s.Input) != string(screening.Input) || string(s.Output) != string(screening.Output)) {
				t.Fatal("selection reran/mutated screening")
			}
		}
		if _, err := app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: selection.ID, Approved: true, AcceptLimitedEvidence: true, Context: mustBootstrapJSON(t, chosen)}); err != nil {
			t.Fatal(err)
		}
		var decisions int
		if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_human_decisions WHERE workflow_step_id=? AND approved=1`, selection.ID).Scan(&decisions); err != nil || decisions != 2 {
			t.Fatal("historical selections lost", decisions, err)
		}
	}
	finished := driveResearchClosure(t, app, p.ID, started.Run.ID, 40*time.Second)
	expectedReads, expectedScreenings, expectedEvents := int32(1), int32(2), 1
	if multipleRounds {
		expectedReads, expectedScreenings, expectedEvents = 4, 3, 2
	}
	if sameChat {
		expectedReads, expectedScreenings, expectedEvents = 1, 1, 0
	}
	if reads.Load() != expectedReads || screenings.Load() != expectedScreenings {
		for _, ev := range finished.Events {
			if strings.Contains(ev.Type, "evidence") {
				t.Logf("event %s %s", ev.Type, ev.Payload)
			}
		}
		for _, e := range finished.AIExecutions {
			t.Logf("execution attempt=%d status=%s error=%s text=%s", e.Attempt, e.Status, e.ErrorMessage, e.OutputText)
		}
		t.Fatalf("source read/syntheses=%d/%d", reads.Load(), screenings.Load())
	}
	if focusedRead && reviewReads.Load() != 1 {
		t.Fatal("review did not execute an adopted-source read", reviewReads.Load())
	}
	var screenStep workflow.Step
	events := 0
	for _, s := range finished.Steps {
		if s.NodeID == "evidence_screening" {
			screenStep = s
		} else {
			want := 1
			if repair != "" && (s.NodeID == "evidence_extraction" || s.NodeID == "report_drafting" || s.NodeID == "independent_review" || s.NodeID == "delivery_gate") {
				want = 2
			}
			if s.Attempt != want {
				t.Fatalf("unexpected replay: %s attempt %d want %d", s.NodeID, s.Attempt, want)
			}
		}
	}
	for _, e := range finished.Events {
		if e.Type == "workflow.indexed_evidence_read" {
			events++
		}
	}
	if events != expectedEvents || screenStep.Attempt != int(expectedScreenings) || (!sameChat && !strings.Contains(string(screenStep.Input), "indexedReadRanges")) || (sameChat && !strings.Contains(string(screenStep.Output), "evidenceCandidates")) || strings.Contains(string(screenStep.Output), "Source collection draft") {
		t.Fatal("source draft or continuation was not isolated")
	}
	if sameChat {
		for _, step := range finished.Steps {
			if step.NodeID != "report_drafting" && step.NodeID != "independent_review" {
				continue
			}
			var input struct {
				Sources struct {
					Ranges []map[string]any `json:"indexedReadRanges"`
				} `json:"researchSourceContext"`
			}
			if err := json.Unmarshal(step.Input, &input); err != nil || len(input.Sources.Ranges) == 0 {
				t.Fatal("pretty synthesis lost observed ranges downstream", step.NodeID, err)
			}
		}
	}
	var sourceCall, chatID string
	if err = app.store.DB().QueryRow(`SELECT tc.id,tc.run_id FROM tool_calls tc JOIN tool_results tr ON tr.tool_call_id=tc.id WHERE tc.tool_name='builtin.resource.open' AND tc.status='completed' AND tr.structured_json LIKE '%builtin.knowledge.read%' ORDER BY tc.created_at,tc.id LIMIT 1`).Scan(&sourceCall, &chatID); err != nil {
		t.Fatal(err)
	}
	var storedReadCitations int
	if err = app.store.DB().QueryRow(`SELECT count(*) FROM message_citations c JOIN runs r ON r.id=c.run_id JOIN messages m ON m.id=c.message_id WHERE c.tool_call_id=? AND r.status='completed' AND m.status='complete'`, sourceCall).Scan(&storedReadCitations); err != nil || storedReadCitations != 1 {
		t.Fatal("read citation did not survive terminal Chat save", storedReadCitations, err)
	}
	// When a search and read returned the identical complete snapshot, either
	// actual successful source may back publication. Do not require publication
	// to discard the search provenance just because the chunk was also reread.
	verifiedSources := map[string]string{sourceCall: chatID}
	if overlap {
		rows, err := app.store.DB().Query(`SELECT tc.id,tr.citations_json FROM tool_calls tc JOIN tool_results tr ON tr.tool_call_id=tc.id WHERE tc.workflow_run_id=? AND tc.tool_name='builtin.knowledge.search' AND tc.status='completed' AND tr.status='success'`, finished.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, encoded string
			if err := rows.Scan(&id, &encoded); err != nil {
				t.Fatal(err)
			}
			var refs []tool.CitationRef
			if err := json.Unmarshal([]byte(encoded), &refs); err != nil {
				t.Fatal(err)
			}
			for _, ref := range refs {
				if string(mustBootstrapJSON(t, ref)) == string(mustBootstrapJSON(t, chosen[0])) {
					verifiedSources[id] = finished.Run.ID
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		if len(verifiedSources) < 2 {
			t.Fatal("overlapping snapshot has no actual successful workflow search source")
		}
	}
	files, err := app.ArtifactFacade.ListTaskArtifacts(p.ID, finished.Run.ResearchTaskID, false)
	if err != nil {
		t.Fatal(err)
	}
	var reportID string
	storedReportCitations := map[string]string{}
	for _, file := range files {
		if file.Kind != artifact.KindDocument {
			continue
		}
		detail, err := app.ArtifactFacade.GetTaskArtifact(p.ID, finished.Run.ResearchTaskID, file.ID)
		if err != nil {
			t.Fatal(err)
		}
		reportID = file.ID
		if len(detail.Versions) == 0 {
			t.Fatal("published report has no immutable version")
		}
		for _, version := range detail.Versions {
			if len(version.Citations) != 1 {
				t.Fatal("published report lost selected citation")
			}
			c := version.Citations[0]
			if runID, ok := verifiedSources[c.SourceToolCallIDSnapshot]; !ok || c.SourceRunIDSnapshot != runID {
				t.Fatalf("published report lost verified source provenance: call=%s run=%s", c.SourceToolCallIDSnapshot, c.SourceRunIDSnapshot)
			}
			ref := chosen[0]
			if c.Reference != ref.Reference || c.IndexVersionID != ref.IndexVersionID || c.DocumentID != ref.DocumentID || c.AttachmentID != ref.AttachmentID || c.ChunkID != ref.ChunkID || c.Quote != ref.Quote || c.QuoteSHA256 != ref.QuoteSHA256 || c.SourceName != ref.SourceName || c.MIMEType != ref.MIMEType || c.Title != ref.Title || c.Locator != ref.Locator || c.SourceStart != ref.SourceStart || c.SourceEnd != ref.SourceEnd {
				t.Fatal("published report changed selected evidence snapshot")
			}
			storedReportCitations[version.ID] = string(mustBootstrapJSON(t, version.Citations))
			t.Logf("Published verified citation source: call=%s run=%s", c.SourceToolCallIDSnapshot, c.SourceRunIDSnapshot)
		}
	}
	if reportID == "" {
		t.Fatal("no immutable report")
	}
	var exports int
	if err = app.store.DB().QueryRow(`SELECT count(*) FROM artifact_exports e JOIN artifact_versions v ON v.id=e.artifact_version_id WHERE v.artifact_id=?`, reportID).Scan(&exports); err != nil || exports < 2 {
		t.Fatal("DOCX/PDF not generated", exports, err)
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reloaded, err := app.WorkflowFacade.GetRun(p.ID, finished.Run.ID)
	if err != nil || reloaded.Run.Status != workflow.RunCompleted || reads.Load() != expectedReads {
		t.Fatal("restart repeated tools or lost terminal state", err)
	}
	stoppedReport, err := app.ArtifactFacade.GetTaskArtifact(p.ID, finished.Run.ResearchTaskID, reportID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stoppedReport.Versions) != len(storedReportCitations) {
		t.Fatal("restart lost immutable report versions")
	}
	for _, version := range stoppedReport.Versions {
		if storedReportCitations[version.ID] != string(mustBootstrapJSON(t, version.Citations)) {
			t.Fatal("restart changed report citations or their original source provenance")
		}
	}
	t.Logf("%d actual indexed reads -> %d synthesis executions (%d checkpoints, no extra no-progress continuation) -> citation selection -> independent review -> immutable report and DOCX/PDF; source snapshots survive restart. No user task/model/database used.", expectedReads, expectedScreenings, expectedEvents)
}

func lenSlice(value any) int { v, _ := value.([]any); return len(v) }

func mustBootstrapJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
