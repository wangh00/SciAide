package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/researchsources"
)

const RetrievalPlanVersion = "discipline-retrieval-v1"
const RetrievalHTTPMax = 14 // Per search invocation, NOT per research task.

type RetrievalSlot struct {
	SourceID     string `json:"sourceId"`
	QueryIndex   int    `json:"queryIndex"`
	ReservedHTTP int    `json:"reservedHttp"`
	MaxHTTP      int    `json:"allocatedHttpRequests,omitempty"`
	UsedHTTP     int    `json:"usedHttpRequests"`
	BudgetDenied int    `json:"budgetDeniedRequests"`
}

type RetrievalSource struct {
	SourceID string `json:"sourceId"`
	Reason   string `json:"reason"`
}

type RetrievalPlan struct {
	Version       string            `json:"version"`
	Profile       string            `json:"profile"`
	SelectionMode string            `json:"selectionMode"`
	MaxHTTP       int               `json:"maxHttpRequests"`
	Sources       []RetrievalSource `json:"sources"`
	Slots         []RetrievalSlot   `json:"slots"`
	Deferred      []RetrievalSlot   `json:"deferredSlots"`
	UsedHTTP      int               `json:"usedHttpRequests"`
	BudgetDenied  int               `json:"budgetDeniedRequests"`
}

// Profiles are explicit planning inputs, never guessed from isolated keywords.
// These are bounded source strategies, not a claim of exhaustive coverage.
func RetrievalProfiles() []string {
	return []string{"education", "biomedical", "computing", "general", "interdisciplinary"}
}

func retrievalPriorities(profile string) []string {
	switch profile {
	case "education":
		return []string{"eric", "openalex", "crossref", "europepmc", "semantic-scholar"}
	case "biomedical":
		return []string{"pubmed", "europepmc", "openalex", "crossref", "semantic-scholar"}
	case "computing":
		return []string{"arxiv", "openalex", "crossref", "semantic-scholar"}
	case "general":
		return []string{"openalex", "crossref", "semantic-scholar"}
	case "interdisciplinary":
		return []string{"openalex", "crossref", "eric", "pubmed", "europepmc", "arxiv", "semantic-scholar"}
	}
	return nil
}

func BuildRetrievalPlan(profile string, explicit []string, queryCount int) (RetrievalPlan, error) {
	priorities := retrievalPriorities(profile)
	if priorities == nil || queryCount < 1 || queryCount > 2 {
		return RetrievalPlan{}, fmt.Errorf("retrieval plan requires a supported profile and 1-2 queries")
	}
	p := RetrievalPlan{Version: RetrievalPlanVersion, Profile: profile, SelectionMode: "profile", MaxHTTP: RetrievalHTTPMax,
		Sources: []RetrievalSource{}, Slots: []RetrievalSlot{}, Deferred: []RetrievalSlot{}}
	if len(explicit) > 0 {
		if len(explicit) > MaxSourcesPerSearch {
			return RetrievalPlan{}, fmt.Errorf("too many explicitly selected retrieval sources")
		}
		p.SelectionMode = "explicit"
		priorities = explicit
	}
	seen := map[string]bool{}
	for _, value := range priorities {
		id := strings.ToLower(strings.TrimSpace(value))
		spec, ok := researchsources.Lookup(id)
		if !ok || (p.SelectionMode == "profile" && spec.ExplicitSelection) {
			return RetrievalPlan{}, fmt.Errorf("unsupported retrieval source %q", value)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		reason := "按 " + profile + " 学科配置选择；仅作候选发现，不代表穷尽该领域"
		if p.SelectionMode == "explicit" {
			reason = "调用方明确指定，保留来源及顺序，不自动补入其他来源"
		}
		p.Sources = append(p.Sources, RetrievalSource{SourceID: id, Reason: reason})
	}
	reserved := 0
	for q := 0; q < queryCount; q++ {
		for _, source := range p.Sources {
			spec, _ := researchsources.Lookup(source.SourceID)
			cost := max(1, spec.SearchHTTPRequests)
			slot := RetrievalSlot{SourceID: source.SourceID, QueryIndex: q, ReservedHTTP: cost}
			if reserved+cost > p.MaxHTTP {
				p.Deferred = append(p.Deferred, slot)
				continue
			}
			p.Slots = append(p.Slots, slot)
			reserved += cost
		}
	}
	return p, nil
}

func (p RetrievalPlan) SourcesForQuery(index int) []string {
	ids := []string{}
	for _, s := range p.Slots {
		if s.QueryIndex == index {
			ids = append(ids, s.SourceID)
		}
	}
	return ids
}

type retrievalScheduleKey struct{}
type retrievalSchedule struct {
	plan  *RetrievalPlan
	query int
}

// Scheduling stays below query persistence: one full query still creates one
// history entry, even though its source slots execute deterministically.
func WithRetrievalSchedule(ctx context.Context, p *RetrievalPlan, index int) (context.Context, error) {
	b, ok := SourceHTTPBudgetFromContext(ctx)
	if !ok || b.parent != nil || p == nil || p.MaxHTTP != b.limit || len(p.SourcesForQuery(index)) == 0 {
		return ctx, fmt.Errorf("retrieval schedule requires a root HTTP budget and nonempty planned sources")
	}
	return context.WithValue(ctx, retrievalScheduleKey{}, retrievalSchedule{plan: p, query: index}), nil
}

func (s retrievalSchedule) allocate(ctx context.Context, sourceID string) (context.Context, *SourceHTTPBudget, *RetrievalSlot, error) {
	b, _ := SourceHTTPBudgetFromContext(ctx)
	for index := range s.plan.Slots {
		slot := &s.plan.Slots[index]
		if slot.QueryIndex != s.query || slot.SourceID != sourceID {
			continue
		}
		future := 0
		for _, next := range s.plan.Slots[index+1:] {
			future += next.ReservedHTTP
		}
		used, _ := b.Usage()
		slot.MaxHTTP = s.plan.MaxHTTP - used - future
		if slot.MaxHTTP < slot.ReservedHTTP {
			return ctx, nil, nil, fmt.Errorf("invalid retrieval HTTP reservation ledger")
		}
		callCtx, allocation, err := WithSourceHTTPSlot(ctx, slot.MaxHTTP)
		return callCtx, allocation, slot, err
	}
	return ctx, nil, nil, fmt.Errorf("source is not in the current retrieval query schedule")
}
