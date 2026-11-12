package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

type revisionEditRepository struct {
	*designIsolationRepository
	execution AIExecution
}

func (r *revisionEditRepository) GetAIExecutionForStep(_ context.Context, step string, attempt int) (AIExecution, bool, error) {
	return r.execution, r.execution.WorkflowStepID == step && r.execution.Attempt == attempt, nil
}

func boundRevisionEditFixture(t *testing.T) (*RuntimeService, *revisionEditRepository, StageMaterialRequest, json.RawMessage) {
	t.Helper()
	_, repo, step, node, base := designIsolationFixture(t, "frozen_design_material")
	node.OutputSchema = raw(`{"type":"object","properties":{"deliverables":{"type":"array"}},"additionalProperties":true}`)
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	var before RunDetail
	_ = json.Unmarshal(repo.snapshot, &before)
	before.Run.Compilation.Nodes = []CompiledNode{node}
	before.Run.Compilation.CompilationSHA256 = ""
	encoded, _ := canonicalJSON(before.Run.Compilation)
	before.Run.CompilationSHA256 = hashBytes(encoded)
	before.Run.Compilation.CompilationSHA256 = before.Run.CompilationSHA256
	repo.snapshot = mustJSON(before)
	repo.detail.Run.Compilation, repo.detail.Run.CompilationSHA256 = before.Run.Compilation, before.Run.CompilationSHA256
	p := repo.detail.RevisionProposals[0]
	p.SnapshotSHA256 = ResearchRevisionSnapshot(before)
	repo.detail.RevisionProposals[0] = p
	repo.detail.Events[0].Payload = mustJSON(map[string]any{"proposal": p, "startOrdinal": step.Ordinal})
	step.Attempt, step.Status, step.WorkflowRunID = 1, StepQueued, repo.detail.Run.ID
	repo.detail.Steps = []Step{step}
	r := &revisionEditRepository{designIsolationRepository: repo}
	s := &RuntimeService{repository: r}
	input := mustJSON(map[string]any{"_userRevision": map[string]any{"priorOutput": map[string]any{"analysis": base}}})
	bound, err := s.bindRevisionEditContext(t.Context(), r.detail, node, input)
	if err != nil {
		t.Fatal(err)
	}
	step.Attempt, step.Status, step.Input, step.InputSHA256 = 2, StepRunning, bound, hashJSON(bound)
	r.detail.Steps[0] = step
	r.execution = AIExecution{WorkflowRunID: r.detail.Run.ID, WorkflowStepID: step.ID, ChatRunID: "chat", Attempt: 2, InputSHA256: step.InputSHA256}
	request := StageMaterialRequest{ProjectID: r.detail.Run.ProjectID, ConversationID: r.detail.Run.ConversationID, WorkflowRunID: r.detail.Run.ID, WorkflowStepID: step.ID, ChatRunID: "chat", Attempt: 2, InputSHA256: step.InputSHA256}
	return s, r, request, base
}

func TestRevisionEditUsesImmutableConfirmedBaseWithoutDuplicateCopy(t *testing.T) {
	s, r, request, want := boundRevisionEditFixture(t)
	before := mustJSON(r.detail)
	snapshot := cloneRaw(r.snapshot)
	base, err := s.StageRevisionEdit(t.Context(), request)
	if err != nil || base == nil || !bytes.Equal(base.Analysis, want) || base.Pointer != "/_userRevision/priorOutput/analysis" || base.SHA256 != hashJSON(want) {
		t.Fatal("did not use the confirmed original", base, err)
	}
	if strings.Count(string(r.detail.Steps[0].Input), "9007199254740993") != 1 || !bytes.Equal(snapshot, r.snapshot) || !bytes.Equal(before, mustJSON(r.detail)) {
		t.Fatal("baseline was duplicated or history changed")
	}
}

func TestRevisionEditRejectsStaleBindingsAndForgedVisibleBases(t *testing.T) {
	for _, name := range []string{"project", "conversation", "run", "step", "chat", "attempt", "hash", "cancel", "input", "compilation", "execution", "source_snapshot", "source_receipt", "visible_base", "visible_number"} {
		t.Run(name, func(t *testing.T) {
			s, r, request, _ := boundRevisionEditFixture(t)
			switch name {
			case "project":
				request.ProjectID = "other"
			case "conversation":
				request.ConversationID = "other"
			case "run":
				request.WorkflowRunID = "other"
			case "step":
				request.WorkflowStepID = "other"
			case "chat":
				request.ChatRunID = "other"
			case "attempt":
				request.Attempt++
			case "hash":
				request.InputSHA256 = "other"
			case "cancel":
				r.detail.Run.CancelRequested = true
			case "input":
				r.detail.Run.Inputs = raw(`{"changed":true}`)
			case "compilation":
				r.detail.Run.Compilation.Nodes[0].Prompt = "forged"
			case "execution":
				r.execution.Attempt++
			case "source_snapshot":
				r.snapshot = raw(`{}`)
			case "source_receipt":
				r.detail.RevisionProposals[0].SnapshotSHA256 = "other"
			case "visible_base", "visible_number":
				input := string(r.detail.Steps[0].Input)
				if name == "visible_base" {
					input = strings.Replace(input, "誊写", "失败候选", 1)
				} else {
					input = strings.Replace(input, "9007199254740993", "9007199254740992", 1)
				}
				r.detail.Steps[0].Input = raw(input)
				r.detail.Steps[0].InputSHA256 = hashJSON(raw(input))
				r.execution.InputSHA256, request.InputSHA256 = r.detail.Steps[0].InputSHA256, r.detail.Steps[0].InputSHA256
			}
			got, err := s.StageRevisionEdit(t.Context(), request)
			var classified *apperr.Error
			if err == nil || got != nil || !errors.As(err, &classified) || classified.Code != revisionWriteBaselineInvalid {
				t.Fatal("forged baseline was accepted or blamed on model", got, err)
			}
		})
	}
}

