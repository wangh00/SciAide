package research

import (
	"context"
	"slices"
	"sync"
	"testing"
)

type greedyScheduledConnector struct {
	fixtureConnector
	order *[]string
}

func (c greedyScheduledConnector) Search(ctx context.Context, _ SearchOptions) ([]Work, error) {
	*c.order = append(*c.order, c.source.ID)
	b, _ := SourceHTTPBudgetFromContext(ctx)
	for i := 0; i < 100; i++ {
		if err := b.Consume(c.source.ID); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func TestRetrievalSchedulePreservesPriorityAndLaterSlotAllocations(t *testing.T) {
	p, _ := BuildRetrievalPlan("education", nil, 2)
	order := []string{}
	connectors := []Connector{}
	for _, source := range p.Sources {
		connectors = append(connectors, greedyScheduledConnector{fixtureConnector: fixtureConnector{source: testSource(source.SourceID)}, order: &order})
	}
	service, err := NewService(connectors)
	if err != nil {
		t.Fatal(err)
	}
	ctx, b, _ := WithSourceHTTPBudget(context.Background(), 14)
	want := []string{}
	for q := 0; q < 2; q++ {
		scheduled, err := WithRetrievalSchedule(ctx, &p, q)
		if err != nil {
			t.Fatal(err)
		}
		ids := p.SourcesForQuery(q)
		want = append(want, ids...)
		r, err := service.Search(scheduled, SearchCommand{Query: "complete topic", SourceIDs: ids})
		if err != nil || !r.Partial {
			t.Fatal(r, err)
		}
		for _, s := range r.Sources {
			if s.ErrorCode != FailureRequestBudget {
				t.Fatal(s)
			}
		}
	}
	if !slices.Equal(order, want) {
		t.Fatal("source priority became a concurrent race", order, want)
	}
	if used, _ := b.Usage(); used != 14 {
		t.Fatal(used)
	}
	for _, slot := range p.Slots {
		if slot.UsedHTTP < slot.ReservedHTTP {
			t.Fatal("retry stole a later source's base allocation", slot)
		}
	}
}

func TestRetrievalProfilesAllocateWithoutGuessingOrEnablingCORE(t *testing.T) {
	for _, profile := range RetrievalProfiles() {
		p, err := BuildRetrievalPlan(profile, nil, 2)
		if err != nil {
			t.Fatal(err)
		}
		cost := 0
		for _, slot := range p.Slots {
			cost += slot.ReservedHTTP
			if slot.SourceID == "core" {
				t.Fatal("profile enabled explicit-only CORE")
			}
			if slot.SourceID == "pubmed" && slot.ReservedHTTP != 2 {
				t.Fatal(slot)
			}
		}
		if cost > p.MaxHTTP || p.MaxHTTP != 14 || len(p.SourcesForQuery(0)) == 0 {
			t.Fatal(p)
		}
		if profile == "education" && !slices.Contains(p.SourcesForQuery(0), "eric") {
			t.Fatal(p)
		}
		if profile == "biomedical" && (!slices.Contains(p.SourcesForQuery(0), "pubmed") || !slices.Contains(p.SourcesForQuery(0), "europepmc")) {
			t.Fatal(p)
		}
		if profile == "computing" && !slices.Contains(p.SourcesForQuery(0), "arxiv") {
			t.Fatal(p)
		}
	}
	if _, err := BuildRetrievalPlan("AI university hospital", nil, 1); err == nil {
		t.Fatal("must not guess a discipline from keywords")
	}
}

func TestRetrievalExplicitSourcesRemainOrderedAndBounded(t *testing.T) {
	p, err := BuildRetrievalPlan("biomedical", []string{" CORE ", "eric", "CORE"}, 2)
	if err != nil || p.SelectionMode != "explicit" || !slices.Equal(p.SourcesForQuery(0), []string{"core", "eric"}) {
		t.Fatal(p, err)
	}
	if _, err := BuildRetrievalPlan("education", []string{"unknown"}, 1); err == nil {
		t.Fatal("invalid explicit source accepted")
	}
	if _, err := BuildRetrievalPlan("education", nil, 0); err == nil {
		t.Fatal("empty plan accepted")
	}
	p, _ = BuildRetrievalPlan("interdisciplinary", nil, 2)
	if len(p.Deferred) == 0 {
		t.Fatal("over-budget query slots silently disappeared")
	}
	for _, source := range p.Sources {
		if !slices.Contains(p.SourcesForQuery(0), source.SourceID) {
			t.Fatal("a selected source was silently replaced", p)
		}
	}
}

func TestSourceHTTPBudgetIsSharedAndCannotBeResetInContext(t *testing.T) {
	ctx, b, err := WithSourceHTTPBudget(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := WithSourceHTTPBudget(ctx, 14); err == nil {
		t.Fatal("active request budget was reset")
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = b.Consume("source") }()
	}
	wg.Wait()
	if used, denied := b.Usage(); used != 7 || denied != 93 {
		t.Fatal(used, denied)
	}
}
