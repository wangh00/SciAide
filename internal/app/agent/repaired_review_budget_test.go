package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/contextmemory"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/resource"
	"github.com/wangh00/SciAide/internal/app/skill"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/userinput"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
	"github.com/wangh00/SciAide/internal/opensciskill"
	"github.com/wangh00/SciAide/internal/tools/builtin"
)

// Exact scoped frozen prompt/tools/schema; fixed context is an explicit
// synthetic footprint, since the formal store has no full wire body.
func TestCapturedRepairedReviewFitsFirstRequestAndFullSubmission(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REPAIRED_REVIEW_CAPTURE")
	if dir == "" {
		t.Skip("no scoped repaired-review capture")
	}
	read := func(name string) []byte {
		v, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	original, projected := string(read("prompt-original.txt")), string(read("prompt-shared.txt"))
	var rows []struct {
		ID             string `json:"id"`
		ProviderCallID string `json:"provider_call_id"`
		ToolName       string `json:"tool_name"`
		Arguments      string `json:"arguments_json"`
		ModelContext   string `json:"model_context_text"`
		Text           string `json:"text_content"`
		Status         string `json:"status"`
		Structured     string `json:"structured_json"`
	}
	if err := json.Unmarshal(read("completed-tools.json"), &rows); err != nil || len(rows) != 3 || len([]rune(original)) != 236841 {
		t.Fatal("incident identity changed", err)
	}
	calls := []tool.Call{}
	for _, row := range rows {
		calls = append(calls, tool.Call{ID: row.ID, ProviderCallID: row.ProviderCallID, ToolName: row.ToolName, Status: tool.CallStatus(row.Status), Arguments: json.RawMessage(row.Arguments), ModelContext: row.ModelContext, ModelContextVersion: tool.ModelContextSnapshotVersion, Result: &tool.Result{Status: tool.ResultSuccess, Text: row.Text, Structured: json.RawMessage(row.Structured)}})
	}
	limits := ContextLimits{EffectiveTokens: 258400, AutoCompactTokens: 244800, CurrentUserHostToolReferences: true}
	fixed := strings.Repeat("s", 32891-len([]rune(fixedSystemRules))-2)
	build := func(prompt string) (model.ChatRequest, ContextBuildInfo, error) {
		return NewContextBuilder(0).buildWithResearchState(context.Background(), []conversation.Message{checkpointText("u", "run", prompt)}, "", "u", nil, calls, skill.RunContext{}, "", fixed, "", limits, contextmemory.Checkpoint{})
	}
	if _, _, err := build(original); err == nil || err.Error() != "system, tool definitions and latest conversation message exceed context window" {
		t.Fatal("original request no longer reproduces local failure", err)
	}
	for _, candidate := range []struct{ name, prompt string }{{"frozen-prompt projection", projected}, {"production pre-bridge build", string(read("prompt-built.txt"))}} {
		t.Run(candidate.name, func(t *testing.T) {
			request, info, err := build(candidate.prompt)
			if err != nil || info.EstimatedTokens > limits.EffectiveTokens || info.CompactedThroughMessageID != "" {
				t.Fatal("shared input still fails before model request", info, err)
			}
			final, err := fitFinalRequest(request, calls, nil, limits.EffectiveTokens, func(r model.ChatRequest) model.ChatRequest {
				return stageSubmissionRequest(r, read("schema-original.json"))
			})
			if err != nil {
				minimum := request
				minimum.Messages = cloneModelMessages(request.Messages)
				for i := range minimum.Messages {
					if minimum.Messages[i].Role == model.RoleTool {
						minimum.Messages[i].Content = truncateToolContext(minimum.Messages[i].Content, 256)
					}
				}
				t.Fatal("complete schema submission still overflows", err, apperr.Public(err), info.EstimatedTokens, "min final", currentRequestBudgetTokens(stageSubmissionRequest(minimum, read("schema-original.json")), nil))
			}
			if final.Messages[1].Content != candidate.prompt || request.Messages[1].Content != candidate.prompt || len(final.Tools) != 1 {
				t.Fatal("fixed projected input/schema was truncated")
			}
			t.Logf("original=%d shared=%d first=%d final=%d budget=%d; fixed=32891 explicitly synthetic; completed calls=%d", len([]rune(original)), len([]rune(candidate.prompt)), info.EstimatedTokens, currentRequestBudgetTokens(final, nil), limits.EffectiveTokens, len(calls))
		})
	}

	t.Run("production fixed context from captured session", func(t *testing.T) {
		// Optional newly bound handoff/grown-report prompt, still using the
		// incident's actual resource/Skill context and unchanged 258400 budget.
		// This is an offline capacity replay, not a production API measurement.
		if path := os.Getenv("SCIAIDE_HANDOFF_BUDGET_PROMPT"); path != "" {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			projected = string(b)
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
		if json.Unmarshal(read("guidance-built.json"), &guidance) != nil || guidance.ExecutionSystemContext == "" || guidance.ExecutionDynamicState == "" || len(guidance.ExecutionTaskReadSchema) == 0 || json.Unmarshal(read("run-skill-routing.json"), &routing) != nil || len(routing) != 1 {
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
		buildActual := func(prompt string) (model.ChatRequest, ContextBuildInfo, error) {
			return NewContextBuilder(0).buildWithResearchState(context.Background(), []conversation.Message{checkpointText("u", "run", prompt)}, "", "u", defs, calls, skill.RunContext{}, routing[0].Prompt, system, dynamic, limits, contextmemory.Checkpoint{})
		}
		if _, _, err := buildActual(original); err == nil || !strings.Contains(err.Error(), "system, tool definitions and latest conversation message exceed context window") {
			t.Fatal("actual reconstructed fixed context no longer reproduces incident", err)
		}
		request, info, err := buildActual(projected)
		if err != nil || info.CompactedThroughMessageID != "" {
			t.Fatal("production fixed context fails with sharing", err)
		}
		final, err := fitFinalRequest(request, calls, nil, limits.EffectiveTokens, func(r model.ChatRequest) model.ChatRequest {
			return stageSubmissionRequest(r, read("schema-original.json"))
		})
		if err != nil || currentRequestBudgetTokens(final, nil) > limits.EffectiveTokens {
			t.Fatal("production fixed context full submission failed", err)
		}
		found := false
		for _, msg := range final.Messages {
			if msg.Role == model.RoleUser && msg.Content == projected {
				found = true
			}
		}
		if !found {
			t.Fatal("complete input not retained in final submission")
		}
		t.Logf("reconstructed production fixed context: actions=%d definitions=%d input=%d first=%d final=%d budget=%d; no guessed fixed padding, no commercial wire body available", len(view.OpenIDs)+len(view.SearchIDs), len(defs), len([]rune(projected)), info.EstimatedTokens, currentRequestBudgetTokens(final, nil), limits.EffectiveTokens)
		// The last actual review was 9007 characters. Keep a candidate of that
		// real size plus explicit synthetic correction feedback during final
		// admission too, so fixing the first request cannot hide correction cost.
		var prior struct {
			Executions []struct {
				ID   string `json:"id"`
				Text string `json:"output_text"`
			} `json:"executions"`
		}
		if json.Unmarshal(read("formal-state-before.json"), &prior) != nil {
			t.Fatal("missing prior review capture")
		}
		candidate := ""
		for _, e := range prior.Executions {
			if strings.Contains(e.Text, "reviewFindings") && len(e.Text) > len(candidate) {
				candidate = e.Text
			}
		}
		if len([]rune(candidate)) != 9007 {
			t.Fatal("prior review candidate size changed", len([]rune(candidate)))
		}
		correcting := request
		correcting.Messages = cloneModelMessages(request.Messages)
		correcting.Messages = append(correcting.Messages, model.Message{Role: model.RoleAssistant, Content: candidate}, model.Message{Role: model.RoleUser, Content: "The preceding assistant message is the rejected result candidate, not an accepted stage result. Host submission validation failed: " + strings.Repeat("Synthetic feedback; verify original digest and original shared-value path. ", 20) + ". Correct only the result against the original frozen schema and output protocol, and submit the complete corrected result. Do not repeat completed tools.", HostToolReferences: true})
		repaired, err := fitFinalRequest(correcting, calls, nil, limits.EffectiveTokens, func(r model.ChatRequest) model.ChatRequest {
			return stageSubmissionRequest(r, read("schema-original.json"))
		})
		if err != nil {
			minimum := correcting
			minimum.Messages = cloneModelMessages(correcting.Messages)
			for i := range minimum.Messages {
				if minimum.Messages[i].Role == model.RoleTool {
					minimum.Messages[i].Content = truncateToolContext(minimum.Messages[i].Content, 256)
				}
			}
			t.Fatal("actual-sized review correction cannot retain full result candidate", err, apperr.Public(err).Details, "minimum", currentRequestBudgetTokens(stageSubmissionRequest(minimum, read("schema-original.json")), nil))
		}
		t.Logf("actual prior candidate=%d chars plus synthetic feedback; complete schema correction=%d <=%d", len([]rune(candidate)), currentRequestBudgetTokens(repaired, nil), limits.EffectiveTokens)
	})
}

type capturedResourceMenu struct {
	resource.Repository
	session resource.Session
	actions []resource.Action
}

func (r *capturedResourceMenu) GetSession(context.Context, string) (resource.Session, bool, error) {
	return r.session, true, nil
}
func (r *capturedResourceMenu) ListActions(context.Context, string, int) ([]resource.Action, error) {
	return r.actions, nil
}

type capturedResourceScope struct{ scope resource.Scope }

func (s capturedResourceScope) ProjectIDForRun(context.Context, string) (string, error) {
	return s.scope.ProjectID, nil
}
func (s capturedResourceScope) ResearchTaskIDForSubject(context.Context, tool.SubjectKind, string) (string, error) {
	return s.scope.TaskID, nil
}
func (s capturedResourceScope) WorkspaceRootForSubject(context.Context, tool.SubjectKind, string) (string, error) {
	return s.scope.WorkspaceRoot, nil
}

type capturedSkillDefinitions struct{}

func (capturedSkillDefinitions) Catalog(context.Context, string) (opensciskill.Snapshot, error) {
	return opensciskill.Snapshot{}, fmt.Errorf("definition inspection only")
}
func (capturedSkillDefinitions) Browse(context.Context, string, string) ([]opensciskill.Info, error) {
	return nil, fmt.Errorf("definition inspection only")
}
func (capturedSkillDefinitions) LoadStructuredForRun(context.Context, string, string, string, string, string, int, int) (opensciskill.Info, opensciskill.Chunk, bool, error) {
	return opensciskill.Info{}, opensciskill.Chunk{}, false, fmt.Errorf("definition inspection only")
}
