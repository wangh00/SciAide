package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/projectarchive"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	research "github.com/wangh00/SciAide/internal/app/research"
)

func TestResearchNewRecordsRequireUnconflictedStrongIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		left, right research.Identifiers
		want        int
	}{
		{name: "title_year_is_not_identity", want: 2},
		{name: "same_doi", left: research.Identifiers{DOI: "10.1000/a"}, right: research.Identifiers{DOI: "10.1000/a"}, want: 1},
		{name: "shared_pmid_conflicting_doi", left: research.Identifiers{DOI: "10.1000/a", PMID: "123"}, right: research.Identifiers{DOI: "10.1000/b", PMID: "123"}, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, e := Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			p, e := project.NewService(NewProjectRepository(s.DB()), filepath.Join(t.TempDir(), "workspace"), filepath.Join(t.TempDir(), "trash")).Create(ctx, "Identity", "")
			if e != nil {
				t.Fatal(e)
			}
			repo := NewResearchRepository(s.DB())
			works := []research.Work{{SourceID: "fixture", SourceRecordID: "left", Title: "Same generic scientific title", Year: 2025, Identifiers: tc.left}, {SourceID: "fixture", SourceRecordID: "right", Title: "Same generic scientific title", Year: 2025, Identifiers: tc.right}}
			q, e := repo.SaveSearch(ctx, p.ID, strings.Repeat("a", 64), research.SearchCommand{Query: "identity", Limit: 10}, research.SearchResult{Query: "identity", Works: works}, time.Now().UTC())
			if e != nil {
				t.Fatal(e)
			}
			page, e := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, Limit: 100})
			if e != nil {
				t.Fatal(e)
			}
			if len(page.Items) != tc.want {
				t.Fatalf("got %d candidates, want %d; unverified records were merged", len(page.Items), tc.want)
			}
		})
	}
}

