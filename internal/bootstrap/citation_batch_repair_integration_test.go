package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type batchQuoteConnector struct{ researchClosureConnector }

func (batchQuoteConnector) Search(context.Context, research.SearchOptions) ([]research.Work, error) {
	works := []research.Work{}
	for i := 1; i <= 3; i++ {
		works = append(works, batchQuoteWork(i))
	}
	return works, nil
}

func (batchQuoteConnector) Fetch(_ context.Context, recordID string) (research.Work, error) {
	for i := 1; i <= 3; i++ {
		w := batchQuoteWork(i)
		if w.SourceRecordID == recordID {
			return w, nil
		}
	}
	return research.Work{}, fmt.Errorf("unknown fixture record")
}

func batchQuoteWork(i int) research.Work {
	w := researchClosureWork()
	w.SourceRecordID = fmt.Sprintf("10.5555/sciaide-batch-quote-%d", i)
	w.Identifiers.DOI = w.SourceRecordID
	w.Title = fmt.Sprintf("Evidence quotation fixture %d", i)
	w.Abstract = fmt.Sprintf("The verified epsilon forty two finding for source %d is reproducible. An intervening qualification limits this result to the fixture. The final observation must not be joined to the first sentence with omissions.", i)
	return w
}

func TestEvidenceBatchQuoteRepairAcrossHTTPAndRestart(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	var rejected, patch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		prompt := workflowAITestPrompt(body)
		input := workflowAITestStageInput(prompt)
		candidates, _ := input["candidates"].([]any)
		if len(candidates) < 3 {
			t.Error("fixture did not expose three independently bound quotes")
			http.Error(w, "missing candidates", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		refs, assessments, updates := []string{}, []any{}, []any{}
		for _, item := range candidates {
			c := item.(map[string]any)
			ref, source := c["reference"].(string), []rune(c["quote"].(string))
			q := string(source[:min(300, len(source))])
			refs = append(refs, ref)
			assessments = append(assessments, map[string]any{"reference": ref, "decision": "support", "sourceLevel": "abstract", "reason": "Fixture comparison", "supportingQuote": "Invented prefix ... " + q})
			updates = append(updates, map[string]any{"reference": ref, "supportingQuote": q})
		}
		answer := ""
		if requests == 1 {
			analyses := []any{}
			for _, id := range input["documentIds"].([]any) {
				analyses = append(analyses, map[string]any{"documentId": id, "finding": "Fixture source read", "limitations": "Not real scientific evidence", "references": []string{}})
			}
			value, _ := json.Marshal(map[string]any{"summary": "Same fixture conclusion", "recommendation": "proceed_limited", "recommendedReferences": refs, "citationAssessments": assessments, "documentAnalyses": analyses})
			rejected = string(value)
			answer = "```json\n" + rejected + "\n```"
		} else if requests == 2 {
			for _, ref := range refs {
				if !strings.Contains(prompt, `"reference":"`+ref+`","rejectedSupportingQuote"`) {
					t.Error("correction omitted an issue: " + ref)
				}
			}
			digest := sha256.Sum256([]byte(rejected))
			value, _ := json.Marshal(map[string]any{"baseSHA256": hex.EncodeToString(digest[:]), "citationAssessmentsPatch": updates})
			patch = "```json\n" + string(value) + "\n```"
			answer = patch
		} else {
			t.Error("batch repair unexpectedly consumed another model turn")
			http.Error(w, "extra request", 400)
			return
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": answer}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: []research.Connector{batchQuoteConnector{}}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Batch quote repair fixture"})
	if err != nil {
		t.Fatal(err)
	}
	definition := referenceWorkflowTemplate(t, app, "trusted-research-closure").Definition
	definition.Name = "Batch quote repair fixture"
	definition.Nodes = append([]workflow.Node{}, definition.Nodes[:6]...)
	definition.Nodes[4].Arguments = json.RawMessage(`{"perDocument":true,"limit":20}`)
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","recommendation","recommendedReferences","citationAssessments"],"properties":{"summary":{"type":"string"},"recommendation":{"type":"string"},"recommendedReferences":{"type":"array","items":{"type":"string"}},"citationAssessments":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["reference","decision","sourceLevel","reason","supportingQuote"],"properties":{"reference":{"type":"string"},"decision":{"type":"string"},"sourceLevel":{"type":"string"},"reason":{"type":"string"},"supportingQuote":{"type":"string","maxLength":700}}}}}}`)
	var schemaObject map[string]any
	_ = json.Unmarshal(schema, &schemaObject)
	schemaObject["properties"].(map[string]any)["documentAnalyses"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
	schema, _ = json.Marshal(schemaObject)
	definition.Nodes = append(definition.Nodes, workflow.Node{ID: "evidence_screening", Name: "Compare supplied excerpts", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "selected-evidence-v6", Prompt: "Compare evidence and quote continuous original text.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: schema})
	kept := map[string]bool{"$input": true}
	for _, n := range definition.Nodes {
		kept[n.ID] = true
	}
	edges := []workflow.Edge{}
	for _, e := range definition.Edges {
		if kept[e.FromNode] && kept[e.ToNode] {
			edges = append(edges, e)
		}
	}
	definition.Edges = append(edges, workflow.Edge{FromNode: "knowledge", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"}, workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "select_citations", ToPort: "screening"})
	definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "sync", FromPort: "structured.documentIds", ToNode: "evidence_screening", ToPort: "documentIds"})
	definition.Outputs = []workflow.Output{{Name: "citations", Type: workflow.TypeCitations, FromNode: "select_citations", FromPort: "citations", Required: true}}
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: p.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: p.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"epsilon forty two"}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	waiting := driveToCandidateSelection(t, app, p.ID, started.Run.ID, 15*time.Second)
	step := currentWorkflowStep(t, waiting)
	var selectionInput struct {
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	if json.Unmarshal(step.Input, &selectionInput) != nil || len(selectionInput.Candidates) != 3 {
		t.Fatal("candidate inventory mismatch")
	}
	ids := []string{}
	for _, c := range selectionInput.Candidates {
		ids = append(ids, c.ID)
	}
	selection, _ := json.Marshal(map[string]any{"selectedCandidateIds": ids})
	if _, err := app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: p.ID, RunID: started.Run.ID, StepID: step.ID, Approved: true, Context: selection}); err != nil {
		t.Fatal(err)
	}
	finished := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 25*time.Second)
	var screen workflow.AIExecution
	for _, e := range finished.AIExecutions {
		if e.PromptVersion == "selected-evidence-v6" {
			screen = e
		}
	}
	mu.Lock()
	count, rawPatch := requests, patch
	mu.Unlock()
	if count != 2 || screen.Status != "completed" || screen.ModelTurns != 2 || screen.Attempt != 1 || screen.ChatRunID == "" {
		t.Fatalf("batch repair did not finish in the same attempt: requests=%d execution=%+v", count, screen)
	}
	var draft string
	if err := app.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=2`, screen.ChatRunID).Scan(&draft); err != nil || draft != rawPatch {
		t.Fatal("raw batch patch was not preserved", err)
	}
	var out struct {
		Summary     string
		Assessments []struct {
			Quote string `json:"supportingQuote"`
		} `json:"citationAssessments"`
	}
	if json.Unmarshal(screen.Output, &out) != nil || out.Summary != "Same fixture conclusion" || len(out.Assessments) < 3 {
		t.Fatal("lost merged full result")
	}
	for _, a := range out.Assessments {
		if strings.Contains(a.Quote, "Invented") {
			t.Fatal("unrepaired quote accepted")
		}
	}
	for _, s := range finished.Steps {
		if s.NodeID != "select_citations" && s.Attempt != 1 {
			t.Fatal("earlier stage rerun")
		}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	reloaded, err := app.WorkflowFacade.GetRun(p.ID, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Run.Status != finished.Run.Status || len(reloaded.AIExecutions) != len(finished.AIExecutions) || string(reloaded.AIExecutions[0].Output) != string(screen.Output) {
		t.Fatal("restart changed completed evidence")
	}
	t.Log("three citation issues corrected in one HTTP model turn; same execution, prior stages preserved, full merged result and raw patch survive restart")
}
