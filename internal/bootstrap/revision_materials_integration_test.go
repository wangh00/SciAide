package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestSupplementalLiteratureDiscussionConfirmationAndNewEvidence(t *testing.T) {
	var mu sync.Mutex
	var supplementID string
	stageServer := newResearchStarterOutputTestServer(t, func(out map[string]any, prompt string) {
		if _, ok := out["query"]; ok {
			out["query"] = "sleep"
		}
		input := workflowAITestStageInput(prompt)
		if _, ok := input["_selectedEvidence"]; !ok {
			return
		}
		analyses := []any{}
		refs := []string{}
		assessments := []any{}
		byDoc := map[string][]string{}
		for _, raw := range input["candidates"].([]any) {
			c := raw.(map[string]any)
			ref := c["reference"].(string)
			doc := c["documentId"].(string)
			refs = append(refs, ref)
			byDoc[doc] = append(byDoc[doc], ref)
			quote := []rune(c["quote"].(string))
			if len(quote) > 200 {
				quote = quote[:200]
			}
			assessments = append(assessments, map[string]any{"reference": ref, "decision": "support", "sourceLevel": "unknown", "reason": "Fixture source evidence", "supportingQuote": string(quote)})
		}
		for _, raw := range input["documentIds"].([]any) {
			doc := raw.(string)
			r := byDoc[doc]
			if len(r) > 3 {
				r = r[:3]
			}
			if r == nil {
				r = []string{}
			}
			analyses = append(analyses, map[string]any{"documentId": doc, "finding": "Source reviewed for sleep study", "limitations": "Fixture, not real scientific validation", "references": r})
		}
		out["documentAnalyses"], out["recommendedReferences"], out["citationAssessments"] = analyses, refs, assessments
		out["summary"] = "Evidence reviewed"
		out["recommendation"] = "proceed"
		out["limitations"] = []string{"Fixture evidence"}
		out["coverage"] = map[string]any{"strength": "adequate", "sufficientForClaimedScope": true, "independentStudyEstimate": 1, "directPopulationEvidence": true, "fullTextEvidenceAvailable": false, "gaps": []string{}}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"research_revision_propose"`)) {
			r.Body = io.NopCloser(bytes.NewReader(body))
			stageServer.Config.Handler.ServeHTTP(w, r)
			return
		}
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &request)
		last := -1
		for i, m := range request.Messages {
			if m.Role == "user" {
				last = i
			}
		}
		hasTool := false
		for _, m := range request.Messages[last+1:] {
			hasTool = hasTool || m.Role == "tool"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if hasTool {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"请确认补充文献返修方案。\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		mu.Lock()
		aid := supplementID
		mu.Unlock()
		args, _ := json.Marshal(workflow.ProposeResearchRevisionCommand{NodeID: "evidence_import", Summary: "Check newly supplied sleep evidence", Changes: []string{"Compare the new study with prior conclusions"}, Reason: "New evidence must be indexed and synthesized, not patched into report text", AttachmentIDs: []string{aid}})
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "supplement-call", "function": map[string]any{"name": "research_revision_propose", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	root := t.TempDir()
	app, err := New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	p, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Supplement integration"})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "sleep.md")
	if err := os.WriteFile(file, []byte("# Sleep evidence\nSleep diary design measures sleep duration daily."), 0600); err != nil {
		t.Fatal(err)
	}
	batch, err := app.AttachmentFacade.ImportDocumentPaths(p.ID, []string{file})
	if err != nil || len(batch.Attachments) != 1 {
		t.Fatal(batch, err)
	}
	started, err := app.WorkflowFacade.StartResearch(workflow.StartResearchCommand{ProjectID: p.ID, ResearchIdea: "Design a sleep diary study using supplied literature", ReferenceAttachmentIDs: []string{batch.Attachments[0].ID}, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	planned := waitForWorkflowState(t, app, p.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
	adopted, err := app.WorkflowFacade.AdoptResearchRoute(workflow.AdoptResearchRouteCommand{ProjectID: p.ID, RunID: planned.Run.ID, RouteID: "design_first"})
	if err != nil || adopted.Run == nil {
		t.Fatal(adopted, err)
	}
	// Explicit local attachments now pass candidate selection automatically.
	completed := driveSupplementFixture(t, app, p.ID, adopted.Run.Run.ID)
	reused := false
	for _, event := range completed.Events {
		reused = reused || event.Type == "workflow.materials_reused"
	}
	if !reused {
		t.Fatal("selected materials were not reused")
	}

	before := workflow.ResearchRevisionSnapshot(completed)
	supp := filepath.Join(t.TempDir(), "new-sleep.md")
	os.WriteFile(supp, []byte("# New sleep evidence\nSleep diaries may systematically underestimate awake time; compare objective sleep measures."), 0600)
	added, err := app.AttachmentFacade.ImportConversationDocumentPaths(p.ID, completed.Run.ConversationID, []string{supp})
	if err != nil || len(added.Attachments) != 1 {
		t.Fatal(added, err)
	}
	mu.Lock()
	supplementID = added.Attachments[0].ID
	mu.Unlock()
	repo := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	ids, err := repo.DiscussionAttachmentIDs(context.Background(), completed.Run.ID)
	if err != nil || len(ids) != 0 {
		t.Fatal("unsent file visible", ids, err)
	}
	run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: completed.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: "Please revise using this new sleep study", AttachmentIDs: []string{supplementID}})
	if err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		snap, e := app.ChatFacade.GetRunSnapshot(run.ID)
		if e != nil {
			t.Fatal(e)
		}
		if snap.Run.Status == chat.RunCompleted {
			for _, call := range snap.ToolCalls {
				if call.Status != "completed" {
					t.Fatal(call.ErrorMessage)
				}
			}
			break
		}
		if time.Now().After(end) || snap.Run.Status == chat.RunFailed {
			t.Fatal("discussion", snap.Run)
		}
	}
	d, err := app.WorkflowFacade.GetRun(p.ID, completed.Run.ID)
	if err != nil || len(d.RevisionProposals) != 1 {
		t.Fatal(d.RevisionProposals, err)
	}
	proposal := d.RevisionProposals[0]
	if len(proposal.Materials) != 1 || !proposal.CanConfirm || workflow.ResearchRevisionSnapshot(d) != before {
		t.Fatal("proposal mutated delivery", proposal)
	}
	page, err := app.workflow.ReadResearchTask(context.Background(), run.ID, workflow.ResearchTaskReadCommand{Section: "supplemental_content", AttachmentID: supplementID})
	if err != nil || !strings.Contains(string(page), "underestimate") {
		t.Fatal(string(page), err)
	}
	if _, err = app.workflow.ReadResearchTask(context.Background(), run.ID, workflow.ResearchTaskReadCommand{Section: "supplemental_content", AttachmentID: batch.Attachments[0].ID}); err == nil {
		t.Fatal("unsent shared file read as supplemental")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(Options{RootDir: root, EventPublisher: workflowAITestPublisher{}})
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	restored, err := app.WorkflowFacade.GetRun(p.ID, d.Run.ID)
	if err != nil || len(restored.RevisionProposals) != 1 || !restored.RevisionProposals[0].CanConfirm || restored.RevisionProposals[0].Materials[0].SHA256 != added.Attachments[0].SHA256 {
		t.Fatal("restart lost pending material snapshot", err)
	}
	if _, err = app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: p.ID, RunID: d.Run.ID, ProposalID: proposal.ID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	revised := driveSupplementFixture(t, app, p.ID, d.Run.ID)
	for _, step := range revised.Steps {
		for _, old := range completed.Steps {
			if step.NodeID == old.NodeID && step.NodeID == "candidate_review" && step.Attempt != old.Attempt {
				t.Fatal("candidate selection repeated")
			}
		}
	}
	var full workflow.Step
	for _, step := range revised.Steps {
		if step.NodeID == "evidence_import" {
			full = step
		}
	}
	if !strings.Contains(string(full.Output), "new-sleep.md") {
		t.Fatal("new material absent from import", string(full.Output))
	}
	var promotedID string
	if err := app.store.DB().QueryRow(`SELECT id FROM attachments WHERE project_id=? AND scope_kind='task' AND research_task_id=? AND sha256=?`, p.ID, d.Run.ResearchTaskID, added.Attachments[0].SHA256).Scan(&promotedID); err != nil {
		t.Fatal(err)
	}
	if promotedID == supplementID {
		t.Fatal("conversation attachment owner was mutated")
	}
	var screening workflow.Step
	for _, step := range revised.Steps {
		if step.NodeID == "evidence_screening" {
			screening = step
		}
	}
	if !strings.Contains(string(screening.Input), promotedID) || !strings.Contains(string(screening.Input), "underestimate") {
		t.Fatal("supplement absent from citation-backed synthesis")
	}
	var count int
	if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM message_citations WHERE attachment_id=?`, promotedID).Scan(&count); err != nil || count == 0 {
		t.Fatal("supplement never issued a citation", count, err)
	}
	// A fresh content-equivalent conversation upload never creates another task
	// material identity on replay, and wrong task IDs remain rejected.
	for _, target := range []string{"evidence_screening", "report_drafting"} {
		if _, err := app.workflow.ProposeResearchRevision(context.Background(), run.ID, "unused", workflow.ProposeResearchRevisionCommand{NodeID: target, Summary: "Wrong start", Changes: []string{"Add paper"}, Reason: "Fixture", AttachmentIDs: []string{supplementID}}); err == nil {
			t.Fatal("new evidence allowed to bypass indexing", target)
		}
	}
	var snapshots int
	if err := app.store.DB().QueryRow(`SELECT COUNT(*) FROM research_revision_proposals WHERE workflow_run_id=? AND status='confirmed' AND previous_snapshot_json<>'{}'`, d.Run.ID).Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatal("old report lost", err)
	}
}

func driveSupplementFixture(t *testing.T, app *Application, projectID, runID string) workflow.RunDetail {
	t.Helper()
	for end := time.Now().Add(40 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		d, err := app.WorkflowFacade.GetRun(projectID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Run.Status == workflow.RunCompleted {
			return d
		}
		if d.Run.Status == workflow.RunFailed || d.Run.Status == workflow.RunInterrupted {
			t.Fatal(d.Run.ErrorMessage)
		}
		if d.Run.Status == workflow.RunWaitingHumanConfirmation {
			s := currentWorkflowStep(t, d)
			if s.NodeKind != workflow.NodeCitationSelection {
				t.Fatal("unexpected confirmation", s.NodeID)
			}
			var in struct {
				Candidates []tool.CitationRef `json:"candidates"`
			}
			json.Unmarshal(s.Input, &in)
			selected, _ := json.Marshal(in.Candidates)
			if _, err := app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: projectID, RunID: runID, StepID: s.ID, Approved: true, Context: selected, AcceptLimitedEvidence: true}); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Fatal("supplement workflow timed out")
	return workflow.RunDetail{}
}
