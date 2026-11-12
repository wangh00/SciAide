package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/skillrun"
)

type handoffRepositoryFixture struct {
	RuntimeRepository
	detail     RunDetail
	executions map[string]AIExecution
}

func (f *handoffRepositoryFixture) GetRun(_ context.Context, project, run string) (RunDetail, error) {
	if project != f.detail.Run.ProjectID || run != f.detail.Run.ID {
		return RunDetail{}, fmt.Errorf("foreign workflow")
	}
	return f.detail, nil
}
func (f *handoffRepositoryFixture) GetAIExecutionForStep(_ context.Context, step string, attempt int) (AIExecution, bool, error) {
	v, ok := f.executions[step]
	return v, ok && v.Attempt == attempt, nil
}

type handoffSkillFixture map[string][]skillrun.Snapshot

func (f handoffSkillFixture) ListRunSkillSnapshots(_ context.Context, chat string) ([]skillrun.Snapshot, error) {
	return f[chat], nil
}

func stageSourceHandoffFixture() (*RuntimeService, *handoffRepositoryFixture, StageMaterialRequest, []tool.Call) {
	content := "id,before,after\n一,1,2\n"
	path := "research-inputs/data-" + hashBytes([]byte(content)) + ".csv"
	artifact := tool.ArtifactRef{WorkspacePath: "analysis-output/results.json", SizeBytes: len64(`{"n":1}`), SHA256: hashBytes([]byte(`{"n":1}`))}
	skills := []ResearchSkillSelection{{Name: "statistics", StageIDs: []string{"report_drafting", "independent_review"}, ContentHash: "content-hash", PackageHash: "package-hash"}}
	current := Step{ID: "current", WorkflowRunID: "wf", NodeID: "report_drafting", NodeKind: NodeAgentStage, Ordinal: 4, Attempt: 2, Status: StepRunning, Input: rawObject(map[string]any{"routeContext": map[string]any{"selectedSkills": skills}, "computedResults": map[string]any{"artifacts": []tool.ArtifactRef{artifact}}})}
	current.InputSHA256 = hashJSON(current.Input)
	prior := Step{ID: "prior", WorkflowRunID: "wf", NodeID: "result_interpretation", NodeKind: NodeAgentStage, Ordinal: 3, Attempt: 1, Status: StepCompleted, Input: raw(`{"frozen":"prior"}`), Output: raw(`{"analysis":{"verdict":"DO_NOT_INHERIT_MODEL_CONCLUSION"}}`)}
	prior.InputSHA256 = hashJSON(prior.Input)
	node := CompiledNode{ID: "report_drafting", Kind: NodeAgentStage, SkillRouting: true, StageMaterialsVersion: stageMaterialHandoffVersion, AllowedTools: []ToolSnapshot{{QualifiedName: "builtin.skill.load"}, {QualifiedName: "builtin.workspace.read_text"}}}
	f := &handoffRepositoryFixture{detail: RunDetail{Run: Run{ID: "wf", ProjectID: "p", ResearchTaskID: "task", ConversationID: "conversation", Status: RunRunning, CurrentStep: 4, Inputs: rawObject(map[string]any{"input_paths": []string{path}}), Compilation: Compilation{Nodes: []CompiledNode{node}}}, Steps: []Step{prior, {ID: "kernel", WorkflowRunID: "wf", NodeKind: NodePython, Ordinal: 2, Status: StepCompleted, Output: rawObject(map[string]any{"artifacts": []tool.ArtifactRef{artifact}})}, current}}, executions: map[string]AIExecution{}}
	for _, step := range []Step{prior, current} {
		f.executions[step.ID] = AIExecution{ID: step.ID + "-execution", WorkflowRunID: "wf", WorkflowStepID: step.ID, ChatRunID: step.ID + "-chat", Attempt: step.Attempt, InputSHA256: step.InputSHA256, Status: "completed", OutputText: "DO_NOT_INHERIT_VERDICT"}
	}
	identity := skillrun.Snapshot{Name: "statistics", ContentHash: "content-hash", PackageHash: "package-hash"}
	s := &RuntimeService{repository: f, skills: handoffSkillFixture{"current-chat": {identity}, "prior-chat": {identity}}}
	command := StageMaterialRequest{ProjectID: "p", ConversationID: "conversation", WorkflowRunID: "wf", WorkflowStepID: "current", ChatRunID: "current-chat", Attempt: 2, InputSHA256: current.InputSHA256}
	section := "## Core method\n原方法内容，独立审查仍需自己判断。"
	calls := []tool.Call{
		handoffCall("skill", "builtin.skill.load", raw(`{"name":"statistics","section":"section-2"}`), rawObject(map[string]any{"name": "statistics", "section": "section-2", "mode": "section", "loaded": true, "offset": 0, "nextOffset": utf8.RuneCountInString(section), "total": utf8.RuneCountInString(section), "truncated": false}), "## Skill wrapper\n<skill_instructions>\n"+section+"\n</skill_instructions>\nold capability diagnostics"),
		handoffFileCall("data", path, content), handoffFileCall("result", artifact.WorkspacePath, `{"n":1}`),
	}
	return s, f, command, calls
}

