package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type indexedContinuationFixture struct {
	submissionRuntimeRepository
	queued   bool
	queueErr error
	event    RuntimeEvent
	detail   RunDetail
}

func (r *indexedContinuationFixture) GetRun(context.Context, string, string) (RunDetail, error) {
	return r.detail, nil
}

type indexedCallsFixture struct{ quoteReplayCalls }

func (r *indexedCallsFixture) ListBySubject(_ context.Context, _ tool.SubjectKind, id string) ([]tool.Call, error) {
	return r.ListByRun(context.Background(), id)
}

func (r *indexedCallsFixture) ListByRun(_ context.Context, id string) ([]tool.Call, error) {
	result := []tool.Call{}
	for _, c := range r.calls {
		if c.RunID == id {
			result = append(result, c)
		}
	}
	return result, nil
}

func (r *indexedContinuationFixture) QueueLiteratureContinuation(_ context.Context, _, _, _ string, _ int, _ time.Time, event RuntimeEvent) error {
	r.event = event
	r.queued = r.queueErr == nil
	return r.queueErr
}

func indexedFixture() (RunDetail, Step, CompiledNode, AIExecution, *indexedCallsFixture) {
	node := CompiledNode{ID: "evidence_screening", Kind: NodeAgentStage, ReviewPolicy: AIReviewAuto, PromptVersion: selectedEvidenceVersion, OutputSchema: selectedEvidenceSchema(), AllowedTools: []ToolSnapshot{{QualifiedName: "builtin.knowledge.read"}}}
	step := Step{ID: "screen", NodeID: node.ID, Status: StepRunning, Attempt: 1, Ordinal: 3, Input: raw(`{"documentIds":["doc"],"candidates":[]}`)}
	step.InputSHA256 = hashJSON(step.Input)
	d := RunDetail{Run: Run{ID: "run", ProjectID: "project", Compilation: Compilation{Nodes: []CompiledNode{node}}}, Steps: []Step{{NodeID: "evidence_import", Status: StepCompleted, Output: raw(`{"structured":{"attachmentIds":["a"]}}`)}, {NodeID: "evidence_sync", Status: StepCompleted, Output: raw(`{"structured":{"documentIds":["doc"]}}`)}, step}}
	e := AIExecution{ID: "execution", WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ChatRunID: "chat", Status: "running", OutputText: "Collected source; not a scientific submission."}
	ref := tool.CitationRef{Kind: citation.KindKnowledgeChunk, ProjectID: "project", IndexVersionID: "index", DocumentID: "doc", AttachmentID: "a", ChunkID: "chunk", SourceName: "paper.pdf", MIMEType: "application/pdf", Locator: "page:2", Quote: "Verified mechanism in source order.", QuoteSHA256: citation.QuoteSHA256("Verified mechanism in source order."), SourceEnd: 35}
	ref.Reference = citation.KnowledgeReference(e.ChatRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	calls := &indexedCallsFixture{quoteReplayCalls{calls: []tool.Call{{ID: "read", RunID: e.ChatRunID, ToolName: "builtin.resource.open", Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Structured: raw(`{"sourceTool":"builtin.knowledge.read"}`), Citations: []tool.CitationRef{ref}}}}}}
	return d, step, node, e, calls
}

