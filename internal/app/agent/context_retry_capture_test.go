package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/tools/builtin"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Offline scoped capture: exact frozen prompt/input, model draft and usage.
// Fixed lifecycle/resource guidance is reconstructed; no HTTP body was saved,
// no commercial model is called and no formal task state is changed.
func TestCapturedContextRetryBudget(t *testing.T) {
	dir := os.Getenv("SCIAIDE_CONTEXT_RETRY_CAPTURE")
	if dir == "" {
		t.Skip("scoped capture only")
	}
	read := func(n string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, n))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var basis workflow.ReviewBasisErrors
	if json.Unmarshal(read("basis.json"), &basis) != nil {
		t.Fatal("bad basis")
	}
	var journals []struct {
		Draft string `json:"draft_text"`
	}
	if json.Unmarshal(read("journal.json"), &journals) != nil || len(journals) != 1 {
		t.Fatal("bad journal")
	}
	candidate, schema := journals[0].Draft, json.RawMessage(read("schema.json"))
	var state workflow.ReviewFindingStateErrors
	if json.Unmarshal(read("finding-state.json"), &state) != nil {
		t.Fatal("bad origin-state diagnostic")
	}
	diagnostic := errors.Join(&basis, &state)
	repair := newReviewRepair(candidate, schema, diagnostic)
	if repair == nil {
		t.Fatal("no repair")
	}
	nav := reviewFullResultNavigation(diagnostic)
	correction := "Host submission validation failed: " + strings.ReplaceAll(string(read("validation.txt")), "\r\n", "\n") + ". Follow the current correction interface and its current-version source menu; keep completed evidence and do not repeat exploration."
	growth := utf8.RuneCountInString(string(repair.base)+correction+nav) + len(repair.schema) + utf8.RuneCountInString(repair.instruction())
	t.Logf("base=%d correction=%d nav=%d repairSchema=%d instruction=%d growth=%d actualDerived=%d", utf8.RuneCount(repair.base), utf8.RuneCountInString(correction), utf8.RuneCountInString(nav), len(repair.schema), utf8.RuneCountInString(repair.instruction()), growth, estimateRequestTokens(stageRepairRequest(model.ChatRequest{}, schema, repair.schema, repair.instruction())))
	var executions []struct {
		Prompt string `json:"prompt_text"`
	}
	if json.Unmarshal(read("execution.json"), &executions) != nil || len(executions) != 1 {
		t.Fatal("invalid execution")
	}
	prompt := executions[0].Prompt
	input := json.RawMessage(read("input.json"))
	c := stageBudgetContract(input, prompt)
	c.OutputSchema = schema
	c.WorkflowRunID = "2340f826-dd5e-4929-a837-1a74dc88d0fa"

	var rows []struct {
		ID             string `json:"id"`
		ProviderCallID string `json:"provider_call_id"`
		ToolName       string `json:"tool_name"`
		Arguments      string `json:"arguments_json"`
		ModelContext   string `json:"model_context_text"`
		Text           string `json:"text_content"`
		Structured     string `json:"structured_json"`
	}
	if json.Unmarshal(read("calls.json"), &rows) != nil {
		t.Fatal("bad calls")
	}
	calls := []tool.Call{}
	for _, r := range rows {
		calls = append(calls, tool.Call{ID: r.ID, RunID: "0eb7d617-068a-44cc-8488-4a5ff3e0f840", ProviderCallID: r.ProviderCallID, ToolName: r.ToolName, Arguments: json.RawMessage(r.Arguments), ModelContext: r.ModelContext, ModelContextVersion: tool.ModelContextSnapshotVersion, Result: &tool.Result{Status: tool.ResultSuccess, Text: r.Text, Structured: json.RawMessage(r.Structured)}})
	}
	var refs []struct {
		ID        string `json:"id"`
		Citations string `json:"citations_json"`
	}
	_ = json.Unmarshal(read("citations.json"), &refs)
	for i := range calls {
		for _, r := range refs {
			if calls[i].ID == r.ID {
				_ = json.Unmarshal([]byte(r.Citations), &calls[i].Result.Citations)
			}
		}
	}

	var sessionRows []struct {
		Scope     string `json:"scope_json"`
		ScopeHash string `json:"scope_sha256"`
		Facts     string `json:"facts_json"`
	}
	// Explicit SQLite-column tags preserve original session/action identity.
	var capturedActions []struct {
		RunID     string `json:"run_id"`
		ID        string `json:"action_id"`
		ScopeHash string `json:"scope_sha256"`
		Seed      string `json:"seed_json"`
		Root      int    `json:"is_root"`
		CreatedAt string `json:"created_at"`
	}
	if json.Unmarshal(read("resource-session.json"), &sessionRows) != nil || len(sessionRows) != 1 || json.Unmarshal(read("resource-actions.json"), &capturedActions) != nil {
		t.Fatal("invalid session capture")
	}
	session := resource.Session{ScopeHash: sessionRows[0].ScopeHash, Facts: json.RawMessage(sessionRows[0].Facts)}
	if json.Unmarshal([]byte(sessionRows[0].Scope), &session.Scope) != nil {
		t.Fatal("invalid original scope")
	}
	repo := &capturedResourceMenu{session: session}
	for _, row := range capturedActions {
		a := resource.Action{RunID: row.RunID, ID: row.ID, ScopeHash: row.ScopeHash, Root: row.Root != 0}
		if json.Unmarshal([]byte(row.Seed), &a.Seed) != nil {
			t.Fatal("invalid original action")
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, row.CreatedAt)
		repo.actions = append(repo.actions, a)
	}
	// The scoped list here was captured in database order, which is not the
	// provider-menu order. Mirror ListActions without reissuing capabilities.
	sort.Slice(repo.actions, func(i, j int) bool {
		a, b := repo.actions[i], repo.actions[j]
		if a.Root != b.Root {
			return a.Root
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	registry := tool.NewRegistry()
	actions := builtin.NewResourceActions(repo, registry, capturedResourceScope{session.Scope}, nil)
	var definitions []tool.Definition
	for _, item := range []tool.Tool{builtin.NewListAttachments(nil), builtin.NewInspectDocument(nil), builtin.NewReadKnowledge(nil), builtin.NewListWorkspace(nil), builtin.NewReadText(nil), builtin.NewBrowserOpen(nil), builtin.NewMCPList(nil, nil, nil), builtin.NewToolsSearch(nil, nil, nil), builtin.NewWebOpen(), builtin.NewWebSearch(nil), builtin.NewUserInputRequest(), builtin.NewSkillLoad(capturedSkillDefinitions{}), builtin.NewListSkillResources(nil), builtin.NewReadSkillResource(nil), builtin.NewResearchDiscussionTools(nil)[0], actions.OpenTool(), actions.SearchTool()} {
		d, err := item.Definition(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if hash := session.Scope.ToolContracts[d.QualifiedName]; hash != "" && hash != tool.DefinitionFingerprint(d) {
			t.Fatal("captured/current definition differs; cannot claim actual fixed-context reconstruction", d.QualifiedName)
		}
		definitions = append(definitions, d)
	}
	view, err := actions.Prepare(context.Background(), resource.Request{RunID: session.Scope.RunID, ProjectID: session.Scope.ProjectID, WorkflowStepID: session.Scope.WorkflowStepID, Definitions: definitions, SkillNames: session.Scope.SkillNames, SkillDiscovery: session.Scope.SkillDiscovery, SelectedOnly: session.Scope.SelectedOnly, SelectedAttachmentIDs: session.Scope.SelectedAttachmentIDs})
	if err != nil {
		t.Fatal("production resource menu rebuild failed", err)
	}
	var guidance struct {
		ExecutionSystemContext  string          `json:"executionSystemContext"`
		ExecutionDynamicState   string          `json:"executionDynamicState"`
		ExecutionTaskReadSchema json.RawMessage `json:"executionTaskReadSchema"`
		SkillDiscovery          bool            `json:"skillDiscovery"`
	}
	var routing []struct {
		Prompt string `json:"prompt_snapshot"`
	}
	if json.Unmarshal(read("guidance-built.json"), &guidance) != nil || guidance.ExecutionSystemContext == "" || guidance.ExecutionDynamicState == "" || len(guidance.ExecutionTaskReadSchema) == 0 || json.Unmarshal(read("skills.json"), &routing) != nil || len(routing) != 1 {
		t.Fatal("invalid execution guidance/routing capture")
	}
	defs, discovery := deferredMCPDefinitions(view.Definitions, calls)
	defs = executionTaskReadDefinitions(defs, true, guidance.ExecutionTaskReadSchema)
	system := guidance.ExecutionSystemContext
	if discovery {
		system += "\n" + mcpDiscoveryGuidance
	}
	if definitionVisible(defs, userinput.ToolName) {
		system += "\n" + userinput.Rules
	}
	progress, _ := resourceStageProgress(0, guidance.SkillDiscovery, calls)
	dynamic := guidance.ExecutionDynamicState + "\n" + view.Context + progress

	msgs := []conversation.Message{checkpointText("u", "run", prompt)}
	run := chat.Run{ID: "0eb7d617-068a-44cc-8488-4a5ff3e0f840", UserMessageID: "u"}
	limits := ContextLimits{EffectiveTokens: 258400, AutoCompactTokens: 244800, CurrentUserHostToolReferences: true}
	build := func(ms []conversation.Message) (model.ChatRequest, ContextBuildInfo, error) {
		return NewContextBuilder(0).buildWithResearchState(context.Background(), ms, "", "u", defs, calls, skill.RunContext{}, routing[0].Prompt, system, dynamic, limits, contextmemory.Checkpoint{})
	}
	first := budgetedStageMessages(msgs, run, c, defs, calls, system, dynamic, routing[0].Prompt, 258400, 244800)
	second := budgetedStageMessages(msgs, run, c, defs, calls, system, dynamic, routing[0].Prompt, 258400, 244800-growth)
	firstReq, info, e := build(first)
	if e != nil {
		t.Fatal("first build failed", e)
	}
	t.Logf("first len=%d estimated=%d error=%v", utf8.RuneCountInString(conversationText(first[0])), info.EstimatedTokens, e)
	_, info, e = build(second)
	if e != nil {
		t.Fatal("unobserved projection should fit", e)
	}
	t.Logf("second len=%d estimated=%d error=%v", utf8.RuneCountInString(conversationText(second[0])), info.EstimatedTokens, e)
	var events []struct {
		Type    string `json:"event_type"`
		Payload string `json:"payload_json"`
	}
	_ = json.Unmarshal(read("events.json"), &events)

	var obs chat.ContextUsageObservation
	for _, ev := range events {
		if strings.Contains(ev.Type, "context_usage") {
			if json.Unmarshal([]byte(ev.Payload), &obs) != nil {
				t.Fatal("bad usage")
			}
		}
	}
	if obs.InputTokens != 84795 || obs.CharacterEstimate != 223378 {
		t.Fatal("incident observation changed")
	}
	matchedPrompt := false
	for _, m := range firstReq.Messages {
		if m.Content == conversationText(first[0]) {
			for _, part := range requestUsageComponents(model.ChatRequest{Messages: []model.Message{m}}) {
				matchedPrompt = matchedPrompt || obs.Components[part.key] > 0
			}
		}
	}
	if !matchedPrompt {
		t.Fatal("reconstructed first material view differs from measured request")
	}
	limits.UsageObservation = &obs
	_, info, e = build(second)
	if e == nil || !strings.Contains(e.Error(), "system, tool definitions and latest conversation message exceed context window") {
		t.Fatal("old projection no longer reproduces incident", e)
	}
	t.Logf("second WITH actual usage len=%d estimated=%d error=%v", utf8.RuneCountInString(conversationText(second[0])), info.EstimatedTokens, e)
	stable, info, e := build(first)
	if e != nil {
		t.Fatal("stable view still failed", e)
	}
	t.Logf("stable WITH actual usage len=%d estimated=%d error=%v", utf8.RuneCountInString(conversationText(first[0])), info.EstimatedTokens, e)
	stable.Messages = append(stable.Messages, model.Message{Role: model.RoleAssistant, Content: string(repair.base)}, model.Message{Role: model.RoleUser, Content: correction, HostToolReferences: true})
	final, e := fitFinalRequest(stable, calls, &obs, 258400, func(r model.ChatRequest) model.ChatRequest {
		return stageRepairRequest(r, workflow.ReviewSubmissionSchema(schema), repair.schema, repair.instruction())
	})
	if e != nil || currentRequestBudgetTokens(final, &obs) > 258400 {
		t.Fatal("complete correction failed", e)
	}
	found := false
	for _, m := range final.Messages {
		found = found || m.Content == conversationText(first[0])
	}
	if !found {
		t.Fatal("final lost the admitted view")
	}
	t.Logf("stable FINAL tokens=%d error=%v", currentRequestBudgetTokens(final, &obs), e)
}
