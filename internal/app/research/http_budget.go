package research

import (
	"context"
	"fmt"
	"sync"
)

const FailureRequestBudget FailureCode = "request_budget_exhausted"

type sourceHTTPBudgetKey struct{}

// SourceHTTPBudget counts transport attempts, not queries, tokens or money.
// One shared instance covers all providers, retries and redirects in a search.
type SourceHTTPBudget struct {
	mu                  sync.Mutex
	limit, used, denied int
	parent              *SourceHTTPBudget
}

// A slot can restrict, but never replace or reset, the invocation's shared cap.
func WithSourceHTTPSlot(ctx context.Context, limit int) (context.Context, *SourceHTTPBudget, error) {
	parent, ok := SourceHTTPBudgetFromContext(ctx)
	if !ok || parent.parent != nil || limit < 1 {
		return ctx, nil, fmt.Errorf("source slot requires a positive cap inside a root HTTP budget")
	}
	b := &SourceHTTPBudget{limit: limit, parent: parent}
	return context.WithValue(ctx, sourceHTTPBudgetKey{}, b), b, nil
}

func WithSourceHTTPBudget(ctx context.Context, limit int) (context.Context, *SourceHTTPBudget, error) {
	if limit < 1 {
		return ctx, nil, fmt.Errorf("source HTTP request budget must be positive")
	}
	if _, exists := SourceHTTPBudgetFromContext(ctx); exists {
		return ctx, nil, fmt.Errorf("cannot replace an active source HTTP request budget")
	}
	b := &SourceHTTPBudget{limit: limit}
	return context.WithValue(ctx, sourceHTTPBudgetKey{}, b), b, nil
}

func SourceHTTPBudgetFromContext(ctx context.Context) (*SourceHTTPBudget, bool) {
	b, ok := ctx.Value(sourceHTTPBudgetKey{}).(*SourceHTTPBudget)
	return b, ok && b != nil
}

func (b *SourceHTTPBudget) Consume(sourceID string) error {
	if b.parent != nil {
		b.parent.mu.Lock()
		defer b.parent.mu.Unlock()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.limit || (b.parent != nil && b.parent.used >= b.parent.limit) {
		b.denied++
		if b.parent != nil {
			b.parent.denied++
		}
		return &SourceError{SourceID: sourceID, Code: FailureRequestBudget, Message: "search or source-slot HTTP allocation exhausted; source not fully searched, not an empty result"}
	}
	b.used++
	if b.parent != nil {
		b.parent.used++
	}
	return nil
}

func (b *SourceHTTPBudget) Usage() (used, denied int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.denied
}
