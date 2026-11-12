package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/research"
)

func TestGroupingDecisionsPersistExactBranchesAndSurviveRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "records.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	p, err := project.NewService(NewProjectRepository(s.DB()), filepath.Join(root, "workspace"), filepath.Join(root, "trash")).Create(ctx, "Grouping", "")
	if err != nil {
		t.Fatal(err)
	}
	repo := NewResearchRepository(s.DB())
	works := []research.Work{
		{SourceID: "crossref", SourceRecordID: "a", Title: "A sufficiently long shared title", Year: 2025, Identifiers: research.Identifiers{DOI: "10.1000/a"}},
		{SourceID: "openalex", SourceRecordID: "b", Title: "A sufficiently long shared title", Year: 2025, Identifiers: research.Identifiers{DOI: "https://doi.org/10.1000/A", OpenAlex: "W1"}},
		{SourceID: "crossref", SourceRecordID: "c", Title: "A sufficiently long shared title", Year: 2025, Identifiers: research.Identifiers{DOI: "10.1000/c", PMID: "123"}},
		{SourceID: "fixture", SourceRecordID: "conflict", Title: "Conflicting identifiers record", Year: 2025, Identifiers: research.Identifiers{DOI: "10.1000/a", PMID: "123"}},
		{SourceID: "fixture", SourceRecordID: "title1", Title: "Title only scientific record", Year: 2025},
		{SourceID: "fixture", SourceRecordID: "title2", Title: "Title-only scientific RECORD", Year: 2025},
		{SourceID: "fixture", SourceRecordID: "short", Title: "short", Year: 2025},
	}
	save := func(key string, w []research.Work) research.Query {
		t.Helper()
		q, e := repo.SaveSearch(ctx, p.ID, strings.Repeat(key, 64), research.SearchCommand{Query: "query", Limit: 10}, research.SearchResult{Query: "query", Works: w}, time.Now().UTC())
		if e != nil {
			t.Fatal(e)
		}
		return q
	}
	q := save("a", works)
	page, err := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	decisions := map[string]*research.RecordGrouping{}
	for _, candidate := range page.Items {
		for _, record := range candidate.Records {
			d := record.Grouping
			if d == nil || d.Version != research.RecordGroupingVersion || d.CandidateID != candidate.ID {
				t.Fatal("lost branch", record)
			}
			decisions[record.Work.SourceRecordID] = d
		}
	}
	for id, action := range map[string]string{"a": "create_unmatched", "b": "match_alias", "c": "create_unmatched", "conflict": "create_conflict", "title1": "create_unmatched", "title2": "create_unmatched", "short": "create_unmatched"} {
		if decisions[id].Action != action {
			t.Fatalf("%s: %+v", id, decisions[id])
		}
	}
	if len(decisions["b"].Matches) != 1 || decisions["b"].Matches[0].Kind != "doi" || decisions["b"].Matches[0].Value != "10.1000/a" || len(decisions["conflict"].Matches) != 2 || len(decisions["title2"].ComparedAliases) != 0 || len(decisions["short"].ComparedAliases) != 0 {
		t.Fatal(decisions)
	}
	for _, alias := range decisions["c"].ComparedAliases {
		if alias.Kind == "title_year" {
			t.Fatal("strong identifier used title fallback")
		}
	}
	before, _ := json.Marshal(page.Items)
	// A different query must describe existing-source reuse honestly; it must
	// not stamp a freshly inferred DOI decision onto the original snapshot.
	works[0].Identifiers.DOI = "10.1000/changed"
	q2 := save("b", works[:1])
	next, e := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q2.ID, Limit: 100})
	if e != nil || next.Items[0].Records[0].Grouping.Action != "reuse_source_record" {
		t.Fatal(next, e)
	}
	// Same-query replay is immutable, even with different incoming works.
	save("a", works[:1])
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	repo = NewResearchRepository(s.DB())
	page, err = repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, Limit: 100})
	after, _ := json.Marshal(page.Items)
	if err != nil || string(before) != string(after) {
		t.Fatal("frozen decisions changed after replay/restart", err)
	}
	// Failed transaction must not leave partial decision snapshots or sources.
	_, err = repo.SaveSearch(ctx, p.ID, strings.Repeat("c", 64), research.SearchCommand{Query: "bad", Limit: 10}, research.SearchResult{Query: "bad", Works: []research.Work{works[6], works[6]}}, time.Now().UTC())
	if err == nil {
		t.Fatal("expected duplicate source error")
	}
	var n int
	if err = s.DB().QueryRow(`SELECT count(*) FROM research_queries WHERE query_key=?`, strings.Repeat("c", 64)).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial transaction", n, err)
	}
}

func TestCapturedSearchRecordsReplayWithActualGroupingDecisions(t *testing.T) {
	dir := os.Getenv("SCIAIDE_GROUPING_CAPTURE")
	if dir == "" {
		t.Skip("optional read-only source snapshots")
	}
	b, e := os.ReadFile(filepath.Join(dir, "captured-search-works.json"))
	if e != nil {
		t.Fatal(e)
	}
	var works []research.Work
	if e = json.Unmarshal(b, &works); e != nil || len(works) != 54 {
		t.Fatal(len(works), e)
	}
	ctx := context.Background()
	root := t.TempDir()
	s, e := Open(ctx, filepath.Join(root, "replay.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p, e := project.NewService(NewProjectRepository(s.DB()), filepath.Join(root, "workspace"), filepath.Join(root, "trash")).Create(ctx, "Actual source replay", "")
	if e != nil {
		t.Fatal(e)
	}
	repo := NewResearchRepository(s.DB())
	q, e := repo.SaveSearch(ctx, p.ID, strings.Repeat("d", 64), research.SearchCommand{Query: "captured search replay", Limit: 50}, research.SearchResult{Query: "captured search replay", Works: works}, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	page, e := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	recorded := 0
	actions := map[string]int{}
	for _, c := range page.Items {
		for _, r := range c.Records {
			if r.Grouping == nil || r.Grouping.CandidateID != c.ID {
				t.Fatal("missing actual decision")
			}
			recorded++
			actions[r.Grouping.Action]++
		}
	}
	if recorded != 54 || len(page.Items) != 47 || actions["match_alias"] != 7 {
		t.Fatal(recorded, len(page.Items), actions)
	}
	data, _ := json.Marshal(page.Items)
	if e = os.WriteFile(filepath.Join(dir, "replayed-candidates.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("54 captured source records -> %d newly derived candidates, actual decisions=%v; isolated SQLite, not old-history backfill", len(page.Items), actions)
}
