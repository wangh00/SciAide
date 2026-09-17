package research

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
)

func TestFullTextReaderRejectsUnscopedOrInvalidQueries(t *testing.T) {
	s := &DiscoveryService{}
	for _, value := range []struct{ task, query string }{{"", "results"}, {"task", " "}, {"task", strings.Repeat("a", 301)}, {"task", "results"}} {
		if _, err := s.ReadCandidateFullText(context.Background(), "project", value.task, "candidate", value.query); err == nil {
			t.Fatal("unscoped or invalid request accepted")
		}
	}
}

type fullTextScopeRepository struct {
	DiscoveryRepository
	candidate Candidate
}

func (f fullTextScopeRepository) GetCandidate(context.Context, string, string) (Candidate, error) {
	return f.candidate, nil
}
func (f fullTextScopeRepository) CandidateForTask(_ context.Context, _, _, task string) (Candidate, error) {
	if task != "owner" {
		return Candidate{}, fmt.Errorf("candidate is not in this task")
	}
	return f.candidate, nil
}
func (fullTextScopeRepository) GetCandidateTaskImport(context.Context, string, string, string) (CandidateTaskImport, bool, error) {
	return CandidateTaskImport{}, false, nil
}
func (fullTextScopeRepository) UpdateCandidateTaskImport(context.Context, ImportStateCommand) (CandidateTaskImport, error) {
	panic("read must not change import state")
}

type fullTextTaskValidator struct{}

func (fullTextTaskValidator) Exists(context.Context, string, string) (bool, error) { return true, nil }

type fullTextProjectLoader struct{}

func (fullTextProjectLoader) Get(context.Context, string) (project.Project, error) {
	return project.Project{ID: "project"}, nil
}

func TestFullTextReaderRejectsAnotherTasksCandidateAndUnselectedCandidate(t *testing.T) {
	s := &DiscoveryService{repository: fullTextScopeRepository{candidate: Candidate{ID: "candidate", ReviewStatus: ReviewPending}}, tasks: fullTextTaskValidator{}, projects: fullTextProjectLoader{}}
	for _, task := range []string{"other", "owner"} {
		_, err := s.ReadCandidateFullText(context.Background(), "project", task, "candidate", "weight")
		if err == nil || !(strings.Contains(err.Error(), "not in this task") || strings.Contains(err.Error(), "selected candidate")) {
			t.Fatalf("task=%s err=%v", task, err)
		}
	}
}

func TestFullTextUnitsAreBoundedAndPreserveSource(t *testing.T) {
	units := []document.Unit{{Content: "No matching information"}}
	for i := 0; i < 10; i++ {
		units = append(units, document.Unit{Locator: "page:2", Content: "Weight result " + strings.Repeat("x", 1600)})
	}
	got, err := selectFullTextUnits(context.Background(), units, "weight")
	if err != nil || len(got) != 6 {
		t.Fatalf("units=%d err=%v", len(got), err)
	}
	for _, u := range got {
		if u != units[1] {
			t.Fatal("source changed")
		}
	}
	got, err = selectFullTextUnits(context.Background(), units, "absent")
	if err != nil || len(got) != 0 {
		t.Fatalf("unmatched query=%v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = selectFullTextUnits(ctx, units, "weight"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}
