package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/apperr"
)

// No model, network or production database. The complete confirmed snapshot
// intentionally has no priorOutput preview; that preview is not authority.
type designIsolationRepository struct {
	submissionRuntimeRepository
	detail         RunDetail
	snapshot       json.RawMessage
	readErr        error
	adopted        json.RawMessage
	reviewSnapshot bool
}

func (r *designIsolationRepository) HasDesignRevisionWriteSnapshot(_ context.Context, run, event string) (bool, error) {
	return r.reviewSnapshot && run == r.detail.Run.ID && event == "review-revision", r.readErr
}

type designIsolationCitationCalls struct{ handoffCitationCalls }

func (r *designIsolationCitationCalls) ListByRun(context.Context, string) ([]tool.Call, error) {
	return nil, nil
}

func (r *designIsolationRepository) GetRun(context.Context, string, string) (RunDetail, error) {
	return r.detail, nil
}
func (r *designIsolationRepository) ProjectIDForWorkflowRun(context.Context, string) (string, error) {
	return r.detail.Run.ProjectID, nil
}
func (r *designIsolationRepository) ReadResearchRevisionSnapshot(_ context.Context, run, revision string) (json.RawMessage, error) {
	if run != r.detail.Run.ID || revision != "revision" {
		return nil, fmt.Errorf("snapshot identity mismatch")
	}
	return cloneRaw(r.snapshot), r.readErr
}
func (r *designIsolationRepository) ReadDesignRevisionWriteSnapshot(_ context.Context, run, event string) (json.RawMessage, error) {
	if run != r.detail.Run.ID || event != "review-revision" {
		return nil, fmt.Errorf("review snapshot identity mismatch")
	}
	return cloneRaw(r.snapshot), r.readErr
}
func (r *designIsolationRepository) RecordDecision(_ context.Context, _ HumanDecision, output json.RawMessage, _ int, _ bool, _ json.RawMessage, _ time.Time, _ RuntimeEvent) error {
	r.adopted = cloneRaw(output)
	return nil
}

