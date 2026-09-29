package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"testing"
	"time"
)

type evidenceReaderFixture struct{ value research.Candidate }

func (f evidenceReaderFixture) GetCandidateForTask(_ context.Context, p, c, task string) (research.Candidate, error) {
	if p != "p" || c != "c" || task != "task" {
		return research.Candidate{}, fmt.Errorf("wrong scope")
	}
	return f.value, nil
}
func TestEvidenceRefreshComparesFrozenMaterialNotModelClaims(t *testing.T) {
	s := &RuntimeService{}
	d := RunDetail{Run: Run{ProjectID: "p", ResearchTaskID: "task"}}
	step := Step{Input: raw(`{"importedMaterials":[{"candidateId":"c","attachmentId":"old"},{"materialOrigin":"user_selected","attachmentId":"user"}]}`)}
	node := CompiledNode{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion}
	for _, tc := range []struct {
		status research.ImportStatus
		kind   research.ImportKind
		id     string
		want   int
	}{
		{research.ImportImported, research.ImportFullText, "new", 1},
		{research.ImportImported, research.ImportFullText, "old", 0},
		{research.ImportFailed, research.ImportFullText, "new", 0},
		{research.ImportImported, research.ImportMetadataAbstract, "new", 0},
	} {
		s.evidenceMaterials = evidenceReaderFixture{research.Candidate{ImportStatus: tc.status, ImportKind: tc.kind, AttachmentID: tc.id}}
		got, err := s.changedEvidenceMaterials(context.Background(), d, step, node)
		if err != nil || len(got) != tc.want {
			t.Fatalf("%+v %v", got, err)
		}
	}
	node.PromptVersion = "selected-evidence-v5"
	s.evidenceMaterials = evidenceReaderFixture{research.Candidate{ImportStatus: research.ImportImported, ImportKind: research.ImportFullText, AttachmentID: "new"}}
	got, err := s.changedEvidenceMaterials(context.Background(), d, step, node)
	if err != nil || len(got) != 0 {
		t.Fatal("old contract rewritten")
	}
}

func TestAgentStageMaterialCapabilityCannotBeBroadened(t *testing.T) {
	base := tool.ResearchMaterialDefinition()
	if !agentStageToolSafe(base) {
		t.Fatal("host capability unavailable")
	}
	for _, mutate := range []func(*tool.Definition){
		func(d *tool.Definition) { d.QualifiedName = "builtin.fake.persist" },
		func(d *tool.Definition) { d.Version = "999" },
		func(d *tool.Definition) {
			d.Permissions = append(d.Permissions, tool.PermissionRequirement{Kind: tool.PermissionWorkspaceWrite, Resource: "."})
		},
		func(d *tool.Definition) {
			d.InputSchema = raw(`{"type":"object","properties":{"path":{"type":"string"}}}`)
		},
	} {
		d := tool.ResearchMaterialDefinition()
		mutate(&d)
		if agentStageToolSafe(d) {
			t.Fatal("broadened material capability accepted")
		}
	}
}

type refreshToolFixture struct{ def tool.Definition }

func (f refreshToolFixture) Definition(context.Context) (tool.Definition, error) { return f.def, nil }
func (f refreshToolFixture) Invoke(context.Context, tool.Invocation) (tool.Result, error) {
	panic("must not execute during validation")
}

type refreshRepositoryFixture struct {
	RuntimeRepository
	queued bool
	detail RunDetail
	failed bool
}

func (r *refreshRepositoryFixture) GetRun(context.Context, string, string) (RunDetail, error) {
	return r.detail, nil
}
func (r *refreshRepositoryFixture) FailStep(_ context.Context, _, _ string, _ StepStatus, _ RunStatus, code, _ string, _ time.Time, _ RuntimeEvent) error {
	r.failed = code == "WORKFLOW_EVIDENCE_REFRESH_FAILED"
	return nil
}

