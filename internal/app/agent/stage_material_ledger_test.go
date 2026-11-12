package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/model/fake"
)

var materialReaderSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["section"],"properties":{"section":{"type":"string","enum":["stage_input","planning_discussion"]},"pointer":{"type":"string","maxLength":2048},"offset":{"type":"integer","minimum":0}}}`)

func materialLedgerFixture(t *testing.T) (*stageMaterialLedger, *stageReadConvergence, func(string, string, int) (tool.Call, model.Message)) {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"A": strings.Repeat("中🙂", 6000), "B": strings.Repeat("中🙂", 6000), "empty": ""})
	c := stageBudgetContract(input, "host")
	c.ID, c.NodeID, c.WorkflowStepID = "execution", "report_drafting", "step"
	g := newStageMaterialLedger(c, "run")
	makePage := func(id, pointer string, offset int) (tool.Call, model.Message) {
		args, _ := json.Marshal(map[string]any{"section": "stage_input", "pointer": pointer, "offset": offset})
		data, err := workflow.ReadResearchStageMaterial(c.Input, pointer, offset, c.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		call := tool.Call{ID: id, RunID: "run", ProviderCallID: id, ToolName: workflow.ResearchTaskReadTool, Arguments: args, Result: &tool.Result{Status: tool.ResultSuccess, Structured: data}}
		return call, model.Message{Role: model.RoleTool, ToolCallID: id, Content: toolCallContext(call)}
	}
	return g, newStageReadConvergence(c, "run"), makePage
}

func materialRequest(messages ...model.Message) model.ChatRequest {
	return model.ChatRequest{Messages: messages, Tools: []model.ToolDefinition{{Name: workflow.ResearchTaskReadTool, InputSchema: materialReaderSchema}}}
}

func TestStageMaterialLedgerSeparatesFetchedVisibleAndColdRestart(t *testing.T) {
	g, _, page := materialLedgerFixture(t)
	call, m := page("A", "/A", 0)
	preview, ok := fitTaskPagePreview(m.Content, 1500)
	if !ok {
		t.Fatal("not a paged fixture")
	}
	m.Content = preview
	req := materialRequest(m)
	views := g.visiblePages(req, []tool.Call{call})
	visibleEnd := views["A"].NextOffset
	if visibleEnd <= 0 || visibleEnd >= 6000 {
		t.Fatal("fitted preview not measured", visibleEnd)
	}
	readState := func(request model.ChatRequest) map[string]any {
		for _, m := range request.Messages {
			if strings.HasPrefix(m.Content, stageMaterialProgressPrefix) {
				var data map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(m.Content, stageMaterialProgressPrefix)), &data)
				return data
			}
		}
		t.Fatal("missing reading state")
		return nil
	}
	first := readState(g.withProgress(req, []tool.Call{call}, nil, 400000))
	record := first["records"].([]any)[0].(map[string]any)
	if len(record["previouslyExposedRanges"].([]any)) != 0 || int(record["nextNeverExposedOffset"].(float64)) != visibleEnd || int(record["fetchedRanges"].([]any)[0].([]any)[1].(float64)) != 6000 || first["scientificCompletionAssessment"] != "not_assessed" || first["scientificCompletion"] != nil {
		t.Fatal("retrieval was misrepresented as exposure/completion", record)
	}
	g.commit(req, []tool.Call{call})
	second := readState(g.withProgress(req, []tool.Call{call}, nil, 400000))["records"].([]any)[0].(map[string]any)
	if len(second["previouslyExposedRanges"].([]any)) != 1 {
		t.Fatal("successful exposure not recorded")
	}
	cold := newStageMaterialLedger(g.contract, "run")
	if len(readState(cold.withProgress(req, []tool.Call{call}, nil, 400000))["records"].([]any)[0].(map[string]any)["previouslyExposedRanges"].([]any)) != 0 {
		t.Fatal("restart inferred old visibility from raw saved result")
	}
	if got := g.withProgress(req, []tool.Call{call}, nil, currentRequestBudgetTokens(req, nil)); !reflect.DeepEqual(got, req) {
		t.Fatal("optional state shortened protected/current evidence to fit")
	}
}

func TestStageMaterialCoordinatesAndCreditsNeverMix(t *testing.T) {
	g, raw, page := materialLedgerFixture(t)
	a, m := page("A", "/A", 0)
	g.commit(materialRequest(m), []tool.Call{a})
	b, n := page("B", "/B", 0)
	g.streak = 3
	if g.inspect(materialRequest(n), []model.ToolCall{{ID: "B", Name: b.ToolName}}, []tool.Call{b}, raw) || g.streak != 0 {
		t.Fatal("different pointer with identical content was considered seen")
	}
	if raw.callEligible(a) || len(raw.visiblePages(materialRequest(m), []tool.Call{a})) != 0 || rawStageInputTransportTurns(g.contract, []tool.Call{a, b}) != 0 {
		t.Fatal("value-relative offset entered whole-input ledger")
	}
	if stageInputTransportTurns(g.contract, []tool.Call{a, b}) != 2 {
		t.Fatal("independent materials did not earn independent transport credit")
	}
	copies := []tool.Call{a, b}
	for i := 0; i < 10; i++ {
		c, _ := page(fmt.Sprint(i), "/A", 0)
		copies = append(copies, c)
	}
	if stageInputTransportTurns(g.contract, copies) != 2 {
		t.Fatal("duplicate pages enlarged execution budget")
	}
	for i := 1; i <= 5; i++ {
		c, v := page(fmt.Sprint("new", i), "/A", i)
		g.streak = 3
		if g.inspect(materialRequest(v), []model.ToolCall{{ID: c.ProviderCallID, Name: c.ToolName}}, []tool.Call{c}, raw) {
			t.Fatal("one new Unicode character was blocked")
		}
		g.commit(materialRequest(v), []tool.Call{c})
	}
}

func TestStageMaterialInvalidOrHiddenPagesCannotCreateReceipts(t *testing.T) {
	for _, mode := range []string{"hash", "value_hash", "encoding", "content", "offset", "node", "foreign_run", "hidden", "duplicate_id"} {
		t.Run(mode, func(t *testing.T) {
			g, _, page := materialLedgerFixture(t)
			call, m := page("A", "/A", 0)
			switch mode {
			case "hash":
				m.Content = strings.ReplaceAll(m.Content, g.contract.InputSHA256, strings.Repeat("0", 64))
			case "value_hash":
				p := g.visiblePages(materialRequest(m), []tool.Call{call})["A"]
				m.Content = strings.ReplaceAll(m.Content, p.ValueSHA256, strings.Repeat("0", 64))
			case "encoding":
				m.Content = strings.Replace(m.Content, `"encoding":"text"`, `"encoding":"json"`, 1)
			case "content":
				m.Content = strings.Replace(m.Content, "中", "伪", 1)
			case "offset":
				m.Content = strings.Replace(m.Content, `"offset":0`, `"offset":1`, 1)
			case "node":
				m.Content = strings.ReplaceAll(m.Content, g.contract.NodeID, "other")
			case "foreign_run":
				call.RunID = "other"
			case "hidden":
				m.Content = `{"contentNotShown":true}`
			}
			req := materialRequest(m)
			if mode == "duplicate_id" {
				req.Messages = append(req.Messages, m)
			}
			g.commit(req, []tool.Call{call})
			if len(g.seen) != 0 {
				t.Fatal("unverified page counted as exposed", mode)
			}
		})
	}
}

func TestStageMaterialSafeConvergenceRetainsBothCoordinatesAndOtherFacts(t *testing.T) {
	g, raw, page := materialLedgerFixture(t)
	a, m := page("A", "/A", 0)
	b, n := page("B", "/B", 0)
	g.commit(materialRequest(m, n), []tool.Call{a, b})
	req := materialRequest(n, model.Message{Role: model.RoleTool, ToolCallID: "other", Content: "UNIQUE_OTHER_FACT"})
	for i := 0; i < 4; i++ {
		if got := g.inspect(req, []model.ToolCall{{ID: "B", Name: b.ToolName}}, []tool.Call{b}, raw); got != (i == 3) {
			t.Fatal("unexpected repetition detection", i, got)
		}
	}
	first, retained, err := g.admitSubmission(req, []tool.Call{b}, nil, 400000, submissionRepairSchema, raw)
	if err != nil || first.ForcedTool != stageSubmissionTool || !strings.Contains(submissionRequestText(first), "UNIQUE_OTHER_FACT") || !strings.Contains(retained, `"pointer":"/A"`) || !strings.Contains(retained, `"pointer":"/B"`) {
		t.Fatal("convergence lost prior material or current other evidence", err)
	}
	if first.Messages[len(first.Messages)-1].HostToolReferences {
		t.Fatal("source fragment promoted to host tool instructions")
	}
	if _, _, err := g.admitSubmission(req, []tool.Call{b}, nil, 1000, submissionRepairSchema, raw); err == nil {
		t.Fatal("oversized evidence silently clipped")
	}
}

func materialReadScript(id, pointer string, offset int) []fake.Step {
	args, _ := json.Marshal(map[string]any{"section": "stage_input", "pointer": pointer, "offset": offset})
	return []fake.Step{{Event: model.Event{Type: model.EventToolCall, ToolCall: &model.ToolCall{ID: id, Name: workflow.ResearchTaskReadTool, Arguments: args}}}, {Event: model.Event{Type: model.EventDone, FinishReason: "tool_calls"}}}
}

func TestStageMaterialRealLoopProgressSubmissionAndCorrection(t *testing.T) {
	for _, novel := range []bool{false, true} {
		t.Run(fmt.Sprint("novel=", novel), func(t *testing.T) {
			scripts := [][]fake.Step{}
			for i := 0; i < 5; i++ {
				offset := 0
				if novel {
					offset = i
				}
				scripts = append(scripts, materialReadScript(fmt.Sprint(i), "/data", offset))
			}
			scripts = append(scripts, submissionToolScript(`{"items":{},"state":"ready"}`), submissionToolScript(`{"items":["corrected"],"state":"ready"}`))
			loop, state, provider := newLoopFixture(t, nil, scripts...)
			bindReadConvergenceFixture(t, loop, state)
			c := loop.runs.(workflowContractRuns).contract
			reader := fixtureTool{definition: tool.Definition{QualifiedName: workflow.ResearchTaskReadTool, Version: "9", Risk: tool.RiskLow, Idempotent: true, Description: "material read", InputSchema: materialReaderSchema}, invoke: func(in tool.Invocation) tool.Result {
				var args workflow.ResearchTaskReadCommand
				_ = json.Unmarshal(in.Arguments, &args)
				page, err := workflow.ReadResearchStageMaterial(c.Input, *args.Pointer, args.Offset, c.NodeID)
				if err != nil {
					t.Fatal(err)
				}
				return tool.Result{Status: tool.ResultSuccess, Structured: page}
			}}
			if err := loop.registry.(*tool.MemoryRegistry).ReplaceNamespace(context.Background(), "builtin.research.task.", []tool.Tool{reader}); err != nil {
				t.Fatal(err)
			}
			if got := loop.Run(context.Background(), "run"); got != OutcomeCompleted {
				t.Fatal(got, state.run.ErrorCode)
			}
			requests := provider.Requests()
			if len(requests) != 7 || len(state.calls) != 5 {
				t.Fatal("unexpected extra work", len(requests), len(state.calls))
			}
			if (requests[5].ForcedTool != "") != (!novel) || requests[6].ForcedTool != stageSubmissionTool {
				t.Fatal("new data forced completion or correction reopened exploration")
			}
			if !strings.Contains(submissionRequestText(requests[1]), stageMaterialProgressPrefix) {
				t.Fatal("real Loop omitted material progress")
			}
			if !novel && !strings.Contains(submissionRequestText(requests[6]), stageMaterialEvidencePrefix) {
				t.Fatal("correction lost addressed evidence")
			}
		})
	}
}