func TestRevisionEditLegacyInputDoesNotAcquireNewProtocol(t *testing.T) {
	s, r, command, _ := boundRevisionEditFixture(t)
	r.detail.Steps[0].Input = raw(`{}`)
	if got, err := s.StageRevisionEdit(t.Context(), command); err != nil || got != nil {
		t.Fatal("old frozen input upgraded", got, err)
	}
}

func TestReportEditProposalRequiresLatestMatchingConfirmation(t *testing.T) {
	p := ResearchRevisionProposal{ID: "report-proposal", RunID: "run", NodeID: "report_drafting", Status: "confirmed", SnapshotSHA256: "original-snapshot"}
	step := Step{ID: "report", NodeID: "report_drafting", Ordinal: 1}
	d := RunDetail{Run: Run{ID: "run"}, Steps: []Step{step}, RevisionProposals: []ResearchRevisionProposal{p}, Events: []RuntimeEvent{{WorkflowRunID: "run", Type: "workflow.user_revision_queued", Payload: mustJSON(map[string]any{"proposal": p, "startOrdinal": 1})}}}
	got, err := confirmedReportEditProposal(d, step)
	if err != nil || got == nil || got.ID != p.ID {
		t.Fatal("report-only proposal incorrectly requires design isolation", err)
	}
	d.RevisionProposals[0].SnapshotSHA256 = "different"
	if got, err := confirmedReportEditProposal(d, step); err == nil || got != nil {
		t.Fatal("mismatched report confirmation accepted")
	}
	d.RevisionProposals[0] = p
	d.Events = append(d.Events, RuntimeEvent{WorkflowRunID: "run", Type: "workflow.review_revision_queued", Payload: mustJSON(reviewRevision{ProducerNodeID: step.NodeID, ProducerStepID: step.ID})})
	if got, err := confirmedReportEditProposal(d, step); err != nil || got != nil {
		t.Fatal("older user report revived after a successor review", err)
	}
}

func TestRevisionEditReportSeparatesOnlyExactHistoricalHostRecords(t *testing.T) {
	input := raw(`{"reportAssembly":"host-report-assembly-v1","designMaterialAssembly":"design-material-assembly-v1","context":{"status":"research_design_not_empirical_result","deliverables":[{"title":"附录1","markdown":"绊倒"}]}}`)
	// Use the actual version and generator, not headings as ownership proof.
	var fields map[string]any
	_ = json.Unmarshal(input, &fields)
	fields["designMaterialAssembly"] = designMaterialAssemblyVersion
	input = mustJSON(fields)
	parts, err := assembledReportAppendices("# 正文\n不改变方法。", input)
	if err != nil || len(parts) == 0 {
		t.Fatal("fixture needs a real host appendix", err)
	}
	body := "# 正文\n不改变方法。"
	base := mustJSON(map[string]any{"markdown": body + "\n\n" + strings.Join(parts, "\n\n"), "methodSummary": "完整原方法摘要", "n": json.Number("9007199254740993")})
	got, err := revisionEditReportBase(base, input)
	want := mustJSON(map[string]any{"markdown": body, "methodSummary": "完整原方法摘要", "n": json.Number("9007199254740993")})
	if err != nil || !equalRevisionEditBase(got, want) {
		t.Fatal("lost authored body/metadata or retained stale managed records", string(got), err)
	}
	for _, separator := range []string{"\n", "\n\n"} {
		var original map[string]json.RawMessage
		_ = json.Unmarshal(base, &original)
		original["markdown"] = mustJSON(body + separator + strings.Join(parts, "\n\n") + " \n\t")
		projected, err := revisionEditReportBase(mustJSON(original), input)
		if err != nil || !equalRevisionEditBase(projected, want) {
			t.Fatal("accepted historical whitespace prevented exact record separation", err)
		}
	}
	altered := raw(strings.Replace(string(base), "绊倒", "修改过的附录", 1))
	got, err = revisionEditReportBase(altered, input)
	if err != nil || !bytes.Equal(got, altered) {
		t.Fatal("unverified appendix was silently removed", err)
	}
}

func TestRevisionProjectionDropsOnlyExactDuplicateJSON(t *testing.T) {
	analysis := raw(`{"n":9007199254740993,"methodSummary":"原样"}`)
	for _, tc := range []struct {
		text string
		drop bool
	}{
		{string(analysis), true},
		{"```json\n" + string(analysis) + "\n```", true},
		{strings.Replace(string(analysis), "9007199254740993", "9007199254740992", 1), false},
		{`{"n":0,"n":9007199254740993,"methodSummary":"原样"}`, false},
		{"Distinct commentary\n" + string(analysis), false},
	} {
		original := mustJSON(map[string]any{"analysis": analysis, "text": tc.text})
		baseline := mustJSON(map[string]any{"stages": []any{map[string]any{"nodeId": "research_design", "output": original}}})
		var projected struct {
			Stages []struct{ Output map[string]json.RawMessage }
		}
		_ = json.Unmarshal(revisionComparisonProjection(baseline), &projected)
		_, exists := projected.Stages[0].Output["text"]
		if exists == tc.drop || !equalRevisionEditBase(projected.Stages[0].Output["analysis"], analysis) {
			t.Fatal("projection lost distinct evidence or numeric precision", tc.text)
		}
	}
}
