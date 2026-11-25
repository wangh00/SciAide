package sqlite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestTaskDiscoveryPageRequiresOwnedOriginalSnapshot(t *testing.T) {
	f := newWorkingStateFixture(t)
	ctx := context.Background()
	repo := NewResearchRepository(f.store.DB())
	save := func(key, task, title string) research.Query {
		q, err := repo.SaveSearch(ctx, "p", fmt.Sprintf("%x", sha256.Sum256([]byte(key))), research.SearchCommand{Query: key, Limit: 10, SourceIDs: []string{"eric"}, ResearchTaskID: task}, research.SearchResult{Query: key, Works: []research.Work{{SourceID: "eric", SourceRecordID: "EJ1234567", Title: title}}}, f.now)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	q := save("original", "task", "original source title")
	manual := save("manual", "", "later merged title must not replace old query")
	command := research.CandidateListCommand{ProjectID: "p", ResearchTaskID: "task", QueryID: q.ID, Limit: 3}
	page, err := repo.TaskDiscoveryPage(ctx, command)
	if err != nil || len(page.Items) != 1 || page.Items[0].Preferred.Title != "original source title" {
		t.Fatal("query read used mutable merged metadata", page, err)
	}
	candidateID := page.Items[0].ID
	snapshot, err := repo.TaskDiscoveryCandidate(ctx, "p", "task", q.ID, candidateID)
	if err != nil || snapshot.Preferred.Title != "original source title" {
		t.Fatal("field reader used the later merged record", snapshot, err)
	}
	for _, ids := range [][4]string{{"other", "task", q.ID, candidateID}, {"p", "foreign", q.ID, candidateID}, {"p", "task", manual.ID, candidateID}, {"p", "task", q.ID, "missing"}} {
		if _, err = repo.TaskDiscoveryCandidate(ctx, ids[0], ids[1], ids[2], ids[3]); err == nil {
			t.Fatal("foreign or missing field snapshot accepted", ids)
		}
	}
	otherQuery, err := repo.SaveSearch(ctx, "p", fmt.Sprintf("%x", sha256.Sum256([]byte("other-candidate"))), research.SearchCommand{Query: "other candidate", ResearchTaskID: "task", SourceIDs: []string{"eric"}, Limit: 1}, research.SearchResult{Query: "other candidate", Works: []research.Work{{SourceID: "eric", SourceRecordID: "EJ7654321", Title: "Another candidate"}}}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	otherPage, err := repo.TaskDiscoveryPage(ctx, research.CandidateListCommand{ProjectID: "p", ResearchTaskID: "task", QueryID: otherQuery.ID, Limit: 1})
	if err != nil || len(otherPage.Items) != 1 {
		t.Fatal("other-query fixture missing", err)
	}
	if _, err = repo.TaskDiscoveryCandidate(ctx, "p", "task", q.ID, otherPage.Items[0].ID); err == nil {
		t.Fatal("candidate from another query accepted")
	}
	for _, c := range []research.CandidateListCommand{{ProjectID: "p", ResearchTaskID: "task", QueryID: manual.ID, Limit: 3}, {ProjectID: "other", ResearchTaskID: "task", QueryID: q.ID, Limit: 3}, {ProjectID: "p", ResearchTaskID: "foreign", QueryID: q.ID, Limit: 3}, {ProjectID: "p", ResearchTaskID: "task", QueryID: "missing", Limit: 3}} {
		if _, err = repo.TaskDiscoveryPage(ctx, c); err == nil {
			t.Fatal("foreign or missing query accepted", c)
		}
	}
	at := formatTime(f.now)
	f.exec(`INSERT INTO research_queries(id,project_id,query_text,query_key,source_ids_json,limit_per_source,source_statuses_json,partial,created_at,updated_at) VALUES ('legacy','p','legacy',?,'[]',10,'[]',0,?,?)`, fmt.Sprintf("%x", sha256.Sum256([]byte("legacy"))), at, at)
	f.exec(`INSERT INTO research_query_origins(query_id,task_id,task_title) VALUES ('legacy','task','Research')`)
	legacy := command
	legacy.QueryID = "legacy"
	if _, err = repo.TaskDiscoveryPage(ctx, legacy); err == nil {
		t.Fatal("legacy query without original snapshot reconstructed as evidence")
	}
	if _, err = repo.TaskDiscoveryCandidate(ctx, "p", "task", "legacy", candidateID); err == nil {
		t.Fatal("field reader reconstructed a missing legacy snapshot")
	}
	f.exec(`UPDATE research_tasks SET status='archived',archived_at=? WHERE id='task'`, at)
	if _, err = repo.TaskDiscoveryPage(ctx, command); err == nil {
		t.Fatal("archived task accepted")
	}
	if _, err = repo.TaskDiscoveryCandidate(ctx, "p", "task", q.ID, candidateID); err == nil {
		t.Fatal("archived task field read accepted")
	}
}

func TestTaskDiscoveryRequiresRealCurrentDiscussionCall(t *testing.T) {
	f := newWorkingStateFixture(t)
	ctx := context.Background()
	at := formatTime(f.now)
	for i, name := range []string{workflow.ResearchDiscoverySearchTool, workflow.ResearchDiscoveryReadTool} {
		id := []string{"search", "read"}[i]
		f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at) VALUES (?,'chat',?,?,'1','{}','running','low','[]',1,?,?)`, id, id, name, at, at)
		if err := f.repo.ValidateResearchDiscussionTool(ctx, "p", "task", "chat", id, name); err != nil {
			t.Fatal(err)
		}
		for _, ids := range [][4]string{{"other", "task", "chat", id}, {"p", "foreign", "chat", id}, {"p", "task", "foreign", id}, {"p", "task", "chat", "missing"}} {
			if err := f.repo.ValidateResearchDiscussionTool(ctx, ids[0], ids[1], ids[2], ids[3], name); err == nil {
				t.Fatal("forged discovery context accepted", ids)
			}
		}
	}
	if err := f.repo.ValidateResearchDiscussionTool(ctx, "p", "task", "chat", "read", workflow.ResearchDiscoverySearchTool); err == nil {
		t.Fatal("another tool's call identity accepted")
	}
	f.exec(`UPDATE runs SET status='cancelled',completed_at=? WHERE id='chat'`, at)
	if err := f.repo.ValidateResearchDiscussionTool(ctx, "p", "task", "chat", "read", workflow.ResearchDiscoveryReadTool); err == nil {
		t.Fatal("cancelled chat accepted")
	}
}
