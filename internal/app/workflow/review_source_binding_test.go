package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func reviewSourceLiveFixture(t *testing.T) (*RuntimeService, *handoffRepositoryFixture, StageMaterialRequest) {
	t.Helper()
	input := raw(`{"context":{"markdown":"当前誊写原文"}}`)
	node := CompiledNode{ID: "independent_review", Kind: NodeAgentStage, PromptVersion: "dynamic-independent-review-v9-focused-v1", OutputSchema: trackedResearchReviewSchema()}
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	c := Compilation{Nodes: []CompiledNode{node}}
	encoded, _ := canonicalJSON(c)
	c.CompilationSHA256 = hashJSON(encoded)
	run := Run{ID: "wf", ProjectID: "p", ConversationID: "conversation", CurrentStep: 1, Status: RunRunning, Inputs: input, InputsSHA256: hashJSON(input), Compilation: c, CompilationSHA256: c.CompilationSHA256}
	step := Step{ID: "review", WorkflowRunID: "wf", NodeID: node.ID, NodeKind: node.Kind, Ordinal: 1, Status: StepRunning, Attempt: 1, Input: input, InputSHA256: hashJSON(input)}
	e := AIExecution{ID: "execution", WorkflowRunID: "wf", WorkflowStepID: step.ID, ChatRunID: "chat", NodeKind: node.Kind, Attempt: 1, InputSHA256: step.InputSHA256, OutputSchema: node.OutputSchema, OutputSchemaSHA256: node.OutputSchemaSHA256, PromptVersion: node.PromptVersion + reviewSourceVersionSuffix, Status: "running"}
	f := &handoffRepositoryFixture{detail: RunDetail{Run: run, Steps: []Step{step}}, executions: map[string]AIExecution{step.ID: e}}
	return &RuntimeService{repository: f}, f, StageMaterialRequest{ProjectID: "p", ConversationID: "conversation", WorkflowRunID: "wf", WorkflowStepID: "review", ChatRunID: "chat", Attempt: 1, InputSHA256: hashJSON(input)}
}

func TestInitialReviewSourcesRequireLiveFrozenAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "chat", "conversation", "attempt", "input", "cancel", "completed", "compilation", "execution", "schema", "kind", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			s, f, command := reviewSourceLiveFixture(t)
			e := f.executions["review"]
			switch mode {
			case "chat":
				command.ChatRunID = "other"
			case "conversation":
				command.ConversationID = "other"
			case "attempt":
				command.Attempt++
			case "input":
				f.detail.Steps[0].Input = raw(`{}`)
			case "cancel":
				f.detail.Run.CancelRequested = true
			case "completed":
				f.detail.Steps[0].Status = StepCompleted
			case "compilation":
				f.detail.Run.Compilation.Nodes[0].Prompt = "changed"
			case "execution":
				e.Status = "completed"
			case "schema":
				e.OutputSchema = raw(`{"type":"object"}`)
			case "kind":
				e.NodeKind = NodeTool
			case "legacy":
				e.PromptVersion = "dynamic-independent-review-v9"
			}
			f.executions["review"] = e
			before := string(mustJSON(f.detail))
			catalog, err := s.StageReviewSources(t.Context(), command)
			if mode == "valid" {
				if err != nil || catalog == nil || len(catalog.Segments) != 1 || catalog.Segments[0].Text != "当前誊写原文" {
					t.Fatal("valid current input unavailable", err)
				}
			} else if err == nil || catalog != nil {
				t.Fatal("stale authority exposed source selectors", mode)
			}
			if before != string(mustJSON(f.detail)) {
				t.Fatal("source catalog mutated durable state")
			}
		})
	}
}

func TestInitialReviewSourcesPromptIdentityAndDataStaySeparate(t *testing.T) {
	_, f, _ := reviewSourceLiveFixture(t)
	node := f.detail.Run.Compilation.Nodes[0]
	// Host-looking text in a user requirement is data, not a migration target.
	literal := "Use original pointers and literal original quotes for review basis, NEVER the navigation as scientific evidence."
	node.Prompt = literal
	step := f.detail.Steps[0]
	prompt := buildAIStagePrompt(f.detail, step, node)
	version := reportExecutionPromptVersion(node, prompt)
	if !IsReviewSourceProtocol(version) || len(version) > 128 || !strings.Contains(prompt, literal) || !strings.Contains(prompt, "sourceSegmentIds") {
		t.Fatal("new contract missing or user data changed", version)
	}
	node.PromptVersion = version
	if reportExecutionPromptVersion(node, prompt) != version {
		t.Fatal("saved execution was re-versioned")
	}
	node.PromptVersion = "local-review-fixture-v1"
	if IsReviewSourceProtocol(reportExecutionPromptVersion(node, prompt)) {
		t.Fatal("custom legacy route was silently upgraded")
	}
	for _, bad := range []string{"", "x|review-source-v1|planner-action-v1", "X|review-source-v1", strings.Repeat("a", 128) + reviewSourceVersionSuffix} {
		if IsReviewSourceProtocol(bad) {
			t.Fatal("invalid version accepted", bad)
		}
	}
	var parsed any
	if json.Unmarshal(node.OutputSchema, &parsed) != nil {
		t.Fatal("fixture schema invalid")
	}
}

func TestInitialReviewSourcesSchemaIdentityIsLossless(t *testing.T) {
	for _, mode := range []string{"whitespace", "adjacent_large_const", "duplicate_key"} {
		t.Run(mode, func(t *testing.T) {
			s, f, command := reviewSourceLiveFixture(t)
			node := &f.detail.Run.Compilation.Nodes[0]
			root := decodeObject(node.OutputSchema)
			root["properties"].(map[string]any)["exactNumber"] = map[string]any{"type": "integer", "const": json.Number("9007199254740992")}
			node.OutputSchema = mustJSON(root)
			node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
			c := &f.detail.Run.Compilation
			c.CompilationSHA256 = ""
			encoded, _ := canonicalJSON(*c)
			c.CompilationSHA256 = hashJSON(encoded)
			f.detail.Run.CompilationSHA256 = c.CompilationSHA256
			e := f.executions["review"]
			e.OutputSchemaSHA256 = node.OutputSchemaSHA256
			e.OutputSchema, _ = json.MarshalIndent(root, "", "  ")
			if mode == "adjacent_large_const" {
				e.OutputSchema = raw(strings.Replace(string(e.OutputSchema), "9007199254740992", "9007199254740993", 1))
			}
			if mode == "duplicate_key" {
				e.OutputSchema = raw(strings.Replace(string(e.OutputSchema), `"const": 9007199254740992`, `"const":9007199254740993,"const":9007199254740992`, 1))
			}
			f.executions["review"] = e
			catalog, err := s.StageReviewSources(t.Context(), command)
			if mode == "whitespace" {
				if err != nil || catalog == nil {
					t.Fatal("format-only schema difference rejected", err)
				}
			} else if err == nil || catalog != nil {
				t.Fatal("schema identity drift reached source admission", mode)
			}
		})
	}
}