func TestIndexedEvidenceCheckpointRebindSelectionAndChatCitation(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	repo := &indexedContinuationFixture{}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if err := s.completeAIExecution(context.Background(), d, step, node, e, true); err != nil {
		t.Fatal(err)
	}
	if !repo.queued || repo.execution.OutputText != e.OutputText || !rawJSONEqual(repo.execution.Output, raw(`{}`)) || len(repo.output) != 0 || len(repo.repairs) != 0 {
		t.Fatal("collection draft was accepted or sources not checkpointed")
	}
	d.Events = append(d.Events, repo.event)
	d.AIExecutions = append(d.AIExecutions, repo.execution)
	fresh := bindIndexedEvidence(d, node, step.Input)
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	_ = json.Unmarshal(fresh, &in)
	if len(in.Candidates) != 1 || in.Candidates[0].Reference == calls.calls[0].Result.Citations[0].Reference || !strings.Contains(string(fresh), "indexedReadRanges") {
		t.Fatal(string(fresh))
	}
	step.Input = fresh
	step.InputSHA256 = hashJSON(fresh)
	d.Steps[2] = step
	seeds, err := s.workflowAICitationSeeds(context.Background(), d, step)
	if err != nil || len(seeds) != 1 {
		t.Fatal(seeds, err)
	}
	chatRefs, err := citation.ReissueKnowledgeRefs("run", "next-chat", "project", seeds)
	if err != nil {
		t.Fatal(err)
	}
	resolved := citation.Resolve("next-chat", "message", chatRefs[0].Reference, []tool.Call{{ID: "seed", RunID: "next-chat", ToolName: citation.WorkflowSeedToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: chatRefs}}}, time.Now())
	if len(resolved) != 1 {
		t.Fatal("new source is not a clickable citation")
	}
	compact := decodeObject(fresh)
	compact["candidates"] = []map[string]any{{"reference": in.Candidates[0].Reference, "documentId": "doc", "quote": in.Candidates[0].Quote, "sourceName": "paper.pdf", "title": ""}}
	d.Steps[2].Input = mustJSON(compact)
	selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, raw(`{"candidates":[]}`))
	_ = json.Unmarshal(selection, &in)
	if err := validateCitationSubset(in.Candidates, seeds); err != nil {
		t.Fatal("selection lost full source identity", err)
	}
	verified, err := s.verifiedIndexedEvidence(context.Background(), d)
	if err != nil || len(verified) != 1 {
		t.Fatal(verified, err)
	}
	repo.detail = d
	projected, err := s.IndexedEvidenceSources(context.Background(), "project", "run")
	if err != nil || len(projected) != 1 || projected[0].ID != "read" || projected[0].RunID != "chat" {
		t.Fatal("publication lost actual source call", projected, err)
	}
	var invalid map[string]any
	_ = json.Unmarshal(repo.event.Payload, &invalid)
	invalid["executionId"] = "another-workflow-execution"
	d.Events[0].Payload = mustJSON(invalid)
	if _, err := s.verifiedIndexedEvidence(context.Background(), d); err == nil {
		t.Fatal("unbound source event accepted")
	}
	d.Events[0] = repo.event
	saved := calls.calls[0].Result.Citations[0].Quote
	calls.calls[0].Result.Citations[0].Quote = "tampered after checkpoint"
	if _, err := s.verifiedIndexedEvidence(context.Background(), d); err == nil {
		t.Fatal("tampered durable source accepted")
	}
	calls.calls[0].Result.Citations[0].Quote = saved
	var page map[string]any
	_ = json.Unmarshal(repo.event.Payload, &page)
	page["materialKey"] = "stale"
	d.Events[0].Payload = mustJSON(page)
	if len(indexedEvidenceSnapshots(d)) != 0 {
		t.Fatal("changed materials reused old reads")
	}
}

func TestIndexedEvidenceCrashRecoveryAndScope(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	repo := &indexedContinuationFixture{queueErr: fmt.Errorf("injected transaction fault")}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if _, err := s.checkpointIndexedEvidence(context.Background(), d, step, node, e); err == nil {
		t.Fatal("fault missed")
	}
	// Completed collection + running step after crash: replay durable source,
	// never commit the empty draft or repeat the model/source call.
	repo.queueErr = nil
	if err := s.projectFinishedAIExecution(context.Background(), d, step, node, repo.execution); err != nil || !repo.queued || len(repo.output) != 0 {
		t.Fatal("recovery failed", err)
	}
	d.Events = append(d.Events, repo.event)
	step.Input = bindIndexedEvidence(d, node, step.Input)
	step.InputSHA256 = hashJSON(step.Input)
	e.InputSHA256 = step.InputSHA256
	if refs, err := s.newIndexedEvidence(context.Background(), d, step, node, e); err != nil || len(refs) != 0 {
		t.Fatal("same read loops", refs, err)
	}
	d, step, node, e, calls = indexedFixture()
	s.tools = tool.NewService(calls, tool.JSONSchemaValidator{})
	node.AllowedTools = nil
	if refs, err := s.newIndexedEvidence(context.Background(), d, step, node, e); err != nil || len(refs) != 0 {
		t.Fatal("old frozen permissions expanded")
	}
	node.AllowedTools = []ToolSnapshot{{QualifiedName: "builtin.knowledge.read"}}
	calls.calls[0].Result.Citations[0].AttachmentID = "private"
	if _, err := s.newIndexedEvidence(context.Background(), d, step, node, e); err == nil {
		t.Fatal("unselected evidence accepted")
	}
	d, step, node, e, calls = indexedFixture()
	s.tools = tool.NewService(calls, tool.JSONSchemaValidator{})
	calls.calls[0].Result.Citations[0].Quote = "tampered"
	if _, err := s.newIndexedEvidence(context.Background(), d, step, node, e); err == nil {
		t.Fatal("tampered quote accepted")
	}
}

