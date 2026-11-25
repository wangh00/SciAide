package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
)

func workingBasisRead(f *workingStateFixture, id string) workflow.ResearchWorkingAbstractPage {
	f.t.Helper()
	ctx := context.Background()
	repo := NewResearchRepository(f.store.DB())
	text := strings.Repeat("摘要🧪 尚未核验，不能当作论文全文。", 20)
	q, err := repo.SaveSearch(ctx, "p", fmt.Sprintf("%x", sha256.Sum256([]byte(id))), research.SearchCommand{Query: id, ResearchTaskID: "task", SourceIDs: []string{"eric"}, Limit: 1}, research.SearchResult{Query: id, Works: []research.Work{{SourceID: "eric", SourceRecordID: "EJ1234567", Title: "Saved abstract", Abstract: text}}}, f.now)
	if err != nil {
		f.t.Fatal(err)
	}
	rows, err := repo.TaskDiscoveryPage(ctx, research.CandidateListCommand{ProjectID: "p", ResearchTaskID: "task", QueryID: q.ID, Limit: 1})
	if err != nil || len(rows.Items) != 1 {
		f.t.Fatal("missing query fixture", err)
	}
	p := workflow.ResearchWorkingAbstractPage{Status: "unverified_discovery_field", SnapshotView: "preferred_record", QueryID: q.ID, CandidateID: rows.Items[0].ID, Field: "abstract", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), Available: true, Offset: 10, NextOffset: 74, TotalRunes: len([]rune(text)), Content: string([]rune(text)[10:74])}
	args, _ := json.Marshal(map[string]any{"queryId": p.QueryID, "candidateId": p.CandidateID, "field": "abstract", "offset": p.Offset, "limit": 64})
	at := formatTime(f.now)
	f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at) VALUES (?,'chat',?,?,'1',?,'running','low','[]',1,?,?)`, id, "provider-"+id, workflow.ResearchDiscoveryReadTool, string(args), at, at)
	raw, _ := json.Marshal(p)
	if err := NewToolRepository(f.store.DB()).Finish(ctx, id, tool.CallRunning, tool.CallCompleted, tool.Result{Status: tool.ResultSuccess, Structured: raw, CreatedAt: f.now}, "", "", f.now); err != nil {
		f.t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	return p
}

func TestResearchWorkingBasisPreservesExplicitRelationsAndExactPage(t *testing.T) {
	f := newWorkingStateFixture(t)
	p := workingBasisRead(f, "read-page")
	first := publishWorkingPatch(f, "first", "chat", `{"expectedVersion":0,"currentFocus":"比较研究条件","rationale":"模型工作判断，未核验","basis":{"rationale":["read-page"]}}`)
	ctx := context.Background()
	got, err := f.repo.GetResearchWorkingStateBasis(ctx, "p", "task", 1, "rationale", "read-page")
	if err != nil || got.StateID != first.ID || got.Basis.Status != "linked_unverified_abstract" || got.Content != p.Content || got.Basis.Offset != 10 || got.Basis.EndOffset != 74 {
		t.Fatal("original page not reopened", got, err)
	}
	before := databaseRows(t, f.store.DB(), "tool_results")
	publishWorkingPatch(f, "second", "chat", `{"expectedVersion":1,"nextStep":"核对冲突，不自动批准"}`)
	linked, err := f.repo.GetResearchWorkingStateBasis(ctx, "p", "task", 2, "rationale", "read-page")
	if err != nil || linked.Content != p.Content {
		t.Fatal("sparse inheritance lost explicit relation", linked, err)
	}
	for _, bad := range []struct {
		p, task   string
		v         int
		field, id string
	}{{"other", "task", 2, "rationale", "read-page"}, {"p", "missing", 2, "rationale", "read-page"}, {"p", "task", 1, "rationale", "read-page"}, {"p", "task", 2, "nextStep", "read-page"}, {"p", "task", 2, "rationale", "missing"}} {
		if _, err := f.repo.GetResearchWorkingStateBasis(ctx, bad.p, bad.task, bad.v, bad.field, bad.id); err == nil {
			t.Fatal("foreign/stale/unselected page accepted", bad)
		}
	}
	// Same-value explicit resubmission without basis does not silently reassert it.
	publishWorkingPatch(f, "third", "chat", `{"expectedVersion":2,"rationale":"模型工作判断，未核验"}`)
	sources, err := f.repo.GetResearchWorkingStateProvenance(ctx, "p", "task", 3)
	if err != nil || len(sources.Fields[1].Basis) != 0 {
		t.Fatal("unlinked replacement inherited support", sources, err)
	}
	if _, err := f.repo.GetResearchWorkingStateBasis(ctx, "p", "task", 3, "rationale", "read-page"); err == nil {
		t.Fatal("old relation surfaced under new explicit submission")
	}
	after := databaseRows(t, f.store.DB(), "tool_results")
	if !reflect.DeepEqual(before[0], after[0]) {
		t.Fatal("reading or linking mutated original source result")
	}
}

func TestResearchWorkingBasisRejectsUnprovenSources(t *testing.T) {
	for name, change := range map[string]func(*workingStateFixture, workflow.ResearchWorkingAbstractPage){
		"failed": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_calls SET status='failed' WHERE id='read-page'`)
		},
		"workflow call": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_calls SET run_id=NULL,workflow_run_id='task' WHERE id='read-page'`)
		},
		"result error": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_results SET status='error' WHERE tool_call_id='read-page'`)
		},
		"foreign task": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`DELETE FROM research_query_origins`)
		},
		"foreign run": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`INSERT INTO conversations(id,project_id,title,created_at,updated_at) VALUES ('foreign','other','Foreign',?,?)`, formatTime(f.now), formatTime(f.now))
			f.chat("foreign-chat", "foreign", "foreign-human")
			f.exec(`UPDATE tool_calls SET run_id='foreign-chat' WHERE id='read-page'`)
		},
		"missing human": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE messages SET status='incomplete' WHERE id='human'`)
		},
		"late completion": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_calls SET completed_at=? WHERE id='read-page'`, formatTime(f.now.Add(time.Hour)))
		},
		"late row": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_calls SET rowid=rowid+1000 WHERE id='read-page'`)
		},
		"missing snapshot": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE research_queries SET snapshot_version=0`)
		},
		"truncated": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_results SET truncated=1 WHERE tool_call_id='read-page'`)
		},
		"projection missing": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_results SET model_context_text='{}' WHERE tool_call_id='read-page'`)
		},
		"projection mismatch": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_results SET model_context_text=json_set(model_context_text,'$.structured.content',?) WHERE tool_call_id='read-page'`, strings.Repeat("错", 64))
		},
		"selector mismatch": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_calls SET arguments_json=json_set(arguments_json,'$.offset',0) WHERE id='read-page'`)
		},
		"content mismatch": func(f *workingStateFixture, p workflow.ResearchWorkingAbstractPage) {
			p.Content = strings.Repeat("错", 64)
			raw, _ := json.Marshal(p)
			f.exec(`UPDATE tool_results SET structured_json=? WHERE tool_call_id='read-page'`, string(raw))
		},
		"hash mismatch": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE research_query_candidates SET snapshot_json=json_set(snapshot_json,'$.preferred.abstract','replacement')`)
		},
		"title not abstract": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_results SET structured_json=json_set(structured_json,'$.field','title') WHERE tool_call_id='read-page'`)
		},
		"index not page": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_results SET structured_json='{"status":"unverified_discovery_index"}' WHERE tool_call_id='read-page'`)
		},
		"empty abstract": func(f *workingStateFixture, _ workflow.ResearchWorkingAbstractPage) {
			f.exec(`UPDATE tool_results SET structured_json=json_set(structured_json,'$.content','','$.available',json('false')) WHERE tool_call_id='read-page'`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWorkingStateFixture(t)
			p := workingBasisRead(f, "read-page")
			cmd := workingPatch(t, `{"expectedVersion":0,"currentFocus":"focus","rationale":"why","basis":{"rationale":["read-page"]}}`)
			f.call("note", "chat", cmd)
			change(f, p)
			if _, err := f.save("note", "chat", cmd); err == nil {
				t.Fatal("unproven basis saved")
			}
			var count int
			if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM research_working_states`).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected basis wrote a version", count, err)
			}
		})
	}
}

