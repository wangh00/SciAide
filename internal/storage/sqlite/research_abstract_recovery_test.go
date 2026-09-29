package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/research/materializer"
	"github.com/wangh00/SciAide/internal/researchtext"
	"github.com/wangh00/SciAide/internal/tools/builtin"
)

func TestLegacyAbstractRecoveryThroughTaskImportPreservesSnapshots(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "projects"), filepath.Join(t.TempDir(), "trash"))
	p, err := projects.Create(ctx, "Abstract recovery", "")
	if err != nil {
		t.Fatal(err)
	}
	tasks := NewResearchTaskRepository(store.DB())
	for _, id := range []string{"owner", "other"} {
		if err = tasks.Upsert(ctx, researchtask.UpsertCommand{ID: id, ProjectID: p.ID, Title: id, ResearchQuestion: "yoga", OriginKind: researchtask.OriginAI, At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	a := attachment.NewService(NewAttachmentRepository(store.DB()), projects)
	a.SetTaskValidator(tasks)
	kb := knowledge.NewService(NewKnowledgeRepository(store.DB()), projects, a)
	kb.SetTaskValidator(tasks)
	repo := NewResearchRepository(store.DB())
	rs, _ := research.NewService(nil)
	s, _ := research.NewDiscoveryService(rs, repo, projects)
	s.SetTaskValidator(tasks)
	m := materializer.New()
	if err = s.SetImportPipeline(m, a, kb); err != nil {
		t.Fatal(err)
	}
	source := `<h4>Results</h4>β=-0.72, p<.005), each additional class improved 0.72 points.<h4>Conclusion</h4>Association.`
	raw, _ := json.Marshal(map[string]string{"id": "123", "source": "MED", "abstractText": source})
	w := research.Work{SourceID: "europepmc", SourceRecordID: "MED/123", Title: "Heated yoga", Abstract: researchtext.Legacy(source), RawSnapshot: raw, Identifiers: research.Identifiers{PMID: "123"}}
	q, err := repo.SaveSearch(ctx, p.ID, research.SearchQueryKey("yoga", nil, 20), research.SearchCommand{Query: "yoga", ResearchTaskID: "owner", Limit: 20}, research.SearchResult{Query: "yoga", Works: []research.Work{w}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, ResearchTaskID: "owner", QueryID: q.ID, Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	id := page.Items[0].ID
	c, err := s.UpdateReview(ctx, research.ReviewCommand{ProjectID: p.ID, ResearchTaskID: "owner", CandidateID: id, Status: research.ReviewIncluded})
	if err != nil {
		t.Fatal(err)
	}
	// Emulate the old imported bytes, without the raw snapshot recovery path.
	legacy := c
	legacy.Records = append([]research.SourceRecord(nil), c.Records...)
	for i := range legacy.Records {
		legacy.Records[i].Work.RawSnapshot = nil
	}
	file, err := m.Materialize(ctx, p, legacy, research.MaterializeMetadata)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup(file)
	old, err := a.ImportResearchStagedForTask(ctx, p.ID, file.Path, file.Name, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = kb.Enqueue(ctx, old); err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateCandidateTaskImport(ctx, research.ImportStateCommand{ProjectID: p.ID, CandidateID: id, ResearchTaskID: "owner", Status: research.ImportImported, Kind: research.ImportMetadataAbstract, AttachmentID: old.ID, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.CandidateForTask(ctx, p.ID, id, "owner")
	if err != nil {
		t.Fatal(err)
	}
	oldDocs, err := kb.SynchronizeAttachmentsForTask(ctx, p.ID, "owner", []string{old.ID})
	if err != nil {
		t.Fatal(err)
	}
	search := builtin.NewSearchKnowledge(kb)
	invoke := func(doc string) tool.Result {
		args, _ := json.Marshal(map[string]any{"query": "yoga", "documentIds": []string{doc}})
		v, e := search.Invoke(ctx, tool.Invocation{RunID: "workflow", ProjectID: p.ID, ResearchTaskID: "owner", Arguments: args})
		if e != nil || len(v.Citations) == 0 {
			t.Fatal(v, e)
		}
		return v
	}
	oldRefs := invoke(oldDocs[0].ID)
	cmd := research.ImportCandidateCommand{ProjectID: p.ID, ResearchTaskID: "owner", CandidateID: id, Mode: research.MaterializeAuto}
	updated, err := s.ImportCandidate(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Attachment.ID == old.ID || updated.Candidate.ImportKind != research.ImportMetadataAbstract {
		t.Fatal("no corrected version", updated)
	}
	_, parsed, err := a.ParsedForTask(ctx, p.ID, "owner", updated.Attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, u := range parsed.Units {
		text += u.Content
	}
	if !strings.Contains(text, "p<.005") || !strings.Contains(text, "additional class") {
		t.Fatal("statistics still damaged", text)
	}
	again, err := s.ImportCandidate(ctx, cmd)
	if err != nil || again.Attachment.ID != updated.Attachment.ID {
		t.Fatal("reimport not idempotent", err)
	}
	after, err := repo.CandidateForTask(ctx, p.ID, id, "owner")
	if err != nil || after.Preferred.Abstract != before.Preferred.Abstract {
		t.Fatal("historical discovery snapshot overwritten", err)
	}
	newDocs, err := kb.SynchronizeAttachmentsForTask(ctx, p.ID, "owner", []string{updated.Attachment.ID})
	if err != nil {
		t.Fatal(err)
	}
	refs := invoke(newDocs[0].ID)
	if refs.Citations[0].Reference == oldRefs.Citations[0].Reference || refs.Citations[0].AttachmentID != updated.Attachment.ID {
		t.Fatal("new evidence reused stale citation")
	}
	for _, ref := range oldRefs.Citations {
		if _, err = kb.ReadEvidenceChunkForTask(ctx, p.ID, "owner", ref.IndexVersionID, ref.DocumentID, ref.AttachmentID, ref.ChunkID); err != nil {
			t.Fatal("old citation lost", err)
		}
	}
	cmd.ResearchTaskID = "other"
	if _, err = s.ImportCandidate(ctx, cmd); err == nil {
		t.Fatal("cross-task recovery allowed")
	}
	// Recovery after a cancelled save uses metadata content identity too.
	if err = repo.RecordMaterialIntent(ctx, p.ID, id, "owner", updated.Attachment.SHA256, research.ImportMetadataAbstract); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.RecoverMaterialIntent(ctx, p.ID, id, "owner"); err != nil || !ok {
		t.Fatal("metadata intent recovery", ok, err)
	}
	// Once full text exists, an automatic import must never downgrade it to a
	// recovered abstract, even while historical discovery still has old text.
	full := &fullTextMaterialFixture{}
	if err = s.SetImportPipeline(full, a, kb); err != nil {
		t.Fatal(err)
	}
	cmd.ResearchTaskID, cmd.Mode = "owner", research.MaterializeFullText
	pdf, err := s.ImportCandidate(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetImportPipeline(m, a, kb); err != nil {
		t.Fatal(err)
	}
	cmd.Mode = research.MaterializeAuto
	retained, err := s.ImportCandidate(ctx, cmd)
	if err != nil || retained.Attachment.ID != pdf.Attachment.ID || retained.Candidate.ImportKind != research.ImportFullText {
		t.Fatal("recovery downgraded full text", err)
	}
}
