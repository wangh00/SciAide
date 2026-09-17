package builtin

import (
	"context"
	"github.com/wangh00/SciAide/internal/app/knowledge"
)

type frozenResearchSearch struct{ result knowledge.SearchResult }

func (s frozenResearchSearch) SearchWithOptions(context.Context, string, knowledge.SearchOptions) (knowledge.SearchResult, error) {
	return s.result, nil
}
