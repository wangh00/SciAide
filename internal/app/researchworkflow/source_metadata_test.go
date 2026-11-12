package researchworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/attachment"
	research "github.com/wangh00/SciAide/internal/app/research"
	"strings"
	"testing"
	"unicode/utf8"
)

type fullTextWithCatalogueFixture struct {
	unusedDiscovery
	candidate research.Candidate
}

func (f fullTextWithCatalogueFixture) GetCandidate(context.Context, string, string) (research.Candidate, error) {
	return f.candidate, nil
}
func (f fullTextWithCatalogueFixture) ImportCandidate(context.Context, research.ImportCandidateCommand) (research.ImportCandidateResult, error) {
	return research.ImportCandidateResult{Candidate: f.candidate}, nil
}

func TestFullTextImportHandsOffCatalogueSeparatelyWithoutPromotingItToPDFText(t *testing.T) {
	yes := true
	metadata := &research.SourceMetadata{Subjects: []string{strings.Repeat("s", 5000)}, EducationLevels: []string{"Higher Education"}, PeerReviewed: &yes}
	candidate := research.Candidate{ID: "candidate", ReviewStatus: research.ReviewIncluded, ImportStatus: research.ImportImported, ImportKind: research.ImportFullText, AttachmentID: "actual-pdf", Preferred: research.Work{Title: "ERIC PDF without abstract"}, Records: []research.SourceRecord{{Work: research.Work{SourceID: "eric", SourceRecordID: "EJ1234567", SourceMetadata: metadata}}}}
	service := &Service{discovery: fullTextWithCatalogueFixture{candidate: candidate}}
	materials, err := service.ImportSelected(context.Background(), "project", []string{"candidate"}, research.MaterializeFullText)
	if err != nil || len(materials) != 1 {
		t.Fatal(err, materials)
	}
	value := materials[0]
	if value.ImportKind != research.ImportFullText || value.AttachmentID != "actual-pdf" || len(value.SourceMetadata) != 1 || value.SourceMetadata[0].Metadata.EducationLevels[0] != "Higher Education" || value.SourceMetadataSHA256 == "" || !value.SourceMetadata[0].Preview.Truncated {
		t.Fatal(value)
	}
	if len(metadata.Subjects[0]) != 5000 {
		t.Fatal("full source changed")
	}
}

type catalogueBatchFixture struct {
	unusedDiscovery
	candidates map[string]research.Candidate
}

