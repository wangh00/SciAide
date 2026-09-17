package researchworkflow

import (
	"context"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/research"
	"testing"
)

type batchKnowledge struct {
	workflowKnowledgeFixture
	sizes []int
}

func (k *batchKnowledge) SynchronizeAttachments(_ context.Context, _ string, ids []string) ([]knowledge.Document, error) {
	k.sizes = append(k.sizes, len(ids))
	if len(ids) > 20 {
		return nil, fmt.Errorf("batch too large")
	}
	docs := []knowledge.Document{}
	for _, id := range ids {
		docs = append(docs, knowledge.Document{AttachmentID: id})
	}
	return docs, nil
}
func TestSynchronizeAllSelectedMaterialsInBoundedBatches(t *testing.T) {
	k := &batchKnowledge{}
	s := &Service{knowledge: k}
	ids := []string{}
	for i := 0; i < 51; i++ {
		ids = append(ids, fmt.Sprint(i))
	}
	docs, err := s.Synchronize(context.Background(), "project", ids)
	if err != nil || len(docs) != 51 || fmt.Sprint(k.sizes) != "[20 20 11]" {
		t.Fatal(len(docs), k.sizes, err)
	}
}

type pagedDiscovery struct {
	unusedDiscovery
	calls int
}

func (p *pagedDiscovery) ListCandidates(_ context.Context, c research.CandidateListCommand) (research.CandidatePage, error) {
	p.calls++
	if c.ProjectID != "project" || c.QueryID != "issued" {
		return research.CandidatePage{}, fmt.Errorf("wrong scope")
	}
	values := []research.Candidate{}
	for i := c.Offset; i < min(c.Offset+c.Limit, 235); i++ {
		values = append(values, research.Candidate{ID: fmt.Sprint(i)})
	}
	return research.CandidatePage{Items: values, Total: 235}, nil
}
func TestLiteratureDrainsAllPersistedPages(t *testing.T) {
	p := &pagedDiscovery{}
	s := &Service{discovery: p}
	values, err := s.LiteratureCandidates(context.Background(), "project", []string{"issued", "issued"})
	if err != nil || len(values) != 235 || p.calls != 6 || values[234].ID != "234" {
		t.Fatal(len(values), p.calls, err)
	}
}
