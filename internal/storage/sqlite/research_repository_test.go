package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

func TestResearchRepositoryDeduplicatesConservativelyAndPreservesRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	created, err := projects.Create(ctx, "Discovery", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewResearchRepository(store.DB())
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	result := appresearch.SearchResult{
		Query: "large language models science",
		Works: []appresearch.Work{
			{SourceID: "openalex", SourceRecordID: "W1", Title: "Large Language Models for Science", Abstract: "OpenAlex abstract", Year: 2025, Authors: []appresearch.Author{{Name: "Ada"}}, Identifiers: appresearch.Identifiers{DOI: "10.1000/Example", OpenAlex: "W1"}, RawSnapshot: json.RawMessage(`{"id":"W1","field":"openalex"}`)},
			{SourceID: "crossref", SourceRecordID: "10.1000/example", Title: "Large language models for science", Venue: "Journal", Year: 2025, Authors: []appresearch.Author{{Name: "Ada"}}, Identifiers: appresearch.Identifiers{DOI: "10.1000/example"}, RawSnapshot: json.RawMessage(`{"DOI":"10.1000/example","field":"crossref"}`)},
			{SourceID: "arxiv", SourceRecordID: "2501.00001", Title: "Large Language Models for Science", Abstract: "Different work without a matching year", Year: 2024, Authors: []appresearch.Author{{Name: "Grace"}}, Identifiers: appresearch.Identifiers{ArXiv: "2501.00001"}, RawSnapshot: json.RawMessage(`{"id":"2501.00001"}`)},
		},
		Sources: []appresearch.SourceSearch{{SourceID: "openalex", Status: appresearch.SearchOK, Count: 1}, {SourceID: "crossref", Status: appresearch.SearchOK, Count: 1}, {SourceID: "arxiv", Status: appresearch.SearchOK, Count: 1}},
	}
	command := appresearch.SearchCommand{Query: result.Query, SourceIDs: []string{"openalex", "crossref", "arxiv"}, Limit: 10}
	key := appresearch.SearchQueryKey(command.Query, command.SourceIDs, command.Limit)
	query, err := repository.SaveSearch(ctx, created.ID, key, command, result, at)
	if err != nil {
		t.Fatal(err)
	}
	values, err := repository.ListCandidates(ctx, created.ID, query.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 {
		t.Fatalf("candidates = %d, want 2: %#v", len(values), values)
	}
	var merged appresearch.Candidate
	for _, value := range values {
		if len(value.Records) == 2 {
			merged = value
		}
	}
	if merged.ID == "" || len(merged.Records) != 2 || merged.Preferred.Title == "" {
		t.Fatalf("merged candidate = %#v", merged)
	}
	if string(merged.Records[0].Work.RawSnapshot) == "" || string(merged.Records[1].Work.RawSnapshot) == "" {
		t.Fatal("source snapshots were not preserved")
	}
	if len(merged.Aliases) < 2 {
		t.Fatalf("aliases = %#v", merged.Aliases)
	}

	// Repeating the same query updates source snapshots without creating a
	// second query, source record, candidate or query-result relation.
	result.Works[0].Abstract = "Updated abstract"
	queryAgain, err := repository.SaveSearch(ctx, created.ID, key, command, result, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if queryAgain.ID != query.ID {
		t.Fatalf("query id changed: %s -> %s", query.ID, queryAgain.ID)
	}
	queries, err := repository.ListQueries(ctx, created.ID, 10)
	if err != nil || len(queries) != 1 || queries[0].ResultCount != 3 {
		t.Fatalf("queries = %#v, %v", queries, err)
	}
	values, err = repository.ListCandidates(ctx, created.ID, query.ID)
	if err != nil || len(values) != 2 {
		t.Fatalf("candidates after repeat = %#v, %v", values, err)
	}

	// A source record keeps its original aggregate identity when a later source
	// snapshot gains an identifier that is already attached to another work.
	originalArXivID := ""
	for _, value := range values {
		if len(value.Records) == 1 && value.Records[0].Work.SourceID == "arxiv" {
			originalArXivID = value.ID
		}
	}
	result.Works[2].Identifiers.DOI = "10.1000/example"
	if _, err := repository.SaveSearch(ctx, created.ID, key, command, result, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	values, err = repository.ListCandidates(ctx, created.ID, query.ID)
	if err != nil || len(values) != 2 {
		t.Fatalf("candidates after identifier conflict = %#v, %v", values, err)
	}
	for _, value := range values {
		if value.ID == originalArXivID && (len(value.Records) != 1 || value.Records[0].Work.SourceID != "arxiv") {
			t.Fatalf("existing source record changed aggregate identity: %#v", value)
		}
	}
}

func TestResearchRepositoryPersistsReviewAndImportState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "research-state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	created, err := projects.Create(ctx, "Review", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewResearchRepository(store.DB())
	at := time.Now().UTC()
	command := appresearch.SearchCommand{Query: "replication", SourceIDs: []string{"pubmed"}, Limit: 5}
	result := appresearch.SearchResult{Query: command.Query, Works: []appresearch.Work{{SourceID: "pubmed", SourceRecordID: "123", Title: "Replication", Year: 2024, Authors: []appresearch.Author{}, Identifiers: appresearch.Identifiers{PMID: "123"}, RawSnapshot: json.RawMessage(`{"uid":"123"}`)}}, Sources: []appresearch.SourceSearch{{SourceID: "pubmed", Status: appresearch.SearchOK, Count: 1}}}
	query, err := repository.SaveSearch(ctx, created.ID, appresearch.SearchQueryKey(command.Query, command.SourceIDs, command.Limit), command, result, at)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := repository.ListCandidates(ctx, created.ID, query.ID)
	candidate := values[0]
	candidate, err = repository.UpdateReview(ctx, appresearch.ReviewCommand{ProjectID: created.ID, CandidateID: candidate.ID, Status: appresearch.ReviewExcluded, ExclusionReason: "不符合纳入标准", Note: "保留供审计"}, at.Add(time.Minute))
	if err != nil || candidate.ReviewStatus != appresearch.ReviewExcluded || candidate.ExclusionReason == "" {
		t.Fatalf("review = %#v, %v", candidate, err)
	}
	candidate, err = repository.UpdateImportState(ctx, appresearch.ImportStateCommand{ProjectID: created.ID, CandidateID: candidate.ID, Status: appresearch.ImportFailed, Kind: appresearch.ImportMetadataAbstract, ErrorMessage: "fixture", At: at.Add(2 * time.Minute)})
	if err != nil || candidate.ImportStatus != appresearch.ImportFailed || candidate.ImportError != "fixture" {
		t.Fatalf("import state = %#v, %v", candidate, err)
	}
}

func TestResearchRepositoryDoesNotMergeDifferentStrongIdentifiersByTitle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "research-strong-id.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	created, err := projects.Create(ctx, "Strong IDs", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewResearchRepository(store.DB())
	command := appresearch.SearchCommand{Query: "same title", SourceIDs: []string{"crossref", "arxiv"}, Limit: 5}
	result := appresearch.SearchResult{Query: command.Query, Works: []appresearch.Work{
		{SourceID: "crossref", SourceRecordID: "10.1/a", Title: "The Same Long Scientific Title", Year: 2025, Identifiers: appresearch.Identifiers{DOI: "10.1/a"}},
		{SourceID: "arxiv", SourceRecordID: "2501.1", Title: "The Same Long Scientific Title", Year: 2025, Identifiers: appresearch.Identifiers{ArXiv: "2501.1"}},
	}, Sources: []appresearch.SourceSearch{{SourceID: "crossref", Status: appresearch.SearchOK, Count: 1}, {SourceID: "arxiv", Status: appresearch.SearchOK, Count: 1}}}
	query, err := repository.SaveSearch(ctx, created.ID, appresearch.SearchQueryKey(command.Query, command.SourceIDs, command.Limit), command, result, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	values, err := repository.ListCandidates(ctx, created.ID, query.ID)
	if err != nil || len(values) != 2 {
		t.Fatalf("strong identifiers were merged by title: %#v, %v", values, err)
	}
}

func TestResearchBibliographyPreservesFieldSourcesAndRevisionHistory(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "bibliography.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := t.TempDir()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	created, err := projects.Create(ctx, "Bibliography", "")
	if err != nil {
		t.Fatal(err)
	}
	repository := NewResearchRepository(store.DB())
	at := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
	command := appresearch.SearchCommand{Query: "canonical record", SourceIDs: []string{"crossref", "openalex"}, Limit: 5}
	result := appresearch.SearchResult{Query: command.Query, Works: []appresearch.Work{
		{SourceID: "crossref", SourceRecordID: "10.1000/ABC", Title: "Canonical Record", Authors: []appresearch.Author{{Name: "Ada Lovelace", ORCID: "0000-0001"}}, Year: 2024, Venue: "Journal A", Volume: "7", Issue: "2", Pages: "10-20", Identifiers: appresearch.Identifiers{DOI: "https://doi.org/10.1000/ABC"}, LandingURL: "https://doi.org/10.1000/ABC"},
		{SourceID: "openalex", SourceRecordID: "W123", Title: "Canonical record", Authors: []appresearch.Author{{Name: "Ada Lovelace"}}, Year: 2025, Venue: "Journal B", Identifiers: appresearch.Identifiers{DOI: "10.1000/abc", OpenAlex: "https://openalex.org/W123"}, LandingURL: "https://openalex.org/W123"},
	}, Sources: []appresearch.SourceSearch{{SourceID: "crossref", Status: appresearch.SearchOK, Count: 1}, {SourceID: "openalex", Status: appresearch.SearchOK, Count: 1}}}
	query, err := repository.SaveSearch(ctx, created.ID, appresearch.SearchQueryKey(command.Query, command.SourceIDs, command.Limit), command, result, at)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := repository.ListCandidates(ctx, created.ID, query.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%#v err=%v", candidates, err)
	}
	bibliography, err := repository.GetBibliography(ctx, created.ID, candidates[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if bibliography.Data.DOI != "10.1000/abc" || bibliography.Data.OpenAlex != "W123" || len(bibliography.FieldSources) < 10 {
		t.Fatalf("bibliography=%#v", bibliography)
	}
	yearSources := 0
	for _, source := range bibliography.FieldSources {
		if source.Field == "year" {
			yearSources++
		}
	}
	if yearSources != 2 {
		t.Fatalf("year field sources=%d", yearSources)
	}

	next := bibliography.Data
	next.Year = 2023
	next.Title = "User-corrected title"
	revised, err := repository.ReviseBibliography(ctx, appresearch.ReviseBibliographyCommand{ProjectID: created.ID, CandidateID: candidates[0].ID, Data: next, Reason: "checked publisher page"}, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if revised.Revision != 2 || revised.Data.Year != 2023 || revised.Data.Title != "User-corrected title" || len(revised.Revisions) != 2 || revised.SelectedSources["year"] != "user" {
		t.Fatalf("revised bibliography=%#v", revised)
	}

	var openAlexRecord string
	for _, record := range candidates[0].Records {
		if record.Work.SourceID == "openalex" {
			openAlexRecord = record.ID
		}
	}
	selected, err := repository.SelectBibliographySource(ctx, appresearch.SelectBibliographySourceCommand{ProjectID: created.ID, CandidateID: candidates[0].ID, Field: "year", SourceRecordID: openAlexRecord, Reason: "prefer indexed source"}, at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if selected.Revision != 3 || selected.Data.Year != 2025 || selected.SelectedSources["year"] != "source:"+openAlexRecord || len(selected.Revisions) != 3 {
		t.Fatalf("selected bibliography=%#v", selected)
	}
}