func designIsolationFixture(t *testing.T, kind string, withCitation ...bool) (*RuntimeService, *designIsolationRepository, Step, CompiledNode, json.RawMessage) {
	t.Helper()
	baseline := raw(`{"status":"research_design_not_empirical_result","populationAndSampling":["誊写"],"n":9007199254740993,"deliverables":[{"title":"方案正文","markdown":"各0–3分"},{"title":"附录1","markdown":"不要糀倒"},{"title":"附录2","markdown":"不要糀倒"},{"title":"附录5","markdown":"即佾如此"}]}`)
	if len(withCitation) > 0 && withCitation[0] {
		baseline = raw(strings.Replace(string(baseline), "誊写", "誊写 [K-AAAAAAAAAAAA]", 1))
	}
	node := CompiledNode{ID: "material_owner", Kind: NodeAgentStage, ReviewPolicy: AIReviewAuto, OutputSchema: raw(`{"type":"object","additionalProperties":true}`)}
	node.OutputSchemaSHA256 = hashJSON(node.OutputSchema)
	step := Step{ID: "owner-step", NodeID: node.ID, Ordinal: 0, Attempt: 1, Status: StepCompleted, Input: raw(`{}`), Output: rawObject(map[string]any{"analysis": baseline})}
	step.InputSHA256 = hashJSON(step.Input)
	before := RunDetail{Run: Run{ID: "run", ProjectID: "project", ConversationID: "conversation", Status: RunCompleted, Inputs: raw(`{}`), Compilation: Compilation{Nodes: []CompiledNode{node}}}, Steps: []Step{step}}
	refs := []tool.CitationRef{}
	if len(withCitation) > 0 && withCitation[0] {
		refs = []tool.CitationRef{{Reference: "[K-AAAAAAAAAAAA]", DocumentID: "doc", IndexVersionID: "index", ChunkID: "chunk", QuoteSHA256: "hash", SourceName: "fixture", Title: "Fixture", Quote: "Exact source"}}
		step.Ordinal = 1
		before.Steps = []Step{{ID: "citations", NodeID: "evidence_extraction", Ordinal: 0, Status: StepCompleted, Output: mustJSON(map[string]any{"citations": refs})}, step}
		before.Run.Compilation.Nodes = append([]CompiledNode{{ID: "evidence_extraction", Kind: NodeCitationSelection}}, node)
		before.Run.Compilation.Edges = []Edge{{FromNode: "evidence_extraction", FromPort: "citations", ToNode: node.ID, ToPort: "evidenceContext"}}
	}
	before.Run.InputsSHA256 = hashJSON(before.Run.Inputs)
	c, err := canonicalJSON(before.Run.Compilation)
	if err != nil {
		t.Fatal(err)
	}
	before.Run.CompilationSHA256 = hashBytes(c)
	before.Run.Compilation.CompilationSHA256 = before.Run.CompilationSHA256
	objects := []revisionWriteObject{}
	add := func(k, pointer string) {
		o := revisionWriteObject{Kind: k, Label: k, OwnerNodeID: node.ID, StepID: step.ID, Ordinal: step.Ordinal, Attempt: step.Attempt, OutputSHA256: hashJSON(step.Output), OutputPointer: pointer}
		o.ID = "edit-" + hashJSON(mustJSON(o))
		objects = append(objects, o)
	}
	add("stage_result", "/analysis")
	add("design_material_collection", "/analysis/deliverables")
	for i := 1; i < 4; i++ {
		add("frozen_design_material", fmt.Sprintf("/analysis/deliverables/%d/markdown", i))
	}
	contract := revisionWriteContract{Version: revisionWriteVersion, Objects: objects}
	p := ResearchRevisionProposal{ID: "revision", RunID: before.Run.ID, NodeID: node.ID, Status: "confirmed", SnapshotSHA256: ResearchRevisionSnapshot(before), WriteContract: mustJSON(contract), WriteContractVersion: revisionWriteVersion, DesignWriteIsolationVersion: designWriteIsolationVersion}
	p.WriteContractSHA256 = hashJSON(p.WriteContract)
	for _, o := range objects {
		if o.Kind == kind {
			p.EditTargets = append(p.EditTargets, o.ID)
		}
	}
	d := before
	d.Run.Status = RunRunning
	d.Run.CurrentStep = step.Ordinal
	d.Steps = append([]Step(nil), before.Steps...)
	index := len(d.Steps) - 1
	d.Steps[index].Attempt, d.Steps[index].Status = 2, StepRunning
	d.Steps[index].Output = raw(`{}`)
	d.RevisionProposals = []ResearchRevisionProposal{p}
	d.Events = []RuntimeEvent{{WorkflowRunID: d.Run.ID, Type: "workflow.user_revision_queued", Payload: mustJSON(map[string]any{"proposal": p, "startOrdinal": step.Ordinal})}}
	r := &designIsolationRepository{detail: d, snapshot: mustJSON(before)}
	s := &RuntimeService{repository: r, now: time.Now, newID: func() (string, error) { return "event", nil }, closed: true}
	if len(refs) > 0 {
		s.tools = tool.NewService(&designIsolationCitationCalls{handoffCitationCalls{calls: []tool.Call{{ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: refs}}}}}, tool.JSONSchemaValidator{})
	}
	return s, r, d.Steps[index], node, baseline
}

func correctedDesign(raw json.RawMessage) json.RawMessage {
	s := strings.ReplaceAll(string(raw), "糀倒", "绊倒")
	return json.RawMessage(strings.ReplaceAll(s, "即佾", "即使"))
}

func TestDesignWriteIsolationRejectsObservedUnselectedChanges(t *testing.T) {
	s, r, step, _, baseline := designIsolationFixture(t, "frozen_design_material")
	mask, err := s.designWriteMask(context.Background(), r.detail, step)
	if err != nil || mask == nil {
		t.Fatal(mask, err)
	}
	good := correctedDesign(baseline)
	if err := mask.validate(good); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, from, to, path string }{
		{"actual extra zero", "各0–3分", "各00–3分", "/deliverables/0/markdown"},
		{"actual changed method word", "誊写", "敲写", "/populationAndSampling/0"},
		{"selected material title", "附录1", "renamed", "/deliverables/1/title"},
		{"precise large integer", "9007199254740993", "9007199254740992", "/n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := json.RawMessage(strings.Replace(string(good), tt.from, tt.to, 1))
			copy := cloneRaw(candidate)
			err := mask.validate(candidate)
			if err == nil || !strings.Contains(err.Error(), tt.path) {
				t.Fatal("unselected mutation escaped exact diff", err)
			}
			if !bytes.Equal(copy, candidate) {
				t.Fatal("validator silently rewrote candidate")
			}
		})
	}
}