func len64(s string) int64 { return int64(len(s)) }
func handoffCall(id, name string, args, structured json.RawMessage, text string) tool.Call {
	return tool.Call{ID: id, RunID: "prior-chat", SubjectKind: tool.SubjectChatRun, ToolName: name, Arguments: args, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Text: text, Structured: structured}}
}
func handoffFileCall(id, path, content string) tool.Call {
	return handoffCall(id, "builtin.workspace.read_text", rawObject(map[string]any{"path": path}), rawObject(map[string]any{"path": path, "content": content, "bytesRead": len(content), "originalBytes": len(content), "truncated": false}), content)
}
func collectHandoff(t *testing.T, s *RuntimeService, f *handoffRepositoryFixture, command StageMaterialRequest, calls []tool.Call) StageMaterials {
	t.Helper()
	step := *currentStep(f.detail)
	got, err := s.collectStageMaterials(context.Background(), f.detail, step, f.detail.Run.Compilation.Nodes[0], command, func(_ context.Context, kind tool.SubjectKind, chat string) ([]tool.Call, error) {
		if kind != tool.SubjectChatRun || chat != "prior-chat" {
			t.Fatalf("foreign read %s %s", kind, chat)
		}
		return calls, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStageMaterialsTransfersExactSourceNotPriorReasoning(t *testing.T) {
	s, f, command, calls := stageSourceHandoffFixture()
	got := collectHandoff(t, s, f, command, calls)
	if len(got.Sources) != 3 || got.Binding != command {
		t.Fatalf("handoff sources=%+v", got)
	}
	for _, source := range got.Sources {
		if source.SHA256 != hashBytes([]byte(source.Content)) || source.SourceStepID != "prior" || source.SourceAttempt != 1 || source.SourceCallID == "" || strings.Contains(source.Content, "DO_NOT_INHERIT") || strings.Contains(source.Content, "capability diagnostics") {
			t.Fatalf("wrong source: %+v", source)
		}
	}
	// A re-created reader returns the same immutable source packet; no inherited
	// read flags, synthetic tools or mutation of the earlier step is necessary.
	if again := collectHandoff(t, s, f, command, append(calls, calls...)); !reflect.DeepEqual(got, again) {
		t.Fatal("restart/dedup changes the original sources")
	}
	// New independent review gets literal data, never a previous verdict.
	n := &f.detail.Run.Compilation.Nodes[0]
	n.ID = "independent_review"
	n.PromptVersion = focusedReviewVersion
	f.detail.Steps[2].NodeID = "independent_review"
	if review := collectHandoff(t, s, f, command, calls); len(review.Sources) != 3 {
		t.Fatal("review lost original sources")
	}
}

func TestStageMaterialsRejectsForeignOrStaleExecution(t *testing.T) {
	for _, change := range []func(*StageMaterialRequest){
		func(c *StageMaterialRequest) { c.ProjectID = "foreign" }, func(c *StageMaterialRequest) { c.WorkflowRunID = "foreign" }, func(c *StageMaterialRequest) { c.ConversationID = "foreign" }, func(c *StageMaterialRequest) { c.WorkflowStepID = "foreign" }, func(c *StageMaterialRequest) { c.ChatRunID = "foreign" }, func(c *StageMaterialRequest) { c.Attempt-- }, func(c *StageMaterialRequest) { c.InputSHA256 = "foreign" },
	} {
		s, _, command, _ := stageSourceHandoffFixture()
		change(&command)
		if _, err := s.StageMaterials(context.Background(), command); err == nil {
			t.Fatalf("accepted %+v", command)
		}
	}
	s, f, command, _ := stageSourceHandoffFixture()
	f.detail.Run.Compilation.Nodes[0].StageMaterialsVersion = ""
	f.detail.Run.ResearchTaskID = "" // legacy planner/non-task nodes stay untouched.
	if got, err := s.StageMaterials(context.Background(), command); err != nil || len(got.Sources) != 0 {
		t.Fatal("legacy route changed", err)
	}
}

func TestStageMaterialsInvalidatesChangedVersionsAndBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*RuntimeService, *handoffRepositoryFixture, []tool.Call)
		want   int
	}{
		{"failed prior stage", func(_ *RuntimeService, f *handoffRepositoryFixture, _ []tool.Call) {
			f.detail.Steps[0].Status = StepFailed
		}, 0},
		{"old attempt", func(_ *RuntimeService, f *handoffRepositoryFixture, _ []tool.Call) { f.detail.Steps[0].Attempt++ }, 0},
		{"changed prior input", func(_ *RuntimeService, f *handoffRepositoryFixture, _ []tool.Call) {
			f.detail.Steps[0].Input = raw(`{"different":true}`)
		}, 0},
		{"other workflow", func(_ *RuntimeService, f *handoffRepositoryFixture, _ []tool.Call) {
			f.detail.Steps[0].WorkflowRunID = "foreign"
		}, 0},
		{"changed skill", func(s *RuntimeService, _ *handoffRepositoryFixture, _ []tool.Call) {
			s.skills.(handoffSkillFixture)["current-chat"][0].PackageHash = "changed"
		}, 2},
		{"changed data", func(_ *RuntimeService, _ *handoffRepositoryFixture, c []tool.Call) {
			c[1] = handoffFileCall("data", "research-inputs/other.csv", "changed")
		}, 2},
		{"removed current artifact", func(_ *RuntimeService, f *handoffRepositoryFixture, _ []tool.Call) {
			f.detail.Steps[2].Input = raw(`{}`)
		}, 1},
		{"failed source read", func(_ *RuntimeService, _ *handoffRepositoryFixture, c []tool.Call) { c[0].Status = tool.CallFailed }, 2},
		{"partial source read", func(_ *RuntimeService, _ *handoffRepositoryFixture, c []tool.Call) { c[1].Result.Truncated = true }, 2},
		{"foreign chat call", func(_ *RuntimeService, _ *handoffRepositoryFixture, c []tool.Call) { c[0].RunID = "other" }, 2},
		{"revoked source tools", func(_ *RuntimeService, f *handoffRepositoryFixture, _ []tool.Call) {
			f.detail.Run.Compilation.Nodes[0].AllowedTools = nil
		}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f, command, calls := stageSourceHandoffFixture()
			test.change(s, f, calls)
			got := collectHandoff(t, s, f, command, calls)
			if len(got.Sources) != test.want {
				t.Fatalf("got %d sources, want %d", len(got.Sources), test.want)
			}
		})
	}
}

