package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func implementationEnvironmentFixture() (RunDetail, Step, CompiledNode) {
	environment := raw(`{"status":"success","structured":{"projectId":"p","state":"ready","baseExecutableVersion":"3.12.3","environmentFingerprint":"recorded-env","lock":["numpy==2.5.3","scipy==1.18.1","matplotlib==3.11.1"]}}`)
	node := CompiledNode{ID: "method_implementation", Kind: NodeAgentStage, PromptVersion: dynamicImplementationPromptVersion, OutputSchema: dynamicImplementationSchema(), AllowedTools: []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}}
	step := Step{ID: "implementation", WorkflowRunID: "run", NodeID: node.ID, Ordinal: 4, Attempt: 1, Input: raw(`{"methodContext":{"method":"frozen custom analysis"}}`)}
	step.InputSHA256 = hashJSON(step.Input)
	detail := RunDetail{Run: Run{ID: "run", ProjectID: "p", Compilation: Compilation{Nodes: []CompiledNode{
		{ID: "data_environment", Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.python.ensure"}},
		{ID: "dependency_preparation", Tool: &ToolSnapshot{QualifiedName: "builtin.research.workflow.python.prepare"}},
		node,
	}}}, Steps: []Step{{ID: "env", WorkflowRunID: "run", NodeID: "data_environment", Ordinal: 1, Attempt: 1, Status: StepCompleted, Output: environment}, step}}
	return detail, step, node
}

func TestImplementationEnvironmentUsesSuccessfulPriorHostRecordOnly(t *testing.T) {
	detail, step, node := implementationEnvironmentFixture()
	before := string(mustJSON(detail))
	prompt := buildAIStagePrompt(detail, step, node)
	for _, text := range []string{implementationEfficiencyInstruction, "scipy==1.18.1", `"sourceStepId":"env"`, "<stage_input>\n" + string(step.Input) + "\n</stage_input>", "there is no tool-count or reading quota", "paging with offset until the relevant code or parameters are complete"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("missing guidance %s", text)
		}
	}
	if string(mustJSON(detail)) != before {
		t.Fatal("host navigation changed the frozen route/input/environment")
	}
	for _, tc := range []struct {
		name string
		edit func(*RunDetail)
	}{
		{"pending", func(d *RunDetail) { d.Steps[0].Status = StepQueued }},
		{"failed", func(d *RunDetail) { d.Steps[0].Status = StepFailed }},
		{"future", func(d *RunDetail) { d.Steps[0].Ordinal = 5 }},
		{"other run", func(d *RunDetail) { d.Steps[0].WorkflowRunID = "another-run" }},
		{"not environment tool", func(d *RunDetail) { d.Run.Compilation.Nodes[0].Tool.QualifiedName = "builtin.workspace.read_text" }},
		{"no tool identity", func(d *RunDetail) { d.Run.Compilation.Nodes[0].Tool = nil }},
		{"other project", func(d *RunDetail) {
			d.Steps[0].Output = raw(`{"status":"success","structured":{"state":"ready","projectId":"other","environmentFingerprint":"hash","lock":["secret-pkg==1"]}}`)
		}},
		{"missing lock", func(d *RunDetail) {
			d.Steps[0].Output = raw(`{"status":"success","structured":{"state":"ready","projectId":"p","environmentFingerprint":"hash"}}`)
		}},
		{"invalid JSON", func(d *RunDetail) { d.Steps[0].Output = raw(`{"status":`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, s, n := implementationEnvironmentFixture()
			tc.edit(&d)
			got := implementationEnvironmentInstruction(d, s, n)
			if got != implementationEfficiencyInstruction {
				t.Fatal("unverified/foreign/future package record advertised", got)
			}
		})
	}
}

