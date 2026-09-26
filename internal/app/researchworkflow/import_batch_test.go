package researchworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/research"
)

type importBatchFixture struct {
	unusedDiscovery
	calls  []string
	fail   bool
	cancel context.CancelFunc
}

func (f *importBatchFixture) GetCandidate(_ context.Context, _, id string) (research.Candidate, error) {
	return research.Candidate{ID: id, ReviewStatus: research.ReviewIncluded, Preferred: research.Work{Title: "Paper " + id}}, nil
}
func (f *importBatchFixture) ImportCandidate(_ context.Context, c research.ImportCandidateCommand) (research.ImportCandidateResult, error) {
	f.calls = append(f.calls, c.CandidateID)
	if f.cancel != nil {
		f.cancel()
		return research.ImportCandidateResult{}, context.Canceled
	}
	if c.CandidateID == "b" && f.fail {
		return research.ImportCandidateResult{}, fmt.Errorf("open full text source returned HTTP 429")
	}
	return research.ImportCandidateResult{Candidate: research.Candidate{ID: c.CandidateID, ImportStatus: research.ImportImported, AttachmentID: "attachment-" + c.CandidateID, ImportKind: research.ImportMetadataAbstract}, Warning: "metadata only"}, nil
}

func TestImportBatchContinuesAndExposesPartialFailure(t *testing.T) {
	f := &importBatchFixture{fail: true}
	s := &Service{discovery: f}
	v, err := s.ImportSelected(context.Background(), "project", []string{"a", "b", "c"}, research.MaterializeAuto)
	var batch *ImportBatchError
	if !errors.As(err, &batch) || batch.Succeeded != 2 || batch.Failed != 1 || len(v) != 2 || strings.Join(f.calls, ",") != "a,b,c" {
		t.Fatalf("results=%+v calls=%v err=%v", v, f.calls, err)
	}
	if !strings.Contains(batch.UserFacingMessage(), "HTTP 429") || v[0].Warning == "" {
		t.Fatalf("missing failure details or warning: %v %+v", err, v)
	}
	f.fail = false
	v, err = s.ImportSelected(context.Background(), "project", []string{"a", "b", "c"}, research.MaterializeAuto)
	if err != nil || len(v) != 3 {
		t.Fatalf("retry=%+v err=%v", v, err)
	}
}

func TestImportBatchStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &importBatchFixture{cancel: cancel}
	s := &Service{discovery: f}
	_, err := s.ImportSelected(ctx, "project", []string{"a", "b", "c"}, research.MaterializeAuto)
	if !errors.Is(err, context.Canceled) || len(f.calls) != 1 {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
}

type userMaterialLoader struct {
	got    []string
	values []attachment.Attachment
	err    error
}

func (f *userMaterialLoader) ReferenceMaterials(_ context.Context, projectID, taskID string, ids []string) ([]attachment.Attachment, error) {
	if projectID != "project" || taskID != "task" {
		return nil, fmt.Errorf("unexpected scope %s/%s", projectID, taskID)
	}
	f.got = append([]string(nil), ids...)
	return append([]attachment.Attachment(nil), f.values...), f.err
}

type userMaterialKnowledge struct{ enqueued []string }

func (f *userMaterialKnowledge) SynchronizeAttachments(context.Context, string, []string) ([]knowledge.Document, error) {
	return nil, nil
}
func (f *userMaterialKnowledge) ReadEvidenceChunk(context.Context, string, string, string, string, string) (knowledge.EvidenceChunk, error) {
	return knowledge.EvidenceChunk{}, nil
}
func (f *userMaterialKnowledge) Enqueue(_ context.Context, value attachment.Attachment) error {
	f.enqueued = append(f.enqueued, value.ID)
	return nil
}

func TestImportUserMaterialsKeepsExplicitUserOriginAndIndexesAll(t *testing.T) {
	loader := &userMaterialLoader{values: []attachment.Attachment{{ID: "shared", OriginalName: "shared.pdf"}, {ID: "task", OriginalName: "task.docx"}}}
	index := &userMaterialKnowledge{}
	service := &Service{knowledge: index}
	service.SetMaterialLoader(loader)
	values, err := service.ImportUserMaterials(context.Background(), "project", "task", []string{"shared", "task"})
	if err != nil || strings.Join(loader.got, ",") != "shared,task" || strings.Join(index.enqueued, ",") != "shared,task" || len(values) != 2 {
		t.Fatalf("values=%#v loader=%v indexed=%v err=%v", values, loader.got, index.enqueued, err)
	}
	for _, value := range values {
		if value.MaterialOrigin != "user_selected" || value.CandidateID != "" || value.Warning == "" {
			t.Fatalf("user material was silently promoted: %#v", value)
		}
	}
	if _, err := service.ImportUserMaterials(context.Background(), "project", "", []string{"shared"}); err == nil {
		t.Fatal("unscoped user material import was accepted")
	}
	loader.values = nil
	values, err = service.ImportUserMaterials(context.Background(), "project", "task", []string{})
	if err != nil || values == nil || len(values) != 0 {
		t.Fatalf("empty material set must stay an explicit empty array: %#v %v", values, err)
	}
}