func TestStageMaterialsUnwrapsOriginalResourceToolWithoutActionMenu(t *testing.T) {
	s, f, command, calls := stageSourceHandoffFixture()
	for i := range calls {
		call := &calls[i]
		call.Result.Structured = rawObject(map[string]any{"label": "original action", "sourceTool": call.ToolName, "sourceArguments": call.Arguments, "sourceResult": call.Result.Structured, "actions": []string{"DO_NOT_INHERIT_OLD_ACTION_ID"}})
		call.Result.Text = "original action\n\n" + call.Result.Text
		call.ToolName = "builtin.resource.open"
		call.Arguments = raw(`{"actionId":"old-scoped-action"}`)
	}
	if got := collectHandoff(t, s, f, command, calls); len(got.Sources) != 3 {
		t.Fatalf("resource source not preserved: %+v", got)
	}
	for _, path := range []string{"../other.csv", ".sciaide/db", "D:/foreign.csv", "research-inputs/../private.txt", "/absolute/file"} {
		if validStageMaterialPath(path) {
			t.Fatalf("unsafe path %s", path)
		}
	}
}

func TestStageMaterialsVersionIsFrozenAndLegacyJSONUnchanged(t *testing.T) {
	base := Node{ID: "stage", Name: "stage", Kind: NodeAgentStage, Arguments: raw(`{}`), Prompt: "Inspect source", PromptVersion: "original-v1", OutputSchema: raw(`{"type":"object"}`), ReviewPolicy: AIReviewAuto}
	compiler := NewCompiler(fixtureRegistry(t, "1"))
	definition := Definition{SchemaVersion: SchemaVersion, Name: "stage materials", Nodes: []Node{base}}
	legacy, err := compiler.Compile(context.Background(), definition)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(legacy)
	if strings.Contains(string(encoded), "stageMaterialsVersion") {
		t.Fatal("new zero field changed legacy snapshot JSON")
	}
	definition.Nodes[0].StageMaterialsVersion = stageMaterialHandoffVersion
	current, err := compiler.Compile(context.Background(), definition)
	if err != nil || current.Nodes[0].StageMaterialsVersion != stageMaterialHandoffVersion || current.Nodes[0].PromptVersion != legacy.Nodes[0].PromptVersion || current.CompilationSHA256 == legacy.CompilationSHA256 {
		t.Fatal("new policy missing from immutable contract", err)
	}
	definition.Nodes[0].StageMaterialsVersion = "unknown"
	assertDiagnostic(t, compiler.Preview(context.Background(), definition), "invalid_stage_materials")
}

func TestStageMaterialsUnavailableOptionalSourceDoesNotFailResearch(t *testing.T) {
	s, f, command, _ := stageSourceHandoffFixture()
	step := *currentStep(f.detail)
	reader := func(context.Context, tool.SubjectKind, string) ([]tool.Call, error) {
		return nil, fmt.Errorf("historical source unavailable")
	}
	got, err := s.collectStageMaterials(context.Background(), f.detail, step, f.detail.Run.Compilation.Nodes[0], command, reader)
	if err != nil || len(got.Sources) != 0 {
		t.Fatal("optional old read stopped current research", err)
	}
	node := f.detail.Run.Compilation.Nodes[0]
	node.AllowedTools = nil
	_, err = s.collectStageMaterials(context.Background(), f.detail, step, node, command, func(context.Context, tool.SubjectKind, string) ([]tool.Call, error) {
		t.Fatal("out-of-scope history scanned")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