func TestDesignWriteIsolationCollectionAndRepresentation(t *testing.T) {
	for _, tt := range []struct {
		name, old, next       string
		appendOnly, bad, host bool
	}{
		{"tail addition", `{"deliverables":[{"title":"a","markdown":"x"}]}`, `{"deliverables":[{"title":"a","markdown":"x"},{"title":"b","markdown":"y"}]}`, true, false, false},
		{"no implicit append", `{"deliverables":[]}`, `{"deliverables":[{"title":"b","markdown":"y"}]}`, false, true, false},
		{"no edit on collection", `{"deliverables":[{"title":"a","markdown":"x"}]}`, `{"deliverables":[{"title":"a","markdown":"changed"}]}`, true, true, false},
		{"no prepend", `{"deliverables":[{"title":"a","markdown":"x"}]}`, `{"deliverables":[{"title":"b","markdown":"y"},{"title":"a","markdown":"x"}]}`, true, true, false},
		{"no delete", `{"deliverables":[{"title":"a","markdown":"x"}]}`, `{"deliverables":[]}`, true, true, false},
		{"create omitted collection", `{"n":1}`, `{"n":1,"deliverables":[]}`, true, false, false},
		{"unchanged omitted collection", `{"n":1}`, `{"n":1}`, true, false, false},
		{"null is not empty", `{"deliverables":null}`, `{"deliverables":[]}`, true, true, true},
		{"cannot erase empty collection", `{"deliverables":[]}`, `{}`, true, true, false},
		{"number and Unicode spelling", `{"deliverables":[],"n":9007199254740993,"text":"中文🙂"}`, `{"text":"\u4e2d文🙂","n":9007199254740993,"deliverables":[]}`, false, false, false},
		{"null versus missing", `{"deliverables":[],"text":null}`, `{"deliverables":[]}`, false, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := designWriteMask{baseline: raw(tt.old), appendOnly: tt.appendOnly}
			err := m.validate(raw(tt.next))
			var host *apperr.Error
			if (err != nil) != tt.bad || (errors.As(err, &host) && host.Code == revisionWriteBaselineInvalid) != tt.host {
				t.Fatal(err)
			}
		})
	}
}

func TestDesignWriteIsolationRejectsDuplicateKeysWithoutRewritingRaw(t *testing.T) {
	s, r, step, node, baseline := designIsolationFixture(t, "frozen_design_material")
	for _, bad := range []string{
		strings.Replace(string(correctedDesign(baseline)), `"populationAndSampling":`, `"populationAndSampling":["敲写"],"populationAndSampling":`, 1),
		strings.Replace(string(correctedDesign(baseline)), `"populationAndSampling":`, `"population\u0041ndSampling":["敲写"],"populationAndSampling":`, 1),
		strings.Replace(string(correctedDesign(baseline)), `"title":"附录1"`, `"title":"unauthorized","title":"附录1"`, 1),
	} {
		_, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, bad)
		var classified *apperr.Error
		if !errors.As(err, &classified) || classified.Code != "WORKFLOW_AI_SUBMISSION_INVALID" {
			t.Fatal("ambiguous candidate escaped precommit", err)
		}
		execution := AIExecution{ID: "execution", WorkflowStepID: step.ID, Attempt: step.Attempt, OutputText: bad}
		if err := s.completeAIExecution(context.Background(), r.detail, step, node, execution, true); err != nil {
			t.Fatal(err)
		}
		if r.failure != "WORKFLOW_REVISION_WRITE_SCOPE_INVALID" || r.execution.OutputText != bad || len(r.output) > 0 {
			t.Fatal("ambiguous draft accepted or raw lost")
		}
	}
	m := designWriteMask{baseline: raw(`{"deliverables":[],"a":1,"a":1}`)}
	var host *apperr.Error
	if err := m.validate(raw(`{"deliverables":[],"a":1}`)); !errors.As(err, &host) || host.Code != revisionWriteBaselineInvalid {
		t.Fatal("ambiguous baseline blamed model", err)
	}
}