func (f catalogueBatchFixture) GetCandidate(_ context.Context, _, id string) (research.Candidate, error) {
	return f.candidates[id], nil
}
func (f catalogueBatchFixture) ImportCandidate(_ context.Context, command research.ImportCandidateCommand) (research.ImportCandidateResult, error) {
	return research.ImportCandidateResult{Candidate: f.candidates[command.CandidateID]}, nil
}
func TestHundredMaterialImportCatalogueHandoffFitsExecutorEnvelope(t *testing.T) {
	yes := true
	metadata := &research.SourceMetadata{Subjects: []string{strings.Repeat("😀", 100)}, EducationLevels: []string{strings.Repeat("😀", 100)}, PublicationTypes: []string{strings.Repeat("😀", 100)}, SourceCitation: strings.Repeat("😀", 300), License: strings.Repeat("😀", 300), PeerReviewed: &yes}
	// Exercise the service's real response construction, including maximal legal
	// titles and JSON escaping, rather than constructing short-title output by hand.
	title := strings.Repeat("😀<>&", 500)
	fixture := catalogueBatchFixture{candidates: map[string]research.Candidate{}}
	ids := []string{}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		records := []research.SourceRecord{{Work: research.Work{SourceID: "eric", SourceRecordID: fmt.Sprintf("EJ12345%02d", i), SourceMetadata: metadata}}, {Work: research.Work{SourceID: "core", SourceRecordID: fmt.Sprint(i + 1), SourceMetadata: metadata}}}
		fixture.candidates[id] = research.Candidate{RecordIdentity: &research.RecordIdentity{Version: research.RecordIdentityVersion, Roles: []research.RecordRoleHint{{Kind: "companion_material", Field: "title"}, {Kind: "preprint_or_working_paper", Field: "workType"}, {Kind: "thesis", Field: "workType"}, {Kind: "dataset", Field: "workType"}, {Kind: "protocol", Field: "workType"}}, Relations: []research.RecordRelationHint{{Kind: "identifier_conflict", TargetCandidateID: "20000000-0000-0000-0000-000000000000"}, {Kind: "possible_same_work", TargetCandidateID: "30000000-0000-0000-0000-000000000000"}, {Kind: "possible_version", TargetCandidateID: "30000000-0000-0000-0000-000000000000"}, {Kind: "possible_companion", TargetCandidateID: "30000000-0000-0000-0000-000000000000"}, {Kind: "shared_identifier", TargetCandidateID: "30000000-0000-0000-0000-000000000000"}}}, ID: id, ReviewStatus: research.ReviewIncluded, ImportStatus: research.ImportImported, ImportKind: research.ImportFullText, AttachmentID: fmt.Sprintf("10000000-0000-0000-0000-%012d", i), Preferred: research.Work{Title: title}, Records: records}
		ids = append(ids, id)
	}
	service := &Service{discovery: fixture}
	materials, err := service.ImportSelected(context.Background(), "project", ids, research.MaterializeFullText)
	if err != nil || len(materials) != 100 {
		t.Fatal(err, len(materials))
	}
	attachments := []string{}
	for _, material := range materials {
		attachments = append(attachments, material.AttachmentID)
		if material.RecordIdentity == nil || len(material.RecordIdentity.RoleKinds) != 5 || len(material.RecordIdentity.RelationKinds) != 5 || material.RecordIdentity.RelationKinds[0] != "identifier_conflict" || !material.RecordIdentity.Preview.Truncated {
			t.Fatal("identity overview lost", material.RecordIdentity)
		}
		sum := sha256.Sum256([]byte(title))
		if utf8.RuneCountInString(material.Title) > 96 || material.TitlePreview == nil || !material.TitlePreview.Truncated || material.TitlePreview.TotalRunes != 2000 || material.TitlePreview.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("title preview not disclosed or bound", material.TitlePreview)
		}
	}
	encoded, err := json.Marshal(map[string]any{"materials": materials, "attachmentIds": attachments})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 256*1024 {
		t.Fatal("catalogue handoff exceeds real executor structured limit", len(encoded))
	}
	if len(metadata.Subjects[0]) != 400 || fixture.candidates[ids[0]].Preferred.Title != title {
		t.Fatal("full candidate modified")
	}
	t.Logf("100-material structured handoff=%d bytes (executor limit 262144)", len(encoded))
}

func TestImportedCandidateTitleViewPreservesShortAndUserMaterialTitles(t *testing.T) {
	short := ImportedMaterial{Title: "Complete title", CandidateID: "candidate"}
	view := importedCandidateMaterialView(short)
	if view.Title != short.Title || view.TitlePreview != nil {
		t.Fatal(view)
	}

	title := strings.Repeat("😀", 300)
	service := &Service{knowledge: catalogueMaterialIndexer{}, materials: reportUserMaterialLoader{value: attachment.Attachment{ID: "user-attachment", OriginalName: title, SHA256: strings.Repeat("a", 64)}, allowedTask: "task-a"}}
	materials, err := service.ImportUserMaterials(context.Background(), "project", "task-a", []string{"user-attachment"})
	if err != nil || len(materials) != 1 || materials[0].Title != title || materials[0].TitlePreview != nil || materials[0].MaterialOrigin != "user_selected" {
		t.Fatal("user-material canonical title was projected", err, materials)
	}
}

type catalogueMaterialIndexer struct{ workflowKnowledgeFixture }

func (catalogueMaterialIndexer) Enqueue(context.Context, attachment.Attachment) error { return nil }
