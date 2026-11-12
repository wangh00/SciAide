package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func TestConvergenceSubmissionProtectsOtherVisibleEvidenceAndLaterCorrection(t *testing.T) {
	g, page := unitReadConvergence(t)
	call, m := page("read", 0, 100)
	source := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "frozen host", HostToolReferences: true}, m}}
	g.commit(source, []tool.Call{call})
	g.streak, g.repeated = 4, g.seen
	fact := strings.Repeat("unique original research fact. ", 400)
	other := model.Message{Role: model.RoleTool, ToolCallID: "other", Content: fact, Parts: []model.ContentPart{{Type: "text", Text: "retained part; preview was incomplete"}}}
	source.ProviderTurns = []model.ProviderTurn{{Items: []model.ProviderItem{{Type: "reasoning", Payload: json.RawMessage(`{"private":"NEVER_EXPOSE_PRIVATE_REASONING"}`)}}, ToolResults: []model.Message{other}}}
	calls := []tool.Call{call, {ProviderCallID: "other", ToolName: "builtin.fixture", Result: &tool.Result{Status: tool.ResultSuccess}}}
	evidence, err := g.retainedEvidence()
	if err != nil {
		t.Fatal(err)
	}
	derive := func(r model.ChatRequest) model.ChatRequest { return g.submission(r, evidence, submissionRepairSchema) }
	limit := currentRequestBudgetTokens(derive(source), nil) - 500
	if _, err := fitFinalRequest(source, calls, nil, limit, unchangedRequest); err != nil {
		t.Fatal("exploration fixture was not admitted", err)
	}
	shrunken, err := fitFinalRequest(source, calls, nil, limit, derive)
	if err != nil || strings.Contains(submissionRequestText(shrunken), fact) {
		t.Fatal("fixture must previously fit only by shortening other evidence", err)
	}
	if _, _, err := g.admitSubmission(source, calls, nil, limit, submissionRepairSchema); apperr.Public(err).Code != "WORKFLOW_AI_READ_STALLED" {
		t.Fatal("convergence silently shrank another admitted result", err)
	}
	first, retained, err := g.admitSubmission(source, calls, nil, 400000, submissionRepairSchema)
	if err != nil || !strings.Contains(submissionRequestText(first), fact) {
		t.Fatal("first submission lost other evidence", err)
	}
	corrected := model.ChatRequest{Messages: []model.Message{{Role: model.RoleUser, Content: "frozen host", HostToolReferences: true}, {Role: model.RoleUser, Content: "correct the schema"}}}
	corrected, err = fitFinalRequest(corrected, calls, nil, 400000, func(r model.ChatRequest) model.ChatRequest {
		r = stageSubmissionRequest(r, submissionRepairSchema)
		r.Messages = append(r.Messages, model.Message{Role: model.RoleUser, Content: retained})
		return r
	})
	text := submissionRequestText(corrected)
	if err != nil || !strings.Contains(text, fact) || !strings.Contains(text, "retained part; preview was incomplete") || strings.Contains(text, "NEVER_EXPOSE_PRIVATE_REASONING") || corrected.Messages[len(corrected.Messages)-1].HostToolReferences {
		t.Fatal("correction receipt changed/dropped evidence or exposed native state", err)
	}
}

func planningConvergencePage(id, sha string, start, end, total int) (tool.Call, model.Message) {
	args, _ := json.Marshal(map[string]any{"section": "planning_discussion", "offset": start})
	page, _ := json.Marshal(map[string]any{"section": "planning_discussion", "sha256": sha, "offset": start, "nextOffset": end, "totalCharacters": total, "hasMore": end < total, "content": strings.Repeat("协", end-start)})
	call := tool.Call{ID: id, RunID: "run", ProviderCallID: id, ToolName: workflow.ResearchTaskReadTool, Arguments: args, Result: &tool.Result{Status: tool.ResultSuccess, Structured: page}}
	return call, model.Message{Role: model.RoleTool, ToolCallID: id, Content: toolCallContext(call)}
}