func TestIndexedEvidenceWorkingSetRetainsNewSourceWithoutOverflow(t *testing.T) {
	d, step, node, _, _ := indexedFixture()
	refs := []tool.CitationRef{}
	for i := 0; i < 300; i++ {
		refs = append(refs, tool.CitationRef{Reference: fmt.Sprintf("[K-%012d]", i), DocumentID: "doc", Quote: strings.Repeat("x", 1000)})
	}
	d.Events = []RuntimeEvent{{Type: "workflow.indexed_evidence_read", Payload: mustJSON(map[string]any{"materialKey": localEvidenceMaterialKey(d), "citations": refs})}}
	in := bindIndexedEvidence(d, node, step.Input)
	var v struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	_ = json.Unmarshal(in, &v)
	if len(v.Candidates) >= 256 || len(in) > 120*1024 || len(indexedEvidenceSnapshots(d)) != 300 {
		t.Fatal("budget lost audit or overflowed")
	}
	if v.Candidates[0].Reference != refs[0].Reference {
		t.Fatal("latest page lost")
	}
}

func TestIndexedEvidenceOverlappingSearchAndReadPreservesExactSourceSnapshots(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy_reader=%v", legacy), func(t *testing.T) {
			d, step, node, e, calls := indexedFixture()
			read := &calls.calls[0].Result.Citations[0]
			if !legacy {
				read.ID = read.AttachmentID
			}
			search := *read
			search.ID = search.AttachmentID
			search.Reference = citation.KnowledgeReference(d.Run.ID, search.IndexVersionID, search.ChunkID, search.QuoteSHA256)
			d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "search", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}})
			d.Run.Compilation.Edges = []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}
			d.Steps = append(d.Steps, Step{ID: "search", NodeID: "search", Ordinal: 2, Status: StepCompleted, Output: rawObject(map[string]any{"citations": []tool.CitationRef{search}})})
			calls.calls = append(calls.calls, tool.Call{ID: "search", RunID: d.Run.ID, ToolName: citation.KnowledgeToolName, Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Citations: []tool.CitationRef{search}}})
			repo := &indexedContinuationFixture{}
			s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
			if ok, err := s.checkpointIndexedEvidence(context.Background(), d, step, node, e); err != nil || !ok {
				t.Fatal(ok, err)
			}
			d.Events = []RuntimeEvent{repo.event}
			d.AIExecutions = []AIExecution{repo.execution}
			step.Input = bindIndexedEvidence(d, node, rawObject(map[string]any{"candidates": []tool.CitationRef{search}, "documentIds": []string{"doc"}}))
			step.InputSHA256 = hashJSON(step.Input)
			d.Steps[2] = step
			seeds, err := s.workflowAICitationSeeds(context.Background(), d, step)
			if err != nil || len(seeds) != 1 || seeds[0].ID != read.ID {
				t.Fatal("overlapping search overwrote exact indexed snapshot", seeds, err)
			}
			// Selection must recover precisely the accepted working set, never
			// replace its snapshot just because another source shares its marker.
			selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, rawObject(map[string]any{"candidates": []tool.CitationRef{search}}))
			var selected struct {
				Candidates []tool.CitationRef `json:"candidates"`
			}
			if err := json.Unmarshal(selection, &selected); err != nil {
				t.Fatal(err)
			}
			if err := validateCitationSubset(selected.Candidates, seeds); err != nil {
				t.Fatal("selection replaced accepted snapshot", err)
			}
			for _, mutate := range []func(*tool.CitationRef){
				func(r *tool.CitationRef) { r.ID = "invented" },
				func(r *tool.CitationRef) { r.Locator = "page:99" },
				func(r *tool.CitationRef) { r.SourceEnd++ },
				func(r *tool.CitationRef) { r.ProjectID = "foreign" },
				func(r *tool.CitationRef) { r.Quote = "changed" },
			} {
				invalid := seeds[0]
				mutate(&invalid)
				bad := step
				bad.Input = rawObject(map[string]any{"candidates": []tool.CitationRef{invalid}})
				if _, err := workflowCandidateCitationSeeds(d.Run.Compilation, d.Steps, bad, seeds); err == nil {
					t.Fatal("changed full snapshot accepted", invalid)
				}
			}
			bad := step
			bad.Input = rawObject(map[string]any{"candidates": []tool.CitationRef{seeds[0], seeds[0]}})
			if _, err := workflowCandidateCitationSeeds(d.Run.Compilation, d.Steps, bad, seeds); err == nil {
				t.Fatal("duplicate marker accepted")
			}
			// Compact coverage input is host-only; restore the exact intact
			// snapshot for seeding and formal selection without rewriting quotes.
			compact := map[string]any{"reference": seeds[0].Reference, "documentId": seeds[0].DocumentID, "sourceName": seeds[0].SourceName, "title": seeds[0].Title, "quote": seeds[0].Quote}
			d.Steps[2].Input = rawObject(map[string]any{"candidates": []any{compact}})
			selection = bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, rawObject(map[string]any{"candidates": []tool.CitationRef{search}}))
			if err := json.Unmarshal(selection, &selected); err != nil {
				t.Fatal(err)
			}
			if err := validateCitationSubset(selected.Candidates, seeds); err != nil {
				t.Fatal("compact selection changed source", err)
			}
		})
	}
}