func TestRecordIdentitySnapshotsAndProjectArchivePreserveScopedEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "data", "identity.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	s, e := Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	projects := project.NewService(NewProjectRepository(s.DB()), filepath.Join(root, "data", "workspaces"), filepath.Join(root, "backups", "trash"))
	p, e := projects.Create(ctx, "Identity archive", "")
	if e != nil {
		t.Fatal(e)
	}
	repo := NewResearchRepository(s.DB())
	now := time.Now().UTC()
	if e := NewResearchTaskRepository(s.DB()).Upsert(ctx, researchtask.UpsertCommand{ID: "task", ProjectID: p.ID, Title: "Identity task", OriginKind: researchtask.OriginAI, At: now}); e != nil {
		t.Fatal(e)
	}
	work := func(id string) research.Work {
		return research.Work{SourceID: "fixture", SourceRecordID: id, Title: "Undergraduate writing feedback and revision quality", Abstract: strings.Repeat("Students revised essays using structured feedback in this study. ", 5), Year: 2024, Authors: []research.Author{{Name: "Doe, Jane"}}}
	}
	save := func(key string, works []research.Work) research.Query {
		t.Helper()
		q, e := repo.SaveSearch(ctx, p.ID, strings.Repeat(key, 64), research.SearchCommand{Query: "identity", Limit: 10, ResearchTaskID: "task"}, research.SearchResult{Query: "identity", Works: works}, now)
		if e != nil {
			t.Fatal(e)
		}
		return q
	}
	q1 := save("a", []research.Work{work("A"), work("B")})
	q2 := save("b", []research.Work{work("A"), work("C")})
	page, e := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q1.ID, ResearchTaskID: "task", Limit: 100})
	if e != nil || len(page.Items) != 2 {
		t.Fatal(e, page)
	}
	before, _ := json.Marshal(page.Items)
	var first research.Candidate
	for _, c := range page.Items {
		if c.Records[0].Work.SourceRecordID == "A" {
			first = c
		}
	}
	merged, e := repo.CandidateForTask(ctx, p.ID, first.ID, "task")
	if e != nil || len(merged.RecordIdentity.Scopes) != 2 || len(merged.RecordIdentity.Relations) != 2 {
		t.Fatal("scoped union lost original inventories", e, merged)
	}
	// Mutable source updates and same-query replay cannot rewrite Q1.
	changed := work("A")
	changed.Abstract = "Changed latest metadata"
	save("c", []research.Work{changed})
	save("a", []research.Work{changed})
	page, e = repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q1.ID, ResearchTaskID: "task", Limit: 100})
	after, _ := json.Marshal(page.Items)
	if e != nil || string(before) != string(after) {
		t.Fatal("query snapshot changed", e)
	}
	// Archive is a real exported/restored SQLite project, not a JSON-only fixture.
	archive := newProjectArchiveTestService(t, s, projects, root)
	file := filepath.Join(root, "identity.sciaide-project")
	if _, e := archive.Export(ctx, p.ID, file); e != nil {
		t.Fatal(e)
	}
	restored, e := archive.Restore(ctx, projectarchive.RestoreCommand{Path: file})
	if e != nil {
		t.Fatal(e)
	}
	queries, e := repo.ListQueries(ctx, restored.Project.ID, 20)
	if e != nil || len(queries) != 3 {
		t.Fatal(e, queries)
	}
	found := false
	for _, q := range queries {
		next, e := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: restored.Project.ID, QueryID: q.ID, Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		if len(next.Items) != 2 {
			continue
		}
		for _, c := range next.Items {
			if c.Records[0].Work.SourceRecordID != "A" {
				continue
			}
			if c.RecordIdentity.Scopes[0].InputSHA256 == first.RecordIdentity.Scopes[0].InputSHA256 {
				found = true
				if c.ID == first.ID || research.RecordIdentityContentSHA256(c.RecordIdentity) != research.RecordIdentityContentSHA256(first.RecordIdentity) {
					t.Fatal("archive changed portable content or reused local ID")
				}
				target := c.RecordIdentity.Relations[0].TargetCandidateID
				if target == first.RecordIdentity.Relations[0].TargetCandidateID {
					t.Fatal("target reference not remapped")
				}
				if _, e := repo.GetCandidate(ctx, restored.Project.ID, target); e != nil {
					t.Fatal("remapped target does not exist", e)
				}
			}
		}
	}
	if !found {
		t.Fatal("original query inventory not recovered")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = nil
	s, e = Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	repo = NewResearchRepository(s.DB())
	page, e = repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q1.ID, ResearchTaskID: "task", Limit: 100})
	after, _ = json.Marshal(page.Items)
	if e != nil || string(after) != string(before) {
		t.Fatal("restart drift", e)
	}
	_ = q2
}
func TestRecordGroupingConflictWitnessStorageStaysLinear(t *testing.T) {
	ctx := context.Background()
	s, e := Open(ctx, filepath.Join(t.TempDir(), "pressure.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p, e := project.NewService(NewProjectRepository(s.DB()), t.TempDir(), t.TempDir()).Create(ctx, "Grouping pressure", "")
	if e != nil {
		t.Fatal(e)
	}
	works := make([]research.Work, 400)
	for i := range works {
		works[i] = research.Work{SourceID: "fixture", SourceRecordID: fmt.Sprint(i), Title: "Conflicting source identifiers", Identifiers: research.Identifiers{DOI: fmt.Sprintf("10.1000/work%d", i), PMID: "123"}}
	}
	repo := NewResearchRepository(s.DB())
	start := time.Now()
	q, e := repo.SaveSearch(ctx, p.ID, strings.Repeat("p", 64), research.SearchCommand{Query: "pressure", Limit: 50}, research.SearchResult{Query: "pressure", Works: works}, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	for offset := 0; offset < 400; offset += 100 {
		page, e := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, Offset: offset, Limit: 100})
		if e != nil || page.Total != 400 {
			t.Fatal(e, page.Total)
		}
		for _, c := range page.Items {
			d := c.Records[0].Grouping
			if len(d.Matches) > 4 || len(d.Conflicts) > 2 {
				t.Fatal("unbounded branch witnesses", d)
			}
			for _, n := range d.MatchCounts {
				if n.Total > 2 && n.Retained != 2 {
					t.Fatal(n)
				}
			}
		}
	}
	var size int
	if e = s.DB().QueryRow(`SELECT sum(length(snapshot_json)) FROM research_query_candidates WHERE query_id=?`, q.ID).Scan(&size); e != nil || size > 3000*1024 {
		t.Fatal("quadratic snapshots", size, e)
	}
	t.Logf("400 conflicting records: snapshot bytes=%d, elapsed=%s", size, time.Since(start))
}
func TestPublicCapturedIdentityExamplesReplayWithoutMergingOrPromoting(t *testing.T) {
	capture := os.Getenv("SCIAIDE_IDENTITY_CAPTURE")
	if capture == "" {
		t.Skip("optional read-only public metadata capture")
	}
	b, e := os.ReadFile(capture)
	if e != nil {
		t.Fatal(e)
	}
	var data struct {
		Rows []struct {
			Search research.SearchResult `json:"search"`
		} `json:"rows"`
	}
	if e = json.Unmarshal(b, &data); e != nil {
		t.Fatal(e)
	}
	wanted := map[string]bool{"EJ1423132": true, "322057997": true, "321704296": true, "153317574": true, "148727828": true}
	works := []research.Work{}
	for _, r := range data.Rows {
		for _, w := range r.Search.Works {
			if wanted[w.SourceRecordID] {
				works = append(works, w)
				delete(wanted, w.SourceRecordID)
			}
		}
	}
	if len(wanted) != 0 || len(works) != 5 {
		t.Fatal("capture missing fixed cases", wanted)
	}
	ctx := context.Background()
	s, e := Open(ctx, filepath.Join(t.TempDir(), "public-replay.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p, e := project.NewService(NewProjectRepository(s.DB()), t.TempDir(), t.TempDir()).Create(ctx, "Public identity replay", "")
	if e != nil {
		t.Fatal(e)
	}
	repo := NewResearchRepository(s.DB())
	q, e := repo.SaveSearch(ctx, p.ID, strings.Repeat("r", 64), research.SearchCommand{Query: "public metadata replay", Limit: 50}, research.SearchResult{Query: "public metadata replay", Works: works}, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	page, e := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, Limit: 100})
	if e != nil || page.Total != 5 {
		t.Fatal("metadata-only works merged", e, page.Total)
	}
	for _, c := range page.Items {
		id := c.Records[0].Work.SourceRecordID
		h := c.RecordIdentity
		switch id {
		case "EJ1423132", "322057997", "153317574", "148727828":
			if len(h.Relations) != 1 || h.Relations[0].Kind != "possible_same_work" {
				t.Fatal("expected unverified match", id, h)
			}
		case "321704296":
			if len(h.Roles) != 1 || h.Roles[0].Kind != "companion_material" || len(h.Relations) != 0 {
				t.Fatal("invented main article or missed supplement", h)
			}
		}
		if c.ReviewStatus != research.ReviewPending || c.ImportStatus != research.ImportNotImported {
			t.Fatal("promoted unverified metadata", c)
		}
	}
	t.Log("5 captured source records remain 5 candidates; 2 same-work pairs are hints only; supplement has no invented primary record")
}

func TestRecordIdentityArchivePayloadMapsNewReferencesWithoutChangingContentDigest(t *testing.T) {
	maps := newArchiveIDMaps()
	maps.values["research_candidates"] = map[string]string{"a": "new-a", "b": "new-b"}
	maps.values["research_queries"] = map[string]string{"q": "new-q"}
	h := &research.RecordIdentity{Version: research.RecordIdentityVersion, Relations: []research.RecordRelationHint{{Kind: "identifier_conflict", TargetCandidateID: "b"}}}
	before := research.RecordIdentityContentSHA256(h)
	raw, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"id": "a", "sourceSha256Version": workflow.LiteratureCandidateDigestVersion, "recordIdentity": h}}, "queryIds": []string{"q"}, "importedMaterials": []any{map[string]any{"candidateId": "a", "recordIdentity": map[string]any{"relatedCandidateIds": []string{"b"}}}}, "legacy": map[string]any{"id": "a"}})
	out, e := rewriteWorkflowPayload(raw, maps, "p", "new-p")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Candidates []struct {
			ID       string                   `json:"id"`
			Identity *research.RecordIdentity `json:"recordIdentity"`
		} `json:"candidates"`
		Queries []string `json:"queryIds"`
		Legacy  struct {
			ID string `json:"id"`
		} `json:"legacy"`
	}
	if e = json.Unmarshal(out, &v); e != nil {
		t.Fatal(e)
	}
	if v.Candidates[0].ID != "new-a" || v.Candidates[0].Identity.Relations[0].TargetCandidateID != "new-b" || v.Queries[0] != "new-q" || v.Legacy.ID != "a" || research.RecordIdentityContentSHA256(v.Candidates[0].Identity) != before || !strings.Contains(string(out), `"relatedCandidateIds":["new-b"]`) {
		t.Fatal("archive changed content identity or missed scoped IDs", string(out))
	}
}