func TestConvergenceKnownPlanningPrerequisiteNeedsCommittedCompleteVisiblePages(t *testing.T) {
	for _, mode := range []string{"unread", "partial", "gap", "mixed_sha", "mixed_total", "hidden", "uncommitted", "complete"} {
		t.Run(mode, func(t *testing.T) {
			g, page := unitReadConvergence(t)
			c, m := page("read", 0, 100)
			g.commit(model.ChatRequest{Messages: []model.Message{m}}, []tool.Call{c})
			g.requiresPlanning = true
			g.streak, g.repeated = 4, g.seen
			sha := strings.Repeat("a", 64)
			a, x := planningConvergencePage("plan-a", sha, 0, 50, 100)
			b, y := planningConvergencePage("plan-b", sha, 50, 100, 100)
			switch mode {
			case "unread":
			case "partial":
				g.commit(model.ChatRequest{Messages: []model.Message{x}}, []tool.Call{a})
			case "gap":
				b, y = planningConvergencePage("plan-b", sha, 60, 100, 100)
				g.commit(model.ChatRequest{Messages: []model.Message{x, y}}, []tool.Call{a, b})
			case "mixed_sha":
				b, y = planningConvergencePage("plan-b", strings.Repeat("b", 64), 50, 100, 100)
				g.commit(model.ChatRequest{Messages: []model.Message{x, y}}, []tool.Call{a, b})
			case "mixed_total":
				b, y = planningConvergencePage("plan-b", sha, 50, 101, 101)
				g.commit(model.ChatRequest{Messages: []model.Message{x, y}}, []tool.Call{a, b})
			case "hidden":
				x.Content = `{"contentNotShown":true}`
				g.commit(model.ChatRequest{Messages: []model.Message{x, y}}, []tool.Call{a, b})
			case "uncommitted": // Stored pages alone are not exposure receipts.
			case "complete":
				g.commit(model.ChatRequest{Messages: []model.Message{x}}, []tool.Call{a})
				g.commit(model.ChatRequest{ProviderTurns: []model.ProviderTurn{{ToolResults: []model.Message{y}}}}, []tool.Call{b})
			}
			_, err := g.retainedEvidence()
			if mode != "complete" {
				if apperr.Public(err).Code != "WORKFLOW_AI_READ_STALLED" || !strings.Contains(apperr.Public(err).Details, "required_planning_discussion_unread") {
					t.Fatal("missing negotiation was silently deemed complete", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			first, retained, err := g.admitSubmission(model.ChatRequest{Messages: []model.Message{m}}, []tool.Call{c}, nil, 400000, submissionRepairSchema)
			if err != nil || !strings.Contains(submissionRequestText(first), "plan-a") || !strings.Contains(retained, "plan-b") {
				t.Fatal("complete negotiation disappeared from submission", err)
			}
		})
	}
}

func TestConvergencePlanningRequirementUsesHostLocationsAndFrozenInterface(t *testing.T) {
	for raw, want := range map[string]bool{`{"context":{"discussionRecord":{"sourcePlannerRunId":"planner","messageCount":1}}}`: true, `{"routeContext":{"discussionRecord":{"sourcePlannerRunId":"planner","messageCount":1}}}`: true, `{"userIntent":{"readSection":"planning_discussion","source":"host_saved_planning_discussion"}}`: true, `{"paper":{"context":{"discussionRecord":{}}}}`: false, `{"context":{"discussionRecord":null}}`: false} {
		required, _ := planningDiscussionRequirement(json.RawMessage(raw), "dynamic-report-v6")
		if required != want {
			t.Fatal("unexpected host navigation requirement", raw)
		}
	}
	for _, raw := range []string{`{"context":{"discussionRecord":{}}}`, `{"context":{"discussionRecord":"business field"}}`, `{"routeContext":{"discussionRecord":{"messageCount":1}}}`} {
		if required, ambiguous := planningDiscussionRequirement(json.RawMessage(raw), "custom-fixture-v1"); required || !ambiguous {
			t.Fatal("custom/unknown field created a host prerequisite", raw)
		}
	}
	g, page := unitReadConvergence(t)
	g.requiresPlanning = true
	c, m := page("read", 0, 100)
	g.commit(model.ChatRequest{Messages: []model.Message{m}}, []tool.Call{c})
	req := model.ChatRequest{Messages: []model.Message{m}, Tools: []model.ToolDefinition{{Name: workflow.ResearchTaskReadTool, InputSchema: json.RawMessage(`{"properties":{"section":{"enum":["stage_input"]}}}`)}}}
	g.streak = 3
	if g.inspect(req, []model.ToolCall{{ID: "read", Name: c.ToolName}}, []tool.Call{c}) || g.streak != 0 {
		t.Fatal("legacy frozen reader acquired an impossible new gate")
	}
	req.Tools[0].InputSchema = json.RawMessage(`{"properties":{"section":{"enum":["stage_input","planning_discussion"]}}}`)
	g.streak = 3
	if !g.inspect(req, []model.ToolCall{{ID: "read", Name: c.ToolName}}, []tool.Call{c}) {
		t.Fatal("authorized ordinary-stage guard did not activate")
	}
}

func TestConvergenceSharedPageLookupKeepsMetadataAndUnseenContent(t *testing.T) {
	g, page := unitReadConvergence(t)
	a, m := page("A", 0, 100)
	b, n := page("B", 80, 200)
	source := model.ChatRequest{Messages: []model.Message{m, n}}
	g.commit(source, []tool.Call{a, b})
	g.streak, g.repeated = 4, g.seen
	first, retained, err := g.admitSubmission(source, []tool.Call{a, b}, nil, 400000, submissionRepairSchema)
	if err != nil || len(g.seen) != 1 || g.seen[0] != (readSpan{0, 200}) {
		t.Fatal("overlapping pages not preserved", err, g.seen)
	}
	for _, text := range []string{submissionRequestText(first), retained} {
		for _, want := range []string{"contentRetainedInReceipt", "contentReceiptLookup", "originalToolCallId", "nextOffset", g.contract.InputSHA256} {
			if !strings.Contains(text, want) {
				t.Fatal("shared-page navigation/metadata disappeared", want)
			}
		}
	}
	unseen, v := page("new", 0, 201)
	unchanged := g.shareVerifiedPageContent(model.ChatRequest{Messages: []model.Message{v}}, []tool.Call{unseen})
	if unchanged.Messages[0].Content != v.Content {
		t.Fatal("unseen suffix was removed without a receipt")
	}
	// Ambiguous call IDs in a malformed in-memory view are never redacted or
	// counted. Normal SQLite runs additionally enforce unique IDs.
	other := a
	other.ToolName = "builtin.fixture"
	for _, test := range []struct {
		request model.ChatRequest
		calls   []tool.Call
	}{{model.ChatRequest{Messages: []model.Message{m}}, []tool.Call{a, other}}, {model.ChatRequest{Messages: []model.Message{m, m}}, []tool.Call{a}}} {
		if len(g.visiblePages(test.request, test.calls)) != 0 {
			t.Fatal("ambiguous ID counted as visible evidence")
		}
		shared := g.shareVerifiedPageContent(test.request, test.calls)
		if shared.Messages[0].Content != m.Content {
			t.Fatal("ambiguous ID deleted another result's content")
		}
	}
}