func TestImplementationEnvironmentPrefersLatestPriorSnapshotWithoutInventingPackages(t *testing.T) {
	detail, step, node := implementationEnvironmentFixture()
	newer := detail.Steps[0]
	newer.ID, newer.NodeID, newer.Ordinal = "prepared", "dependency_preparation", 3
	newer.Output = raw(`{"status":"success","structured":{"projectId":"p","state":"ready","baseExecutableVersion":"3.12.4","environmentFingerprint":"new-env","lock":[]}}`)
	detail.Steps = append(detail.Steps, newer)
	text := implementationEnvironmentInstruction(detail, step, node)
	if !strings.Contains(text, `"sourceStepId":"prepared"`) || !strings.Contains(text, `"lock":[]`) || strings.Contains(text, "scipy==") {
		t.Fatal(text)
	}
	newer.Output = raw(`{}`)
	detail.Steps[len(detail.Steps)-1] = newer
	if got := implementationEnvironmentInstruction(detail, step, node); got != implementationEfficiencyInstruction {
		t.Fatal("fell back to a stale environment after incomplete newer record")
	}
	node.ID = "research_design"
	if implementationEnvironmentInstruction(detail, step, node) != "" {
		t.Fatal("implementation guidance leaked into design-only route")
	}
}

func TestExecutionEfficiencyVersionIsIdempotentWithOtherHostGuidance(t *testing.T) {
	detail, step, node := implementationEnvironmentFixture()
	prompt := buildAIStagePrompt(detail, step, node)
	version := reportExecutionPromptVersion(node, prompt)
	if version != node.PromptVersion+"-impl-parts-v1-"+implementationEfficiencyVersion+"-"+computationAuditVersion {
		t.Fatal(version)
	}
	node.PromptVersion = version
	if reportExecutionPromptVersion(node, prompt) != version {
		t.Fatal("duplicated implementation marker")
	}
	node = CompiledNode{ID: "independent_review", PromptVersion: "review-v1", OutputSchema: trackedResearchReviewSchema()}
	prompt = reviewScopeInstruction + implementationDecisionInstruction + kernelReviewNavigationInstruction
	version = reportExecutionPromptVersion(node, prompt)
	node.PromptVersion = version
	if reportExecutionPromptVersion(node, prompt) != version || !strings.HasSuffix(version, reviewKernelPathVersion) {
		t.Fatal("duplicated a non-final host marker", version)
	}
	// A node without implementation guidance must keep the old prompt identity.
	node = CompiledNode{ID: "other", PromptVersion: "unchanged", OutputSchema: json.RawMessage(`{}`)}
	if reportExecutionPromptVersion(node, implementationEfficiencyInstruction) != node.PromptVersion {
		t.Fatal("unrelated stage version changed")
	}
}

func TestLargeEnvironmentInventoryKeepsFullRecordWithoutInflatingPrompt(t *testing.T) {
	detail, step, node := implementationEnvironmentFixture()
	lock := make([]string, 512)
	for i := range lock {
		lock[i] = strings.Repeat("package", 30) + "==1.0.0"
	}
	output := decodeObject(detail.Steps[0].Output)
	output["structured"].(map[string]any)["lock"] = lock
	detail.Steps[0].Output = mustJSON(output)
	before := string(mustJSON(detail))
	got := implementationEnvironmentInstruction(detail, step, node)
	start := strings.Index(got, "<recorded_python_environment>\n")
	data := strings.Split(strings.TrimPrefix(got[start:], "<recorded_python_environment>\n"), "\n</recorded_python_environment>")[0]
	var inventory implementationEnvironmentInventory
	if json.Unmarshal([]byte(data), &inventory) != nil || inventory.LockComplete || inventory.LockPackageCount != 512 || len(inventory.Lock) == 0 || len(data) > 10000 || inventory.SourceLockPointer != "/structured/lock" {
		t.Fatal("large lock is not a clearly bounded navigation preview", inventory)
	}
	if string(mustJSON(detail)) != before || !strings.Contains(got, "an omitted package is not proven absent") {
		t.Fatal("full environment record lost or preview claimed complete")
	}
}