func TestDesignWriteIsolationCitationAliasesShareAcceptanceDomain(t *testing.T) {
	s, r, step, node, baseline := designIsolationFixture(t, "frozen_design_material", true)
	canonical := correctedDesign(baseline)
	alias := citation.KnowledgeReference("current-chat", "index", "chunk", "hash")
	shown := raw(strings.ReplaceAll(string(canonical), "[K-AAAAAAAAAAAA]", alias))
	execution := AIExecution{ID: "current", ChatRunID: "current-chat", WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, OutputText: string(shown)}
	r.detail.AIExecutions = []AIExecution{execution}
	accepted, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, string(shown))
	if err != nil || !rawJSONEqual(accepted, canonical) {
		t.Fatal("reversible citation alias rejected at precommit", err)
	}
	if err := s.completeAIExecution(context.Background(), r.detail, step, node, execution, true); err != nil {
		t.Fatal(err)
	}
	if r.failure != "" || !rawJSONEqual(r.execution.Output, canonical) || r.execution.OutputText != string(shown) {
		t.Fatal("final alias restoration differs from precommit", r.failure)
	}
	completed := r.execution
	if err := s.completeAIExecution(context.Background(), r.detail, step, node, completed, false); err != nil || r.failure != "" {
		t.Fatal("restored completed execution rejected", err, r.failure)
	}
	r.detail.Run.Status = RunWaitingHumanConfirmation
	r.detail.Steps[len(r.detail.Steps)-1].Status = StepWaitingHumanConfirmation
	s.permissions = permission.NewEngine(nil)
	s.projects = fixedProjectLoader{value: project.Project{WorkspacePath: t.TempDir()}}
	s.ai = handoffAdoptionExecutor{state: AIStageState{Status: "completed", ChatRunID: "current-chat", Text: string(shown)}}
	if _, err := s.Decide(context.Background(), HumanDecisionCommand{ProjectID: "project", RunID: "run", StepID: step.ID, Approved: true}); err != nil {
		t.Fatal("human adoption rejected equivalent alias", err)
	}
	adopted, err := outputPort(r.adopted, "analysis")
	if err != nil || !rawJSONEqual(adopted, canonical) {
		t.Fatal("human canonical domain differs", err)
	}
	bad := strings.Replace(string(shown), "誊写", "敲写", 1)
	r.detail.Run.Status = RunRunning
	r.detail.Steps[len(r.detail.Steps)-1].Status = StepRunning
	if _, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, bad); err == nil {
		t.Fatal("alias restoration concealed actual word change")
	}
}

func TestDesignWriteIsolationTrustedHistoryAndLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*designIsolationRepository)
		legacy bool
	}{
		{"missing event", func(r *designIsolationRepository) { r.detail.Events = nil }, false},
		{"missing proposal", func(r *designIsolationRepository) { r.detail.RevisionProposals = nil }, false},
		{"missing stored policy", func(r *designIsolationRepository) { r.detail.RevisionProposals[0].DesignWriteIsolationVersion = "" }, false},
		{"wrong event start", func(r *designIsolationRepository) {
			r.detail.Events[0].Payload = raw(`{"startOrdinal":0,"proposal":{"designWriteIsolationVersion":"design-material-isolation-v1","nodeId":"missing"}}`)
		}, false},
		{"missing snapshot", func(r *designIsolationRepository) { r.readErr = errors.New("missing snapshot") }, false},
		{"truncated snapshot", func(r *designIsolationRepository) { r.snapshot = raw(`{"run":{"id":"run"}}`) }, false},
		{"changed baseline", func(r *designIsolationRepository) {
			r.snapshot = raw(strings.Replace(string(r.snapshot), "各0–3分", "各00–3分", 1))
		}, false},
		{"event proposal mismatch", func(r *designIsolationRepository) { r.detail.RevisionProposals[0].Changes = []string{"injected"} }, false},
		{"unknown policy", func(r *designIsolationRepository) {
			r.detail.RevisionProposals[0].DesignWriteIsolationVersion = "unknown"
		}, false},
		{"automatic repair keeps scope", func(r *designIsolationRepository) {
			r.detail.Events = append(r.detail.Events, RuntimeEvent{Type: "workflow.upstream_revision_queued", Payload: raw(`{}`)})
		}, true},
		{"old review cannot erase narrow scope", func(r *designIsolationRepository) {
			r.detail.Events = append(r.detail.Events, RuntimeEvent{Type: "workflow.review_revision_queued", Payload: mustJSON(reviewRevision{ProducerNodeID: "material_owner", ProducerStepID: "owner-step"})})
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, r, step, _, baseline := designIsolationFixture(t, "frozen_design_material")
			tt.change(r)
			err := s.validateDesignRevisionWrites(context.Background(), r.detail, step, correctedDesign(baseline))
			if (err == nil) != tt.legacy {
				t.Fatal("trusted history result", err)
			}
			if !tt.legacy {
				var classified *apperr.Error
				if !errors.As(err, &classified) || classified.Code != revisionWriteBaselineInvalid {
					t.Fatal("host binding consumed model correction", err)
				}
			}
			if tt.name == "automatic repair keeps scope" {
				candidate := raw(strings.Replace(string(baseline), "誊写", "敲写", 1))
				if s.validateDesignRevisionWrites(context.Background(), r.detail, step, candidate) == nil {
					t.Fatal("automatic repair expanded permission")
				}
			}
		})
	}
	// Historical confirmed generations are kept intact, not silently upgraded.
	s, r, step, _, baseline := designIsolationFixture(t, "frozen_design_material")
	r.detail.RevisionProposals[0].DesignWriteIsolationVersion = ""
	r.detail.Events[0].Payload = mustJSON(map[string]any{"proposal": r.detail.RevisionProposals[0], "startOrdinal": 0})
	r.snapshot = nil
	if err := s.validateDesignRevisionWrites(context.Background(), r.detail, step, baseline); err != nil {
		t.Fatal("legacy was retroactively upgraded", err)
	}
	// Whole-stage access is explicit but still requires a verified baseline.
	s, r, step, _, _ = designIsolationFixture(t, "stage_result")
	if err := s.validateDesignRevisionWrites(context.Background(), r.detail, step, raw(`{"rewritten":true}`)); err != nil {
		t.Fatal(err)
	}
	r.snapshot = raw(`{}`)
	if s.validateDesignRevisionWrites(context.Background(), r.detail, step, raw(`{"rewritten":true}`)) == nil {
		t.Fatal("wide permission bypassed history")
	}
}

