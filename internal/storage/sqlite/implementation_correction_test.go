package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/model"
)

func correctionFixture(v chat.ImplementationDraft, n int) chat.ImplementationDraft {
	v.PartCorrectionCount = n
	v.PartValidationError = "$.metadata.dependencies: required property is missing"
	v.PartRejectedCandidate = `{"metadata":{"methodSummary":"unsaved"},"code":"MUST_NOT_EXECUTE"}`
	v.PartControlName = "submit_implementation_part"
	return v
}

func TestImplementationCorrectionCheckpointSurvivesCancellationAndDiskReopen(t *testing.T) {
	for _, withPrefix := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_metadata", true: "next_source"}[withPrefix], func(t *testing.T) {
			f := newImplementationStorageFixture(t)
			ctx := context.Background()
			v := f.base
			v.SourceEvidence = []model.CompletedToolEvidence{{SourceRunID: v.RunID, CallID: "read", ToolName: "builtin.workspace.read_text", Arguments: json.RawMessage(`{"path":"materials.txt"}`), Result: "READ-ONCE-MATERIALS"}}
			if withPrefix {
				v.SourceTurnIndex = f.completedTurn(t, "valid prefix")
				if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
					t.Fatal(err)
				}
			} else {
				v.PartCount, v.Code, v.Metadata = 0, "", json.RawMessage(`{}`)
			}
			v = correctionFixture(v, 1)
			v.SourceTurnIndex = f.completedTurn(t, "invalid unsaved submission")
			if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
				t.Fatal(err)
			}
			f.finishAttempt(t, "cancelled", "RUN_CANCELLED")
			f.createAttempt(t, 11)
			loaded, found, err := f.repo.ImplementationDraft(ctx, f.base.RunID)
			if err != nil || !found || loaded.PartCorrectionCount != 1 || loaded.Code != v.Code || !sameImplementationEvidence(loaded, v) {
				t.Fatal(loaded, found, err)
			}
			if err := f.repo.SaveImplementationDraft(ctx, loaded); err != nil {
				t.Fatal("failed adoption", err)
			}
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db, err = OpenExisting(ctx, f.path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer f.db.Close()
			f.repo = NewRunRepository(f.db)
			loaded, found, err = f.repo.ImplementationDraft(ctx, f.base.RunID)
			if err != nil || !found || loaded.PartCorrectionCount != 1 || !strings.Contains(loaded.PartValidationError, "dependencies") {
				t.Fatal(loaded, found, err)
			}
			loaded = correctionFixture(loaded, 2)
			loaded.SourceTurnIndex = f.completedTurn(t, "another invalid submission")
			if err := f.repo.SaveImplementationDraft(ctx, loaded); err != nil {
				t.Fatal(err)
			}
			loaded.PartCorrectionCount, loaded.PartValidationError, loaded.PartRejectedCandidate, loaded.PartControlName = 0, "", "", ""
			loaded.PartCount++
			loaded.Metadata = f.base.Metadata
			loaded.Code += "result = {'ok': True}\nresult"
			loaded.Complete = true
			loaded.SourceTurnIndex = f.completedTurn(t, "corrected source")
			if err := f.repo.SaveImplementationDraft(ctx, loaded); err != nil {
				t.Fatal("corrected part did not advance", err)
			}
			if strings.Contains(loaded.Code, "MUST_NOT_EXECUTE") {
				t.Fatal("invalid candidate joined source")
			}
		})
	}
}