func TestEvidenceRefreshFailureIsVisibleUnlessUserStopped(t *testing.T) {
	for _, status := range []RunStatus{RunRunning, RunPaused, RunCancelled} {
		r := &refreshRepositoryFixture{detail: RunDetail{Run: Run{Status: status}}}
		s := &RuntimeService{repository: r, now: time.Now, newID: func() (string, error) { return "failure", nil }}
		if err := s.finishEvidenceRefresh(context.Background(), RunDetail{}, Step{}, fmt.Errorf("fixture failure")); err != nil {
			t.Fatal(err)
		}
		if r.failed != (status == RunRunning) {
			t.Fatalf("status=%s failed=%v", status, r.failed)
		}
	}
}

func (r *refreshRepositoryFixture) QueueEvidenceRefresh(context.Context, string, string, string, int, time.Time, RuntimeEvent) error {
	r.queued = true
	return nil
}
func TestEvidenceRefreshRejectsUntrustedCompilation(t *testing.T) {
	for _, test := range []string{"valid", "kind", "tool", "version", "material-edge", "extra-source"} {
		t.Run(test, func(t *testing.T) {
			registry := tool.NewRegistry()
			repo := &refreshRepositoryFixture{}
			s := &RuntimeService{repository: repo, registry: registry, evidenceMaterials: evidenceReaderFixture{research.Candidate{ImportStatus: research.ImportImported, ImportKind: research.ImportFullText, AttachmentID: "new"}}, now: time.Now, newID: func() (string, error) { return "event", nil }}
			ids := []string{"evidence_import", "evidence_sync", "evidence_search", "evidence_screening"}
			names := []string{"builtin.research.workflow.import", "builtin.research.workflow.sync", "builtin.knowledge.search"}
			d := RunDetail{Run: Run{ID: "run", ProjectID: "p", ResearchTaskID: "task"}}
			for i, id := range ids {
				n := CompiledNode{ID: id, Kind: NodeAgentStage, PromptVersion: selectedEvidenceVersion}
				if i < 3 {
					def := tool.Definition{QualifiedName: names[i], Description: "fixture", Version: "1", Risk: tool.RiskLow, Idempotent: true, InputSchema: raw(`{"type":"object"}`)}
					if err := registry.Register(context.Background(), refreshToolFixture{def}); err != nil {
						t.Fatal(err)
					}
					permissions, _ := json.Marshal(def.Permissions)
					n.Kind = NodeTool
					n.Tool = &ToolSnapshot{QualifiedName: def.QualifiedName, Version: def.Version, Risk: string(def.Risk), Idempotent: def.Idempotent, InputSchema: def.InputSchema, Permissions: permissions}
				}
				d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, n)
				d.Steps = append(d.Steps, Step{ID: id, NodeID: id, Ordinal: i, Status: StepCompleted})
			}
			d.Steps[3].Input = raw(`{"importedMaterials":[{"candidateId":"c","attachmentId":"old"}]}`)
			d.Steps[3].Status = StepRunning
			d.Run.Compilation.Edges = []Edge{{FromNode: ids[0], FromPort: "structured.attachmentIds", ToNode: ids[1], ToPort: "attachmentIds"}, {FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[2], ToPort: "documentIds"}, {FromNode: ids[2], FromPort: "citations", ToNode: ids[3], ToPort: "candidates"}, {FromNode: ids[0], FromPort: "structured.materials", ToNode: ids[3], ToPort: "importedMaterials"}, {FromNode: ids[1], FromPort: "structured.documentIds", ToNode: ids[3], ToPort: "documentIds"}}
			switch test {
			case "kind":
				d.Run.Compilation.Nodes[0].Kind = NodeHumanConfirmation
			case "tool":
				d.Run.Compilation.Nodes[0].Tool.QualifiedName = "builtin.shell"
			case "version":
				d.Run.Compilation.Nodes[0].Tool.Version = "999"
			case "material-edge":
				d.Run.Compilation.Edges[3].FromNode = "foreign"
			case "extra-source":
				d.Run.Compilation.Edges = append(d.Run.Compilation.Edges, d.Run.Compilation.Edges[3])
			}
			handled, err := s.refreshChangedEvidence(context.Background(), d, d.Steps[3], d.Run.Compilation.Nodes[3])
			if test == "valid" {
				if err != nil || !handled || !repo.queued {
					t.Fatal(handled, err)
				}
			} else if err == nil || repo.queued {
				t.Fatal("untrusted rewind", err)
			}
		})
	}
}
