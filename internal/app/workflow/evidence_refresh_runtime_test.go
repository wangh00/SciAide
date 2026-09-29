package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type evidenceTerminalRepository struct {
	refreshRepositoryFixture
	execution AIExecution
}

func (r *evidenceTerminalRepository) GetAIExecutionForStep(context.Context, string, int) (AIExecution, bool, error) {
	return r.execution, true, nil
}

type evidenceTerminalExecutor struct {
	AIStageExecutor
	status string
}

func (e evidenceTerminalExecutor) Get(context.Context, AIExecution) (AIStageState, error) {
	return AIStageState{Status: e.status, Text: "This stale summary must not be committed"}, nil
}

// Exercise the driver handoff, not just the SQL operation: a completed or
// schema-failed old summary cannot escape into the report after full text saves.
func TestDriveAIRefreshesSavedFullTextBeforeCommittingStaleSummary(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			d := revisionEvidenceFixture(t)
			d.Run.ID = "run"
			d.Run.ProjectID = "p"
			d.Run.ResearchTaskID = "task"
			d.Run.Status = RunRunning
			d.Run.Inputs = raw(`{}`)
			d.Run.InputsSHA256 = hashJSON(d.Run.Inputs)
			registry := tool.NewRegistry()
			for i := range d.Run.Compilation.Nodes {
				n := &d.Run.Compilation.Nodes[i]
				if n.Tool != nil {
					def := tool.Definition{QualifiedName: n.Tool.QualifiedName, Version: "1", Description: "fixture", Risk: tool.RiskLow, Idempotent: true, InputSchema: raw(`{"type":"object"}`)}
					if err := registry.Register(context.Background(), refreshToolFixture{def}); err != nil {
						t.Fatal(err)
					}
					permissions, _ := json.Marshal(def.Permissions)
					n.Tool = &ToolSnapshot{QualifiedName: def.QualifiedName, Version: def.Version, Risk: string(def.Risk), Idempotent: true, InputSchema: def.InputSchema, Permissions: permissions}
				}
				if n.ID == "evidence_screening" {
					n.OutputSchema = selectedEvidenceSchema()
					n.OutputSchemaSHA256 = hashJSON(n.OutputSchema)
				}
			}
			d.Run.Compilation.CompilationSHA256 = ""
			encoded, err := canonicalJSON(d.Run.Compilation)
			if err != nil {
				t.Fatal(err)
			}
			d.Run.CompilationSHA256 = hashBytes(encoded)
			d.Run.Compilation.CompilationSHA256 = d.Run.CompilationSHA256
			step := *findStepByNode(d.Steps, "evidence_screening")
			step.Status = StepRunning
			step.Input = raw(`{"importedMaterials":[{"candidateId":"c","attachmentId":"old"}],"_selectedEvidence":{"direct":true,"phase":"coverage","documents":["doc"]}}`)
			repo := &evidenceTerminalRepository{execution: AIExecution{ID: "execution", Status: "running", ChatRunID: "chat"}}
			s := &RuntimeService{repository: repo, projects: fixedProjectLoader{project.Project{ID: "p", WorkspacePath: t.TempDir()}}, registry: registry, evidenceMaterials: evidenceReaderFixture{research.Candidate{ImportStatus: research.ImportImported, ImportKind: research.ImportFullText, AttachmentID: "new"}}, ai: evidenceTerminalExecutor{status: status}, now: time.Now, newID: func() (string, error) { return "event", nil }}
			if err := s.driveAI(context.Background(), d, step, compilationNodeMap(d.Run.Compilation)[step.NodeID]); err != nil {
				t.Fatal(err)
			}
			if !repo.queued {
				t.Fatal("saved full text did not invalidate old synthesis")
			}
		})
	}
}