func TestIndexedEvidenceSelectionKeepsOnlySynthesisWorkingSet(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			d, step, node, _, calls := indexedFixture()
			read := calls.calls[0].Result.Citations[0]
			read.Reference = citation.KnowledgeReference(d.Run.ID, read.IndexVersionID, read.ChunkID, read.QuoteSHA256)
			search := read
			search.ID = search.AttachmentID
			omitted := read
			omitted.ChunkID = "audit-only-chunk"
			omitted.Reference = citation.KnowledgeReference(d.Run.ID, omitted.IndexVersionID, omitted.ChunkID, omitted.QuoteSHA256)
			d.Events = []RuntimeEvent{{Type: "workflow.indexed_evidence_read", Payload: rawObject(map[string]any{"materialKey": localEvidenceMaterialKey(d), "citations": []tool.CitationRef{read, omitted}})}}
			d.Run.Compilation.Nodes = append(d.Run.Compilation.Nodes, CompiledNode{ID: "search", Kind: NodeTool, Tool: &ToolSnapshot{QualifiedName: citation.KnowledgeToolName}})
			d.Run.Compilation.Edges = []Edge{{FromNode: "search", FromPort: "citations", ToNode: node.ID, ToPort: "candidates"}}
			d.Steps = append(d.Steps, Step{NodeID: "search", Ordinal: 2, Status: StepCompleted, Output: rawObject(map[string]any{"citations": []tool.CitationRef{search, omitted}})})
			step.Input = rawObject(map[string]any{"candidates": []tool.CitationRef{search}})
			if compact {
				step.Input = rawObject(map[string]any{"candidates": []any{map[string]any{"reference": search.Reference, "documentId": search.DocumentID, "sourceName": search.SourceName, "title": search.Title, "quote": search.Quote}}})
			}
			d.Steps[2] = step
			d.AIExecutions = []AIExecution{{Status: "completed", Output: rawObject(map[string]any{"recommendedReferences": []string{omitted.Reference}})}}
			seeds, err := workflowCandidateCitationSeeds(d.Run.Compilation, d.Steps, step, []tool.CitationRef{read, omitted})
			if err != nil || len(seeds) != 1 {
				t.Fatal("synthesis seed fixture", seeds, err)
			}
			selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, rawObject(map[string]any{"candidates": []tool.CitationRef{search, omitted}}))
			var selected struct {
				Candidates []tool.CitationRef `json:"candidates"`
			}
			if err := json.Unmarshal(selection, &selected); err != nil || len(selected.Candidates) != 1 {
				t.Fatal("selection added an audit-only or previously recommended chunk", string(selection), err)
			}
			if err := validateCitationSubset(selected.Candidates, seeds); err != nil {
				t.Fatal("selection replaced the frozen search snapshot with the overlapping read", err)
			}
		})
	}
}