func TestImplementationCorrectionTransitionCannotRewriteOrResetBudget(t *testing.T) {
	for _, mode := range []string{"reset_budget", "rewrite_source", "replace_metadata", "lost_evidence", "same_response", "too_many", "patch_without_base"} {
		t.Run(mode, func(t *testing.T) {
			f := newImplementationStorageFixture(t)
			ctx := context.Background()
			v := f.base
			v.SourceTurnIndex = f.completedTurn(t, "valid prefix")
			if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
				t.Fatal(err)
			}
			v = correctionFixture(v, 1)
			v.SourceTurnIndex = f.completedTurn(t, "invalid part")
			if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
				t.Fatal(err)
			}
			v = correctionFixture(v, 2)
			if mode != "same_response" {
				v.SourceTurnIndex = f.completedTurn(t, "next response")
			}
			switch mode {
			case "reset_budget":
				v.PartCorrectionCount = 1
			case "rewrite_source":
				v.Code = "changed = True\n"
			case "replace_metadata":
				v.Metadata = json.RawMessage(`{"methodSummary":"changed"}`)
			case "lost_evidence":
				v.SourceEvidence = []model.CompletedToolEvidence{{Result: "invented"}}
			case "too_many":
				v.PartCorrectionCount = 3
			case "patch_without_base":
				v.PartCorrectionCount, v.PartValidationError, v.PartRejectedCandidate, v.PartControlName = 0, "", "", ""
				v.SourcePatch = &chat.ImplementationSourcePatch{BaseCodeSHA256: strings.Repeat("0", 64)}
				v.Complete = true
			}
			if err := f.repo.SaveImplementationDraft(ctx, v); err == nil {
				t.Fatal("invalid correction transition saved")
			}
		})
	}
}

func TestImplementationAtomicPatchCheckpointRecomputesFromHostFrozenInput(t *testing.T) {
	f := newImplementationStorageFixture(t)
	ctx := context.Background()
	input := json.RawMessage(`{"_pythonRepair":{"priorImplementation":{"methodSummary":"same agreed method","dependencies":["numpy"],"analysisInput":{"seed":42},"code":"result = missing\nresult","researchChanges":[]}}}`)
	digest := chat.ImplementationCodeSHA256(string(input))
	if _, err := f.db.Exec(`UPDATE workflow_steps SET input_json=?,input_sha256=? WHERE id='step'`, string(input), digest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE workflow_ai_executions SET input_sha256=?,prompt_version=prompt_version||'-impl-patch-v1' WHERE id=?`, digest, f.base.ExecutionID); err != nil {
		t.Fatal(err)
	}
	c, found, err := f.repo.WorkflowAIContract(ctx, f.base.RunID)
	if err != nil || !found || string(c.Input) != string(input) {
		t.Fatal(c, found, err)
	}
	base, found, err := chat.FrozenImplementationPatchBase(c)
	if err != nil || !found {
		t.Fatal(err)
	}
	v := f.base
	v.InputSHA256 = digest
	v.Metadata = base.Metadata
	v.SourcePatch = &chat.ImplementationSourcePatch{BaseCodeSHA256: chat.ImplementationCodeSHA256(base.Code), Replacements: []chat.ImplementationReplacement{{Old: "missing", New: "1"}}}
	v.Code, err = chat.ApplyImplementationSourcePatch(base.Code, *v.SourcePatch)
	if err != nil {
		t.Fatal(err)
	}
	v.Complete = true
	v.SourceTurnIndex = f.completedTurn(t, "completed source patch")
	if err := f.repo.SaveImplementationDraft(ctx, v); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := f.repo.ImplementationDraft(ctx, v.RunID)
	if err != nil || !found || loaded.SourcePatch == nil || loaded.Code != "result = 1\nresult" {
		t.Fatal(loaded, found, err)
	}
	v.Code = "result = 2\nresult"
	v.SourceTurnIndex = f.completedTurn(t, "changed payload")
	if err := f.repo.SaveImplementationDraft(ctx, v); err == nil {
		t.Fatal("source not derived from recorded patch accepted")
	}
	if _, err := f.db.Exec(`UPDATE workflow_steps SET input_json='{}' WHERE id='step'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repo.WorkflowAIContract(ctx, v.RunID); err == nil {
		t.Fatal("changed frozen source input was trusted")
	}
}
