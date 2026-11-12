package workflow

import (
	"context"
	"testing"
)

type deleteOnlyRepository struct {
	RuntimeRepository
	called bool
}

func (r *deleteOnlyRepository) GetRun(context.Context, string, string) (RunDetail, error) {
	panic("deletion must not hydrate any history")
}
func (r *deleteOnlyRepository) DeleteRun(_ context.Context, projectID, runID string) error {
	if projectID != "project" || runID != "run" {
		panic("wrong scope")
	}
	r.called = true
	return nil
}
func TestDeleteDelegatesAtomicGuardsWithoutLoadingDetails(t *testing.T) {
	r := &deleteOnlyRepository{}
	s := &RuntimeService{repository: r}
	if e := s.Delete(context.Background(), " project ", " run "); e != nil || !r.called {
		t.Fatal(e)
	}
	r.called = false
	if e := s.Delete(context.Background(), "", "run"); e == nil || r.called {
		t.Fatal("invalid scope accepted")
	}
}
