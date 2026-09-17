package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryDeletionOnlyBlocksRelatedResumableTask(t *testing.T) {
	h := newWorkflowRuntimeHarness(t)
	saved := h.save(t, workflow.Definition{SchemaVersion: 1, Name: "Deletion boundary", Nodes: []workflow.Node{{ID: "wait", Name: "Wait", Kind: workflow.NodeHumanConfirmation, Prompt: "fixture", Arguments: json.RawMessage(`{}`)}}})
	ctx := context.Background()
	started, err := h.runtime.Start(ctx, workflow.StartCommand{ProjectID: h.project.ID, WorkflowID: saved.Workflow.ID, ResearchTaskID: workflow.NewResearchTaskID, Inputs: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	waiting := waitWorkflowStatus(t, h.runtime, h.project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation)
	if err := h.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	repo := NewResearchRepository(h.store.DB())
	save := func(label, task string) research.Query {
		q, err := repo.SaveSearch(ctx, h.project.ID, research.SearchQueryKey(label, nil, 20), research.SearchCommand{Query: label, Limit: 20, ResearchTaskID: task}, research.SearchResult{Query: label}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	manual := save("manual", "")
	if err := repo.DeleteQueryHistory(ctx, research.DeleteQueriesCommand{ProjectID: h.project.ID, QueryIDs: []string{manual.ID}}); err != nil {
		t.Fatal("unrelated task blocked deletion", err)
	}
	owned := save("owned", waiting.Run.ResearchTaskID)
	if err := repo.DeleteQueryHistory(ctx, research.DeleteQueriesCommand{ProjectID: h.project.ID, QueryIDs: []string{owned.ID}}); err == nil {
		t.Fatal("resumable task discovery deleted")
	}
	if _, err := h.store.DB().ExecContext(ctx, `UPDATE workflow_runs SET status='failed' WHERE id=?`, waiting.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteQueryHistory(ctx, research.DeleteQueriesCommand{ProjectID: h.project.ID, QueryIDs: []string{owned.ID}}); err == nil {
		t.Fatal("failed retryable task discovery deleted")
	}
	if _, err := h.store.DB().ExecContext(ctx, `UPDATE workflow_runs SET status='cancelled' WHERE id=?`, waiting.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteQueryHistory(ctx, research.DeleteQueriesCommand{ProjectID: h.project.ID, QueryIDs: []string{owned.ID}}); err != nil {
		t.Fatal("cancelled task discovery cannot be deleted", err)
	}
	owned = save("archive-owned", waiting.Run.ResearchTaskID)
	manual = save("retained-manual", "")
	if _, err := h.store.DB().ExecContext(ctx, `DELETE FROM workflow_runs WHERE id=?`, waiting.Run.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM research_queries WHERE id=?`, owned.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted task retained discovery", count, err)
	}
	if err := h.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM research_queries WHERE id=?`, manual.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("manual discovery removed", count, err)
	}
}

func TestDiscoverySnapshotsTaskReviewsAndHistoricalPaging(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "discovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	p, err := projects.Create(ctx, "Discovery isolation", "")
	if err != nil {
		t.Fatal(err)
	}
	repo := NewResearchRepository(store.DB())
	at := time.Now().UTC()
	for _, task := range []string{"a", "b"} {
		if _, err := store.DB().ExecContext(ctx, `INSERT INTO research_tasks(id,project_id,title,research_question,origin_kind,status,created_at,updated_at) VALUES(?,?,?,'question','ai_route','active',?,?)`, task, p.ID, task, formatTime(at), formatTime(at)); err != nil {
			t.Fatal(err)
		}
	}
	search := func(task, label string) research.Query {
		works := []research.Work{}
		for i := 0; i < 125; i++ {
			works = append(works, research.Work{SourceID: "pubmed", SourceRecordID: fmt.Sprint(i), Title: fmt.Sprintf("Paper %03d", i), Abstract: label, Year: 2000 + i, Identifiers: research.Identifiers{PMID: fmt.Sprint(i + 1)}})
		}
		c := research.SearchCommand{Query: label, Limit: 50, ResearchTaskID: task}
		q, err := repo.SaveSearch(ctx, p.ID, research.SearchQueryKey(label, nil, 50), c, research.SearchResult{Query: label, Works: works}, at)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	a := search("a", "Original abstract")
	b := search("b", "Later abstract")
	page, err := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: a.ID, Limit: 20, Offset: 100, Sort: "year_desc"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 125 || len(page.Items) != 20 || page.Items[0].Preferred.Year != 2024 || page.Items[0].Preferred.Abstract != "Original abstract" {
		t.Fatalf("snapshot page: %#v", page)
	}
	candidate := page.Items[0]
	_, err = repo.UpdateReview(ctx, research.ReviewCommand{ProjectID: p.ID, ResearchTaskID: "a", CandidateID: candidate.ID, Status: research.ReviewExcluded, ExclusionReason: "A-only", Note: "private review"}, at)
	if err != nil {
		t.Fatal(err)
	}
	av, err := repo.CandidateForTask(ctx, p.ID, candidate.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	bv, err := repo.CandidateForTask(ctx, p.ID, candidate.ID, "b")
	if err != nil {
		t.Fatal(err)
	}
	if av.ReviewStatus != research.ReviewExcluded || bv.ReviewStatus != research.ReviewPending || bv.Note != "" || bv.Preferred.Abstract != "Later abstract" {
		t.Fatal("cross-task state leak", av, bv)
	}
	if _, err := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: a.ID, ResearchTaskID: "b", Limit: 20}); err == nil {
		t.Fatal("wrong query owner accepted")
	}
	filtered, err := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: a.ID, Status: research.ReviewExcluded, Limit: 20})
	if err != nil || filtered.Total != 1 {
		t.Fatal(filtered, err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM research_tasks WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateReview(ctx, research.ReviewCommand{ProjectID: p.ID, ResearchTaskID: "a", CandidateID: candidate.ID, Status: research.ReviewIncluded}, at); err == nil {
		t.Fatal("deleted task allowed review mutation")
	}
	history, err := repo.QueryHistory(ctx, research.QueryPageCommand{ProjectID: p.ID, Origin: "a", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || !history[0].TaskDeleted || history[0].LegacySnapshot {
		t.Fatal(history)
	}
	archiveService := newProjectArchiveTestService(t, store, projects, t.TempDir())
	archivePath := filepath.Join(t.TempDir(), "discovery.sciaide-project")
	if _, err := archiveService.Export(ctx, p.ID, archivePath); err != nil {
		t.Fatal(err)
	}
	restored, err := archiveService.Restore(ctx, projectarchive.RestoreCommand{Path: archivePath})
	if err != nil {
		t.Fatal(err)
	}
	restoredHistory, err := repo.QueryHistory(ctx, research.QueryPageCommand{ProjectID: restored.Project.ID, Limit: 50})
	if err != nil || len(restoredHistory) != 2 {
		t.Fatal(restoredHistory, err)
	}
	for _, q := range restoredHistory {
		p, err := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: restored.Project.ID, QueryID: q.ID, Limit: 100})
		if err != nil || p.Total != 125 {
			t.Fatal(p, err)
		}
		for _, c := range p.Items {
			if c.ProjectID != restored.Project.ID || c.Records[0].ProjectID != restored.Project.ID {
				t.Fatal("snapshot identity not remapped")
			}
		}
	}
	page, err = repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: a.ID, ResearchTaskID: "a", Limit: 20})
	if err != nil || page.Total != 125 {
		t.Fatal(page, err)
	}
	if err := repo.DeleteQueryHistory(ctx, research.DeleteQueriesCommand{ProjectID: p.ID, QueryIDs: []string{a.ID, "missing"}}); err == nil {
		t.Fatal("partial deletion accepted")
	}
	history, err = repo.QueryHistory(ctx, research.QueryPageCommand{ProjectID: p.ID, Origin: "a", Limit: 50})
	if err != nil || len(history) != 1 {
		t.Fatal("transaction was not atomic", err)
	}
	if err := repo.DeleteQueryCandidates(ctx, research.DeleteCandidatesCommand{ProjectID: p.ID, QueryID: a.ID, CandidateIDs: []string{candidate.ID, "missing"}}); err == nil {
		t.Fatal("partial candidate removal accepted")
	}
	page, err = repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: a.ID, Limit: 20})
	if err != nil || page.Total != 125 {
		t.Fatal("candidate removal was not atomic", err)
	}
	if err := repo.DeleteQueryCandidates(ctx, research.DeleteCandidatesCommand{ProjectID: p.ID, QueryID: a.ID, CandidateIDs: []string{candidate.ID}}); err != nil {
		t.Fatal(err)
	}
	page, err = repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: b.ID, Limit: 20})
	if err != nil || page.Total != 125 {
		t.Fatal("other query's candidates changed", err)
	}
	if err := repo.DeleteQueryHistory(ctx, research.DeleteQueriesCommand{ProjectID: p.ID, Origin: "a"}); err != nil {
		t.Fatal(err)
	}
	history, err = repo.QueryHistory(ctx, research.QueryPageCommand{ProjectID: p.ID, Origin: "b", Limit: 50})
	if err != nil || len(history) != 1 || history[0].ID != b.ID {
		t.Fatal("other group removed", err)
	}
}
