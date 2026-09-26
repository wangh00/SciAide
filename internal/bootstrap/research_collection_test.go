package bootstrap

import (
	"context"
	"testing"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestTaskCandidateCollectionDoesNotChangeTaskInputs(t *testing.T) {
	ctx := context.Background()
	a, err := New(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p, err := a.ProjectFacade.CreateProject(struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		WorkspacePath string `json:"workspacePath"`
	}{Name: "Collection isolation"})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"collection-task", "other-task"} {
		_, err = a.store.DB().ExecContext(ctx, `INSERT INTO research_tasks(id,project_id,title,research_question,origin_kind,status,created_at,updated_at) VALUES (?,?,?,'question','ai_route','active',?,?)`, task, p.ID, task, time.Now().UTC(), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
	}
	r := sqlite.NewResearchRepository(a.store.DB())
	c := appresearch.SearchCommand{Query: "collection", SourceIDs: []string{"crossref"}, Limit: 20, ResearchTaskID: "collection-task"}
	q, err := r.SaveSearch(ctx, p.ID, appresearch.SearchQueryKey(c.Query, c.SourceIDs, c.Limit), c, appresearch.SearchResult{Query: c.Query, Works: []appresearch.Work{{SourceID: "crossref", SourceRecordID: "collection-record", Title: "Collected reference", Abstract: "An abstract for isolated collection."}}}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	page, err := a.ResearchFacade.ListCandidates(appresearch.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, ResearchTaskID: c.ResearchTaskID, Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("candidates=%+v %v", page, err)
	}
	id := page.Items[0].ID
	if _, err = a.ResearchFacade.CollectCandidate(p.ID, id, "other-task"); err == nil {
		t.Fatal("cross-task candidate accepted")
	}
	_, err = a.ResearchFacade.UpdateReview(appresearch.ReviewCommand{ProjectID: p.ID, CandidateID: id, ResearchTaskID: c.ResearchTaskID, Status: appresearch.ReviewExcluded, ExclusionReason: "Outside this task; save for another topic"})
	if err != nil {
		t.Fatal(err)
	}
	collected, err := a.ResearchFacade.CollectCandidate(p.ID, id, c.ResearchTaskID)
	if err != nil || !collected.Reusable {
		t.Fatalf("collect=%+v %v", collected, err)
	}
	page, err = a.ResearchFacade.ListCandidates(appresearch.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, ResearchTaskID: c.ResearchTaskID, Limit: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].ReviewStatus != appresearch.ReviewExcluded || page.Items[0].ImportStatus != appresearch.ImportNotImported || page.Items[0].AttachmentID != "" {
		t.Fatalf("collection changed task inputs: %+v %v", page, err)
	}
	var taskImports int
	if err = a.store.DB().QueryRowContext(ctx, `SELECT count(*) FROM research_candidate_task_imports WHERE research_task_id=?`, c.ResearchTaskID).Scan(&taskImports); err != nil || taskImports != 0 {
		t.Fatalf("task imports=%d %v", taskImports, err)
	}
	library, err := a.KnowledgeFacade.ListLibraryMaterials(p.ID)
	if err != nil || len(library) != 1 || library[0].ID != collected.ID {
		t.Fatalf("library=%+v %v", library, err)
	}
	if err = a.KnowledgeFacade.ArchiveMaterial(p.ID, "", collected.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := a.ResearchFacade.CollectCandidate(p.ID, id, c.ResearchTaskID)
	if err != nil || restored.ID != collected.ID || !restored.Reusable {
		t.Fatalf("restore=%+v %v", restored, err)
	}
}
