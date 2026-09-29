package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/researchtask"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/tools/builtin"
)

type fullTextMaterialFixture struct {
	calls       int
	fail        bool
	unavailable bool
}

type fullTextUnavailableFixture struct{}

func (fullTextUnavailableFixture) Error() string             { return "fixture publisher unavailable" }
func (fullTextUnavailableFixture) FullTextUnavailable() bool { return true }

func (f *fullTextMaterialFixture) Materialize(ctx context.Context, p project.Project, c research.Candidate, mode research.MaterializeMode) (research.MaterializedCandidate, error) {
	f.calls++
	if f.unavailable {
		return research.MaterializedCandidate{}, fullTextUnavailableFixture{}
	}
	if f.fail {
		return research.MaterializedCandidate{}, fmt.Errorf("fixture network unavailable")
	}
	text, name, kind := []byte("# Abstract\nOriginal summary without detailed weight results."), "paper-metadata.md", research.ImportMetadataAbstract
	if mode == research.MaterializeFullText {
		name, kind = "paper.pdf", research.ImportFullText
		stream := "BT /F1 12 Tf 72 720 Td (Detailed weight result was 42 units with uncertainty.) Tj ET"
		objects := []string{`<< /Type /Catalog /Pages 2 0 R >>`, `<< /Type /Pages /Kids [3 0 R] /Count 1 >>`, `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>`, `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)}
		var b bytes.Buffer
		b.WriteString("%PDF-1.4\n")
		offsets := []int{0}
		for i, o := range objects {
			offsets = append(offsets, b.Len())
			fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
		}
		x := b.Len()
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
		for _, v := range offsets[1:] {
			fmt.Fprintf(&b, "%010d 00000 n \n", v)
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), x)
		text = b.Bytes()
	}
	dir := filepath.Join(project.PrivateDataPath(p), "tmp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return research.MaterializedCandidate{}, err
	}
	file, err := os.CreateTemp(dir, "research-import-*")
	if err != nil {
		return research.MaterializedCandidate{}, err
	}
	defer file.Close()
	if _, err = file.Write(text); err != nil {
		return research.MaterializedCandidate{}, err
	}
	return research.MaterializedCandidate{Path: file.Name(), Name: name, Kind: kind, SHA256: fmt.Sprintf("%x", sha256.Sum256(text))}, nil
}
func (*fullTextMaterialFixture) Cleanup(v research.MaterializedCandidate) { _ = os.Remove(v.Path) }

func TestFullTextRefreshPersistsReindexesAndReissuesWithoutDestroyingAbstract(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(NewProjectRepository(store.DB()), filepath.Join(t.TempDir(), "workspaces"), filepath.Join(t.TempDir(), "trash"))
	p, err := projects.Create(ctx, "Evidence refresh", "")
	if err != nil {
		t.Fatal(err)
	}
	tasks := NewResearchTaskRepository(store.DB())
	if err = tasks.Upsert(ctx, researchtask.UpsertCommand{ID: "task", ProjectID: p.ID, Title: "test", ResearchQuestion: "weight", OriginKind: researchtask.OriginAI, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	attachments := attachment.NewService(NewAttachmentRepository(store.DB()), projects)
	attachments.SetTaskValidator(tasks)
	kb := knowledge.NewService(NewKnowledgeRepository(store.DB()), projects, attachments)
	kb.SetTaskValidator(tasks)
	repo := NewResearchRepository(store.DB())
	rs, _ := research.NewService(nil)
	discovery, _ := research.NewDiscoveryService(rs, repo, projects)
	discovery.SetTaskValidator(tasks)
	materializer := &fullTextMaterialFixture{}
	if err = discovery.SetImportPipeline(materializer, attachments, kb); err != nil {
		t.Fatal(err)
	}
	command := research.SearchCommand{Query: "weight", ResearchTaskID: "task", Limit: 20}
	q, err := repo.SaveSearch(ctx, p.ID, research.SearchQueryKey("weight", nil, 20), command, research.SearchResult{Query: "weight", Works: []research.Work{{SourceID: "pubmed", SourceRecordID: "123", Title: "Detailed weight evidence", Abstract: "summary", Identifiers: research.Identifiers{PMID: "123"}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.CandidatePage(ctx, research.CandidateListCommand{ProjectID: p.ID, QueryID: q.ID, ResearchTaskID: "task", Limit: 20})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page=%+v %v", page, err)
	}
	id := page.Items[0].ID
	if _, err = discovery.UpdateReview(ctx, research.ReviewCommand{ProjectID: p.ID, ResearchTaskID: "task", CandidateID: id, Status: research.ReviewIncluded}); err != nil {
		t.Fatal(err)
	}
	old, err := discovery.ImportCandidate(ctx, research.ImportCandidateCommand{ProjectID: p.ID, ResearchTaskID: "task", CandidateID: id, Mode: research.MaterializeMetadata})
	if err != nil {
		t.Fatal(err)
	}
	oldDocs, err := kb.SynchronizeAttachmentsForTask(ctx, p.ID, "task", []string{old.Attachment.ID})
	if err != nil {
		t.Fatal(err)
	}
	search := builtin.NewSearchKnowledge(kb)
	oldRefs, err := search.Invoke(ctx, tool.Invocation{RunID: "workflow", ProjectID: p.ID, ResearchTaskID: "task", Arguments: []byte(fmt.Sprintf(`{"query":"summary","documentIds":[%q]}`, oldDocs[0].ID))})
	if err != nil || len(oldRefs.Citations) == 0 {
		t.Fatalf("old citations %v %v", oldRefs, err)
	}
	materializer.fail = true
	if _, err = discovery.ReadCandidateFullText(ctx, p.ID, "task", id, "weight"); err == nil {
		t.Fatal("failed source accepted")
	}
	preserved, err := discovery.GetCandidateForTask(ctx, p.ID, id, "task")
	if err != nil || preserved.AttachmentID != old.Attachment.ID {
		t.Fatal("failed upgrade lost abstract", err)
	}
	materializer.fail = false
	materializer.unavailable = true
	callsBefore := materializer.calls
	unavailable, err := discovery.ReadCandidateFullText(ctx, p.ID, "task", id, "weight")
	if err != nil || unavailable["status"] != "unavailable" || unavailable["retryRecommended"] != false || materializer.calls != callsBefore+1 {
		t.Fatal("source failure disguised or internally retried", unavailable, err)
	}
	materializer.unavailable = false
	normal, err := discovery.ReadCandidateFullText(ctx, p.ID, "task", id, "weight")
	if err != nil || normal["materialSaved"] != true || normal["attachmentId"] == old.Attachment.ID {
		t.Fatalf("normal upgrade did not persist full text: %v %v", normal, err)
	}
	// Restore the last committed summary binding to exercise the separate crash
	// recovery path with the same content-addressed PDF already on disk.
	if _, err = repo.UpdateCandidateTaskImport(ctx, research.ImportStateCommand{ProjectID: p.ID, ResearchTaskID: "task", CandidateID: id, Status: research.ImportImported, Kind: old.Candidate.ImportKind, AttachmentID: old.Attachment.ID, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Crash window: content identity is durable, attachment copy finished, but
	// candidate binding did not commit. Restart must recover by task+candidate+hash.
	staged, err := materializer.Materialize(ctx, p, preserved, research.MaterializeFullText)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.RecordMaterialIntent(ctx, p.ID, id, "task", staged.SHA256, research.ImportFullText); err != nil {
		t.Fatal(err)
	}
	copied, err := attachments.ImportResearchStagedForTask(ctx, p.ID, staged.Path, staged.Name, "task")
	if err != nil {
		t.Fatal(err)
	}
	materializer.Cleanup(staged)
	beforeRecover := materializer.calls
	if _, err = repo.RecoverImports(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	value, err := discovery.ReadCandidateFullText(ctx, p.ID, "task", id, "weight")
	if err != nil {
		t.Fatal(err)
	}
	if materializer.calls != beforeRecover || value["attachmentId"] != copied.ID {
		t.Fatal("crash recovery redownloaded or lost full text")
	}
	fullID, _ := value["attachmentId"].(string)
	if fullID == "" || fullID == old.Attachment.ID || value["materialSaved"] != true {
		t.Fatalf("upgrade=%v", value)
	}
	calls := materializer.calls
	// Fresh service emulates restart; persisted material avoids another network request.
	restarted, _ := research.NewDiscoveryService(rs, repo, projects)
	restarted.SetTaskValidator(tasks)
	restarted.SetImportPipeline(materializer, attachments, kb)
	if _, err = restarted.ReadCandidateFullText(ctx, p.ID, "task", id, "weight"); err != nil || materializer.calls != calls {
		t.Fatalf("replay downloaded again: %d %d %v", calls, materializer.calls, err)
	}
	newDocs, err := kb.SynchronizeAttachmentsForTask(ctx, p.ID, "task", []string{fullID})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := search.Invoke(ctx, tool.Invocation{RunID: "workflow", ProjectID: p.ID, ResearchTaskID: "task", Arguments: []byte(fmt.Sprintf(`{"query":"weight","documentIds":[%q]}`, newDocs[0].ID))})
	if err != nil || len(refs.Citations) == 0 {
		t.Fatalf("new citations=%+v %v", refs, err)
	}
	if refs.Citations[0].Reference == oldRefs.Citations[0].Reference || refs.Citations[0].AttachmentID != fullID {
		t.Fatal("full text borrowed abstract marker")
	}
	if _, _, err = attachments.ParsedForTask(ctx, p.ID, "task", old.Attachment.ID); err != nil {
		t.Fatal("old material lost", err)
	}
	for _, ref := range append(oldRefs.Citations, refs.Citations...) {
		if _, err = kb.ReadEvidenceChunkForTask(ctx, p.ID, "task", ref.IndexVersionID, ref.DocumentID, ref.AttachmentID, ref.ChunkID); err != nil {
			t.Fatal("immutable evidence unreadable", err)
		}
	}
	for _, v := range []struct {
		id    string
		level research.EvidenceLevel
	}{{old.Attachment.ID, research.EvidenceMetadataAbstract}, {fullID, research.EvidenceFullText}} {
		snap, err := repo.CitationSnapshotForAttachmentForTask(ctx, p.ID, "task", v.id, time.Now())
		if err != nil || snap.EvidenceLevel != v.level {
			t.Fatal("bibliography level changed", snap, err)
		}
	}
	if _, err = restarted.ReadCandidateFullText(ctx, p.ID, "other-task", id, "weight"); err == nil {
		t.Fatal("cross-task full text access")
	}
	// Recovery of an interrupted upgrade retains the last good material.
	if _, err = repo.UpdateCandidateTaskImport(ctx, research.ImportStateCommand{ProjectID: p.ID, ResearchTaskID: "task", CandidateID: id, Status: research.ImportImporting, Kind: research.ImportFullText, AttachmentID: fullID, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RecoverImports(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	recovered, _, err := repo.GetCandidateTaskImport(ctx, p.ID, id, "task")
	if err != nil || recovered.Status != research.ImportImported || recovered.AttachmentID != fullID {
		t.Fatalf("recovery=%+v %v", recovered, err)
	}
}