func TestResearchWorkingBasisMissingHistoryStaysUnavailable(t *testing.T) {
	f := newWorkingStateFixture(t)
	workingBasisRead(f, "read-page")
	publishWorkingPatch(f, "note", "chat", `{"expectedVersion":0,"currentFocus":"focus","rationale":"why","basis":{"rationale":["read-page"]}}`)
	f.exec(`UPDATE tool_results SET model_context_text='{}' WHERE tool_call_id='read-page'`)
	sources, err := f.repo.GetResearchWorkingStateProvenance(context.Background(), "p", "task", 1)
	if err != nil || sources.Fields[1].Status != "recorded" || len(sources.Fields[1].Basis) != 1 || sources.Fields[1].Basis[0].Status != "unavailable" {
		t.Fatal("missing original read repaired as support", sources, err)
	}
	if _, err := f.repo.GetResearchWorkingStateBasis(context.Background(), "p", "task", 1, "rationale", "read-page"); err == nil {
		t.Fatal("missing page replaced")
	}
}

func TestResearchWorkingBasisArchiveRemapsOnlyExplicitIdentities(t *testing.T) {
	f := newWorkingStateFixture(t)
	p := workingBasisRead(f, "read-page")
	publishWorkingPatch(f, "note", "chat", `{"expectedVersion":0,"currentFocus":"read-page","rationale":"queryId and read-page are scientific text, not identity slots","basis":{"rationale":["read-page"]}}`)
	// Failed and malformed arguments are historical audit data, not identities
	// that may be repaired or made valid during restore.
	bad := `{"expectedVersion":1,"rationale":"bad audit","basis":{"rationale":["read-page"],"rationale":["missing-read"]}}`
	f.call("failed-note", "chat", workingCommand(1, "audit"))
	f.exec(`UPDATE tool_calls SET arguments_json=? WHERE id='failed-note'`, bad)
	if err := f.finish("failed-note", false); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at)
 SELECT 'failed-read',run_id,'provider-failed-read',tool_name,tool_version,arguments_json,'running',risk,permissions_json,idempotent,created_at,updated_at FROM tool_calls WHERE id='read-page'`)
	if err := f.finish("failed-read", false); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO tool_calls(id,run_id,provider_call_id,tool_name,tool_version,arguments_json,status,risk,permissions_json,idempotent,created_at,updated_at)
 SELECT 'incomplete-read',run_id,'provider-incomplete-read',tool_name,tool_version,arguments_json,'running',risk,permissions_json,idempotent,created_at,updated_at FROM tool_calls WHERE id='read-page'`)
	if err := f.finish("incomplete-read", true); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE tool_results SET structured_json=NULL WHERE tool_call_id='incomplete-read'`)
	var failedArgs string
	if err := f.store.DB().QueryRow(`SELECT arguments_json FROM tool_calls WHERE id='failed-read'`).Scan(&failedArgs); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE runs SET status='completed' WHERE id='chat'`)
	ctx := context.Background()
	archive := NewProjectArchiveRepository(f.store.DB())
	file := filepath.Join(t.TempDir(), "basis.db")
	if _, err := archive.CreateSnapshot(ctx, "p", file); err != nil {
		t.Fatal(err)
	}
	restored := project.Project{ID: "restored", Name: "Restored", WorkspacePath: t.TempDir(), CreatedAt: f.now, UpdatedAt: f.now}
	plan, err := archive.RewriteSnapshot(ctx, file, restored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.MergeSnapshot(ctx, file, nil); err != nil {
		t.Fatal(err)
	}
	sources, err := f.repo.GetResearchWorkingStateProvenance(ctx, "restored", plan.TaskIDs["task"], 1)
	if err != nil || len(sources.Fields) != 4 || len(sources.Fields[1].Basis) != 1 {
		t.Fatal("restored basis missing", sources, err)
	}
	basis := sources.Fields[1].Basis[0]
	if basis.Status != "linked_unverified_abstract" || basis.ReadCallID == "read-page" || basis.QueryID == p.QueryID || basis.CandidateID == p.CandidateID {
		t.Fatal("restored basis retained live identities or vanished", basis)
	}
	page, err := f.repo.GetResearchWorkingStateBasis(ctx, "restored", plan.TaskIDs["task"], 1, "rationale", basis.ReadCallID)
	if err != nil || page.Content != p.Content {
		t.Fatal("restored relation cannot reopen original page", page, err)
	}
	note, err := f.repo.GetResearchWorkingState(ctx, "restored", plan.TaskIDs["task"])
	if err != nil || note.CurrentFocus != "read-page" || note.Rationale != "queryId and read-page are scientific text, not identity slots" {
		t.Fatal("archive rewrote scientific text", note, err)
	}
	if _, err := f.repo.GetResearchWorkingStateBasis(ctx, "p", "task", 1, "rationale", "read-page"); err != nil {
		t.Fatal("restore changed live original", err)
	}
	for _, raw := range []string{bad, failedArgs} {
		var n int
		if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE arguments_json=? AND status='failed'`, raw).Scan(&n); err != nil || n != 2 {
			t.Fatal("restore normalized failed audit parameters", n, err)
		}
	}
}
