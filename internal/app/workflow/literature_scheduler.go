package workflow

import (
	"fmt"
	"github.com/wangh00/SciAide/internal/app/research"
	"strings"
)

type literaturePage struct {
	Query  string `json:"query"`
	Source string `json:"source"`
	Offset int    `json:"offset"`
}

func updateLiteraturePages(state *literatureCheckpoint, sources []research.SourceSearch) {
	for _, source := range sources {
		query := source.EffectiveQuery
		if query == "" {
			continue
		}
		key := source.SourceID + "\n" + query
		if state.PageLedger == nil {
			state.PageLedger = map[string]int{}
		}
		// Persist one workflow-level retry for a transient failed page. Keep
		// the ledger incomplete after exhaustion; never infer scholarly absence.
		if source.Status == research.SearchFailed {
			if state.PageFailures == nil {
				state.PageFailures = map[string]int{}
			}
			failureKey := fmt.Sprintf("%s\n%d", key, source.Offset)
			state.PageFailures[failureKey]++
			if _, exists := state.PageLedger[key]; !exists {
				state.PageLedger[key] = source.Offset
			}
			if source.Retryable && state.PageFailures[failureKey] <= 1 {
				found := false
				for _, p := range state.Pages {
					found = found || p.Source == source.SourceID && p.Query == query && p.Offset == source.Offset
				}
				if !found {
					state.Pages = append(state.Pages, literaturePage{query, source.SourceID, source.Offset})
				}
			}
			continue
		}
		offset, exists := state.PageLedger[key]
		if exists && (offset < 0 || source.Offset < offset-20) {
			continue
		}
		next := -1
		if source.LimitReached {
			next = source.Offset + 20
		}
		state.PageLedger[key] = next
		pages := state.Pages[:0]
		for _, p := range state.Pages {
			if p.Source+"\n"+p.Query != key {
				pages = append(pages, p)
			}
		}
		state.Pages = pages
		if next >= 0 && next <= 140 {
			state.Pages = append(state.Pages, literaturePage{query, source.SourceID, next})
		}
	}
}

func scheduleLiteratureSearch(state *literatureCheckpoint, result literatureScreening, pending int) bool {
	if pending > 0 || result.Recommendation == "narrow_scope" {
		return false
	}
	// Consume persisted pages fairly before opening more first pages.
	if scheduleLiteraturePage(state) {
		return true
	}
	if result.Coverage.Sufficient && result.Recommendation != "expand_search" {
		return false
	}
	if state.Round >= literatureMaxSupplementRounds {
		return false
	}
	seen := map[string]bool{}
	for _, q := range state.Queries {
		seen[strings.ToLower(strings.TrimSpace(q))] = true
	}
	next := []string{}
	for _, q := range result.SupplementalQueries {
		q = strings.TrimSpace(q)
		key := strings.ToLower(q)
		if q != "" && !seen[key] {
			seen[key] = true
			next = append(next, q)
		}
	}
	if len(next) == 0 {
		return false
	}
	state.NextQueries = next
	state.NextSources = nil
	state.NextOffset = 0
	state.Round++
	state.Phase = "search"
	return true
}

func scheduleLiteraturePage(state *literatureCheckpoint) bool {
	if len(state.Pages) == 0 || state.PageRequests >= 30 {
		return false
	}
	p := state.Pages[0]
	state.Pages = state.Pages[1:]
	state.NextQueries = []string{p.Query}
	state.NextSources = []string{p.Source}
	state.NextOffset = p.Offset
	state.PageRequests++
	state.Phase = "search"
	return true
}

func literaturePagesIncomplete(state literatureCheckpoint) bool {
	for _, offset := range state.PageLedger {
		if offset >= 0 {
			return true
		}
	}
	return len(state.Pages) > 0
}

func literatureBudgetMessage(state literatureCheckpoint) string {
	return "已完成计划内每式每源最多20条的首页检索，现有材料尚不足以支持声明的范围；未穷尽数据库，不据此推断整个领域没有相关研究。"
}
