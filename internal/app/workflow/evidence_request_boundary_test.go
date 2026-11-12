package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/apperr"
)

func TestEvidenceRequestBoundaryOnlyStopsBoundObsoleteMaterial(t *testing.T) {
	for _, mode := range []string{"changed", "unchanged", "failed_download", "foreign_chat", "foreign_digest", "old_contract", "paused"} {
		t.Run(mode, func(t *testing.T) {
			d := RunDetail{Run: Run{ID: "run", ProjectID: "p", ResearchTaskID: "task", Status: RunRunning, CurrentStep: 0, Compilation: Compilation{Nodes: []CompiledNode{{ID: "evidence_screening", PromptVersion: selectedEvidenceVersion}}}}, Steps: []Step{{ID: "step", NodeID: "evidence_screening", Status: StepRunning, Attempt: 1, InputSHA256: "digest", Input: raw(`{"importedMaterials":[{"candidateId":"c","attachmentId":"old"}]}`)}}, AIExecutions: []AIExecution{{ChatRunID: "chat", WorkflowStepID: "step", Attempt: 1, InputSHA256: "digest"}}}
			c := research.Candidate{ImportStatus: research.ImportImported, ImportKind: research.ImportFullText, AttachmentID: "new"}
			switch mode {
			case "unchanged":
				c.AttachmentID = "old"
			case "failed_download":
				c.ImportStatus = research.ImportFailed
			case "foreign_chat":
				d.AIExecutions[0].ChatRunID = "other"
			case "foreign_digest":
				d.Steps[0].InputSHA256 = "other"
			case "old_contract":
				d.Run.Compilation.Nodes[0].PromptVersion = "selected-evidence-v5"
			case "paused":
				d.Run.Status = RunPaused
			}
			repo := &refreshRepositoryFixture{detail: d}
			s := &RuntimeService{repository: repo, evidenceMaterials: evidenceReaderFixture{c}}
			err := s.CheckAIRequestEvidence(context.Background(), "p", "run", "step", "chat", "digest")
			if mode == "changed" {
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Code != "WORKFLOW_EVIDENCE_UPDATED" {
					t.Fatal("obsolete request not stopped", err)
				}
			} else if err != nil {
				t.Fatal("unrelated/current request blocked", err)
			}
			if repo.queued || repo.failed || string(repo.detail.Steps[0].Input) != string(d.Steps[0].Input) {
				t.Fatal("preflight mutated workflow instead of using its owner")
			}
		})
	}
}