func TestExecutionGuidanceVersionFitsPersistedIdentity(t *testing.T) {
	prompt := reviewScopeInstruction + implementationDecisionInstruction + implementationEfficiencyInstruction + kernelReviewNavigationInstruction
	for _, id := range []string{"independent_review", "method_implementation"} {
		node := CompiledNode{ID: id, PromptVersion: strings.Repeat("r", 64), OutputSchema: trackedResearchReviewSchema()}
		before := string(mustJSON(node))
		version := reportExecutionPromptVersion(node, prompt)
		if len(version) > 128 || !strings.HasPrefix(version, "p-") || !strings.HasSuffix(version, "-"+reviewKernelPathVersion) {
			t.Fatal("derived identity cannot be stored", version)
		}
		for _, marker := range []string{reviewScopeVersion, implementationDecisionVersion, reviewKernelPathVersion} {
			if strings.Count(version, "-"+marker) != 1 {
				t.Fatal("host marker lost or repeated", version, marker)
			}
		}
		if id == "method_implementation" && strings.Count(version, "-"+implementationEfficiencyVersion) != 1 {
			t.Fatal("implementation marker lost", version)
		}
		if string(mustJSON(node)) != before || reportExecutionPromptVersion(node, prompt) != version {
			t.Fatal("frozen node mutated or identity not deterministic")
		}
		other := node
		other.PromptVersion = strings.Repeat("s", 64)
		if reportExecutionPromptVersion(other, prompt) == version {
			t.Fatal("distinct frozen identities collapsed")
		}
		node.PromptVersion = version
		if reportExecutionPromptVersion(node, prompt) != version {
			t.Fatal("compacted identity is not idempotent")
		}
	}
	// An existing legal execution identity at the exact persisted boundary
	// stays byte-for-byte unchanged when no further guidance is enabled.
	node := CompiledNode{ID: "other", PromptVersion: strings.Repeat("x", 128)}
	if reportExecutionPromptVersion(node, "") != node.PromptVersion {
		t.Fatal("legal existing identity changed")
	}
	// Normal current route identities remain readable, without compaction.
	node = CompiledNode{ID: "independent_review", PromptVersion: routeDeliveryReviewVersion + "-" + focusedReviewVersion, OutputSchema: trackedResearchReviewSchema()}
	if version := reportExecutionPromptVersion(node, prompt); len(version) > 128 || !strings.HasPrefix(version, node.PromptVersion+"-") {
		t.Fatal("ordinary current route identity changed", version)
	}
}

func TestImplementationRepairGuidanceFitsPersistedIdentityWithoutChangingFrozenRoute(t *testing.T) {
	producer := Step{ID: "producer", NodeID: "method_implementation", Status: StepQueued, Attempt: 0}
	detail := RunDetail{Steps: []Step{producer}, Events: []RuntimeEvent{{Type: "workflow.upstream_revision_queued", Payload: raw(`{"producerStepId":"producer","producerNodeId":"method_implementation","automatic":true,"nextProducerAttempt":1,"priorImplementation":{"code":"result = missing\nresult"},"failureSummary":"KeyError"}`)}}}
	for _, baseVersion := range []string{dynamicImplementationPromptVersion, strings.Repeat("r", 64)} {
		node := CompiledNode{ID: "method_implementation", PromptVersion: baseVersion, OutputSchema: dynamicImplementationSchema()}
		prompt := buildAIStagePrompt(detail, Step{Input: raw(`{"_pythonRepair":{"priorImplementation":{"code":"result = missing\nresult"},"failureSummary":"KeyError"}}`)}, node)
		before := string(mustJSON(node))
		version := reportExecutionPromptVersion(node, prompt)
		if len(version) > 128 || string(mustJSON(node)) != before {
			t.Fatal("repair identity cannot be stored or frozen node changed", version)
		}
		for _, marker := range []string{"impl-parts-v1", "impl-patch-v1", implementationEfficiencyVersion} {
			if strings.Count(version, "-"+marker) != 1 {
				t.Fatal("repair host marker missing or duplicated", version, marker)
			}
		}
		node.PromptVersion = version
		if reportExecutionPromptVersion(node, prompt) != version {
			t.Fatal("repair identity not idempotent", version)
		}
	}
}