func TestIndexedEvidenceRereadKeepsLegacyAndCurrentSnapshots(t *testing.T) {
	d, step, node, e, calls := indexedFixture()
	repo := &indexedContinuationFixture{}
	s := &RuntimeService{repository: repo, tools: tool.NewService(calls, tool.JSONSchemaValidator{}), now: time.Now, newID: func() (string, error) { return "event", nil }}
	if ok, err := s.checkpointIndexedEvidence(context.Background(), d, step, node, e); err != nil || !ok {
		t.Fatal("first legacy read", ok, err)
	}
	d.Events = []RuntimeEvent{repo.event}
	d.AIExecutions = []AIExecution{repo.execution}
	legacy := indexedEvidenceSnapshots(d)[0]
	// The previously read block is outside this execution's working set (for
	// example, evicted by a newer page's budget). A current reader rereads it.
	step.Attempt++
	step.Input = raw(`{"documentIds":["doc"],"candidates":[]}`)
	step.InputSHA256 = hashJSON(step.Input)
	d.Steps[2] = step
	e = AIExecution{ID: "second-execution", WorkflowStepID: step.ID, Attempt: step.Attempt, InputSHA256: step.InputSHA256, ChatRunID: "second-chat", Status: "running"}
	current := calls.calls[0].Result.Citations[0]
	current.ID = current.AttachmentID
	current.Reference = citation.KnowledgeReference(e.ChatRunID, current.IndexVersionID, current.ChunkID, current.QuoteSHA256)
	calls.calls = append(calls.calls, tool.Call{ID: "second-read", RunID: e.ChatRunID, ToolName: "builtin.resource.open", Status: tool.CallCompleted, Result: &tool.Result{Status: tool.ResultSuccess, Structured: raw(`{"sourceTool":"builtin.knowledge.read"}`), Citations: []tool.CitationRef{current}}})
	if ok, err := s.checkpointIndexedEvidence(context.Background(), d, step, node, e); err != nil || ok {
		t.Fatal("evicted reread restarted synthesis", ok, err)
	}
	// Existing installations may already have checkpointed the same source
	// with both reader representations. Preserve/replay those historical events
	// even though a new reread no longer schedules another synthesis.
	repo.event = RuntimeEvent{Type: "workflow.indexed_evidence_read", Payload: mustJSON(indexedEvidencePage{Key: localEvidenceMaterialKey(d), Run: e.ChatRunID, StepID: step.ID, ExecutionID: e.ID, Attempt: e.Attempt, Citations: []tool.CitationRef{func() tool.CitationRef { r := current; r.Reference = legacy.Reference; return r }()}})}
	repo.execution = e
	repo.execution.Status, repo.execution.Output, repo.execution.OutputSHA256 = "completed", raw(`{}`), hashJSON(raw(`{}`))
	d.Events = append(d.Events, repo.event)
	d.AIExecutions = append(d.AIExecutions, repo.execution)
	current.Reference = legacy.Reference
	verified, err := s.verifiedIndexedEvidence(context.Background(), d)
	if err != nil || len(verified) != 2 {
		t.Fatal("reread discarded an intact source snapshot", len(verified), err)
	}
	for _, frozen := range []tool.CitationRef{legacy, current} {
		step.Input = rawObject(map[string]any{"candidates": []tool.CitationRef{frozen}})
		d.Steps[2] = step
		seeds, err := s.workflowAICitationSeeds(context.Background(), d, step)
		if err != nil || len(seeds) != 1 || validateCitationSubset(seeds, []tool.CitationRef{frozen}) != nil {
			t.Fatal("frozen reread seed changed", frozen.ID, seeds, err)
		}
		selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, raw(`{"candidates":[]}`))
		var offered struct {
			Candidates []tool.CitationRef `json:"candidates"`
		}
		if err := json.Unmarshal(selection, &offered); err != nil || len(offered.Candidates) != 1 || validateCitationSubset(offered.Candidates, seeds) != nil {
			t.Fatal("selection changed frozen reread", err)
		}
	}
	// Compact coverage restoration must use the same complete snapshot for
	// seeding and selection, even if two reader versions issued one marker.
	step.Input = rawObject(map[string]any{"candidates": []any{map[string]any{"reference": legacy.Reference, "documentId": legacy.DocumentID, "sourceName": legacy.SourceName, "title": legacy.Title, "quote": legacy.Quote}}})
	step.InputSHA256 = hashJSON(step.Input)
	d.Steps[2] = step
	seeds, err := s.workflowAICitationSeeds(context.Background(), d, step)
	if err != nil || len(seeds) != 1 || seeds[0].ID != current.ID {
		t.Fatal("compact reread seed did not use latest actual snapshot", seeds, err)
	}
	selection := bindIndexedEvidence(d, CompiledNode{ID: "evidence_extraction"}, raw(`{"candidates":[]}`))
	var offered struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	if err := json.Unmarshal(selection, &offered); err != nil || len(offered.Candidates) != 1 || validateCitationSubset(offered.Candidates, seeds) != nil {
		t.Fatal("compact selection and seeds disagree", err)
	}
	// Reading the already offered block once more is not new progress and may
	// not discard a scientific submission or schedule another synthesis.
	e.InputSHA256 = step.InputSHA256
	if next, err := s.newIndexedEvidence(context.Background(), d, step, node, e); err != nil || len(next) != 0 {
		t.Fatal("same-block reread caused a continuation loop", next, err)
	}
	// Repeated identical audit events do not multiply available snapshots.
	d.Events = append(d.Events, d.Events[1])
	if got := len(indexedEvidenceSnapshots(d)); got != 2 {
		t.Fatal("identical reread snapshot not deduplicated", got)
	}
	repo.detail = d
	projected, err := s.IndexedEvidenceSources(context.Background(), d.Run.ProjectID, d.Run.ID)
	if err != nil || len(projected) != 2 {
		t.Fatal("publication lost original reread sources", len(projected), err)
	}
	for _, c := range projected {
		if c.ID == "read" && c.RunID == "chat" && validateCitationSubset(c.Result.Citations, []tool.CitationRef{legacy}) == nil {
			continue
		}
		if c.ID == "second-read" && c.RunID == "second-chat" && validateCitationSubset(c.Result.Citations, []tool.CitationRef{current}) == nil {
			continue
		}
		t.Fatal("publication invented or rewrote reread provenance", c.ID, c.RunID)
	}
	bad := calls.calls[1].Result.Citations[0]
	calls.calls[1].Result.Citations[0].ID = "tampered"
	if _, err := s.verifiedIndexedEvidence(context.Background(), d); err == nil {
		t.Fatal("tampered later source hidden by legacy snapshot")
	}
	calls.calls[1].Result.Citations[0] = bad
	calls.calls[1].Status = tool.CallFailed
	if _, err := s.verifiedIndexedEvidence(context.Background(), d); err == nil {
		t.Fatal("failed later source hidden by intact legacy snapshot")
	}
	calls.calls[1].Status = tool.CallCompleted
	duplicate := step
	duplicate.Input = rawObject(map[string]any{"candidates": []tool.CitationRef{legacy, current}})
	if _, err := s.workflowAICitationSeeds(context.Background(), d, duplicate); err == nil {
		t.Fatal("two valid snapshots turned one marker into two offered citations")
	}
	// Audit source variants are not two distinct read blocks in model state.
	d.Steps[2].Input = raw(`{}`)
	if ranges := indexedReadRanges(d); len(ranges) != 1 {
		t.Fatal("reread variants inflated physical read ranges", len(ranges))
	}
}

func TestIndexedReadRangeFallbackRetainsLatestUniqueBlocks(t *testing.T) {
	d, _, _, _, _ := indexedFixture()
	for batch := 0; batch < 2; batch++ {
		refs := []tool.CitationRef{}
		for i := 0; i < 150; i++ {
			refs = append(refs, tool.CitationRef{Reference: fmt.Sprintf("[K-%012d]", batch*150+i), DocumentID: "doc", Locator: fmt.Sprintf("page:%d", batch+1)})
		}
		d.Events = append(d.Events, RuntimeEvent{Type: "workflow.indexed_evidence_read", Payload: rawObject(map[string]any{"materialKey": localEvidenceMaterialKey(d), "citations": refs})})
	}
	read := indexedReadRanges(d)
	if len(read) != 240 || read[0]["reference"] != "[K-000000000150]" || read[0]["locator"] != "page:2" {
		t.Fatal("fallback omitted newest source ranges", len(read), read[0])
	}
}