func TestDesignWriteIsolationAllAcceptanceEntrypoints(t *testing.T) {
	for _, brokenHistory := range []bool{false, true} {
		t.Run(fmt.Sprint("host_", brokenHistory), func(t *testing.T) {
			s, r, step, node, baseline := designIsolationFixture(t, "frozen_design_material")
			bad := raw(strings.Replace(string(correctedDesign(baseline)), "各0–3分", "各00–3分", 1))
			if brokenHistory {
				r.snapshot = raw(`{}`)
			}
			before := mustJSON(r.detail)
			_, err := s.ValidateResearchSubmission(context.Background(), "conversation", "run", step.ID, string(bad))
			want := "WORKFLOW_AI_SUBMISSION_INVALID"
			if brokenHistory {
				want = revisionWriteBaselineInvalid
			}
			var classified *apperr.Error
			if !errors.As(err, &classified) || classified.Code != want {
				t.Fatal("precommit classification", err)
			}
			if !bytes.Equal(before, mustJSON(r.detail)) || len(r.output) > 0 || len(r.repairs) > 0 {
				t.Fatal("read-only check mutated history")
			}
			for _, finish := range []bool{true, false} {
				execution := AIExecution{ID: "execution", WorkflowStepID: step.ID, Attempt: step.Attempt, ChatRunID: "chat", InputSHA256: step.InputSHA256, Output: bad, OutputText: string(bad), OutputSHA256: hashJSON(bad)}
				if err := s.completeAIExecution(context.Background(), r.detail, step, node, execution, finish); err != nil {
					t.Fatal(err)
				}
				code := "WORKFLOW_REVISION_WRITE_SCOPE_INVALID"
				if brokenHistory {
					code = revisionWriteBaselineInvalid
				}
				if r.failure != code || len(r.output) > 0 || len(r.repairs) > 0 {
					t.Fatal("completion accepted/auto-repaired bad scope", r.failure)
				}
				if finish && (r.execution.OutputText != string(bad) || r.execution.Status != "failed" || string(r.execution.Output) != "{}") {
					t.Fatal("lost raw rejected response")
				}
			}
			// Actual human adoption path, not just the mask helper.
			r.detail.Run.Status = RunWaitingHumanConfirmation
			r.detail.Steps[0].Status = StepWaitingHumanConfirmation
			s.permissions = permission.NewEngine(nil)
			s.projects = fixedProjectLoader{value: project.Project{WorkspacePath: t.TempDir()}}
			s.tools = tool.NewService(&handoffCitationCalls{}, tool.JSONSchemaValidator{})
			s.ai = handoffAdoptionExecutor{state: AIStageState{Status: "completed", ChatRunID: "chat", Text: string(bad)}}
			if _, err := s.Decide(context.Background(), HumanDecisionCommand{ProjectID: "project", RunID: "run", StepID: step.ID, Approved: true}); err == nil || !brokenHistory && !strings.Contains(err.Error(), "/deliverables/0/markdown") || brokenHistory && !errors.As(err, &classified) {
				t.Fatal("human adoption bypassed isolation or failed for an unrelated reason", err)
			}
			if len(r.adopted) > 0 {
				t.Fatal("bad adoption persisted")
			}
		})
	}
}
