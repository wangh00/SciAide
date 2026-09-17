package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestDefinitionSnapshotRejectsChangedWorkspaceReadSchema(t *testing.T) {
	old := tool.Definition{
		QualifiedName: "builtin.workspace.read_text",
		Version:       "1",
		Risk:          tool.RiskLow,
		Idempotent:    true,
		Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","required":["path","content","bytesRead","originalBytes","truncated"]}`),
	}
	current := old
	current.InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"offset":{"type":"integer","minimum":0,"maximum":67108864},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`)
	if definitionSnapshotEqual(old, current) {
		t.Fatal("changed workspace read schema was accepted without recompiling the Workflow")
	}
}

func TestDefinitionSnapshotRejectsUnsafeWorkspaceSchemaChanges(t *testing.T) {
	old := tool.Definition{
		QualifiedName: "builtin.workspace.read_text",
		Version:       "1",
		Risk:          tool.RiskLow,
		Idempotent:    true,
		InputSchema:   json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object"}`),
	}
	addedRequired := old
	addedRequired.InputSchema = json.RawMessage(`{"type":"object","required":["path","offset"],"properties":{"path":{"type":"string"},"offset":{"type":"integer"}}}`)
	if definitionSnapshotEqual(old, addedRequired) {
		t.Fatal("a newly required workspace field was accepted for an old snapshot")
	}
	addedUnknown := old
	addedUnknown.InputSchema = json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"encoding":{"type":"string"}}}`)
	if definitionSnapshotEqual(old, addedUnknown) {
		t.Fatal("an unapproved workspace field was accepted for an old snapshot")
	}
	addedTopLevel := old
	addedTopLevel.InputSchema = json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}},"deprecated":true}`)
	if definitionSnapshotEqual(old, addedTopLevel) {
		t.Fatal("an unapproved workspace schema keyword was accepted for an old snapshot")
	}
	nonWorkspace := old
	nonWorkspace.QualifiedName = "builtin.other"
	if definitionSnapshotEqual(old, nonWorkspace) {
		t.Fatal("a different tool name was accepted as a compatible snapshot")
	}
}

func TestTrustedWorkspaceFilesInstructionUsesOnlyHostPaths(t *testing.T) {
	detail := RunDetail{
		Run:   Run{Inputs: json.RawMessage(`{"input_paths":["research-inputs/data-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.csv"]}`)},
		Steps: []Step{{Status: StepCompleted, Output: json.RawMessage(`{"artifacts":[{"workspacePath":"analysis-output/results.json"}]}`)}},
	}
	prompt := buildAIStagePrompt(detail, Step{Input: json.RawMessage(`{}`)}, CompiledNode{ID: "result_interpretation", Prompt: "interpret", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if !strings.Contains(prompt, "research-inputs/data-") || !strings.Contains(prompt, "analysis-output/results.json") {
		t.Fatalf("trusted workspace inventory missing host paths: %s", prompt)
	}
	if !strings.Contains(prompt, "Do not guess") || !strings.Contains(prompt, "required file is absent") {
		t.Fatalf("trusted workspace inventory lacks recovery rules: %s", prompt)
	}
}
