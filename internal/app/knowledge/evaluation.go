package knowledge

import (
	"fmt"
	"strings"
)

type EvaluationCase struct {
	ID              string   `json:"id"`
	Query           string   `json:"query"`
	RelevantSources []string `json:"relevantSources"`
}

type EvaluationRanking struct {
	Case    EvaluationCase
	Matches []Match
}

type RetrievalMetrics struct {
	Cases          int     `json:"cases"`
	K              int     `json:"k"`
	HitRate        float64 `json:"hitRate"`
	MeanRecall     float64 `json:"meanRecall"`
	MeanReciprocal float64 `json:"meanReciprocalRank"`
	LocatableRate  float64 `json:"locatableRate"`
}

func EvaluateRankings(rankings []EvaluationRanking, k int) (RetrievalMetrics, error) {
	if len(rankings) == 0 || k < 1 || k > maxSearchLimit {
		return RetrievalMetrics{}, fmt.Errorf("retrieval evaluation inputs are invalid")
	}
	metrics := RetrievalMetrics{Cases: len(rankings), K: k}
	var hitCases, totalMatches, locatableMatches int
	for _, ranking := range rankings {
		if strings.TrimSpace(ranking.Case.ID) == "" || strings.TrimSpace(ranking.Case.Query) == "" || len(ranking.Case.RelevantSources) == 0 {
			return RetrievalMetrics{}, fmt.Errorf("retrieval evaluation case is incomplete")
		}
		relevant := make(map[string]struct{}, len(ranking.Case.RelevantSources))
		for _, source := range ranking.Case.RelevantSources {
			source = strings.ToLower(strings.TrimSpace(source))
			if source == "" {
				return RetrievalMetrics{}, fmt.Errorf("retrieval evaluation source is empty")
			}
			relevant[source] = struct{}{}
		}
		limit := min(k, len(ranking.Matches))
		found := map[string]struct{}{}
		firstRank := 0
		for index, match := range ranking.Matches[:limit] {
			totalMatches++
			if strings.TrimSpace(match.Locator) != "" && match.SourceEnd > match.SourceStart {
				locatableMatches++
			}
			key := strings.ToLower(strings.TrimSpace(match.Name))
			if _, ok := relevant[key]; !ok {
				continue
			}
			found[key] = struct{}{}
			if firstRank == 0 {
				firstRank = index + 1
			}
		}
		if firstRank > 0 {
			hitCases++
			metrics.MeanReciprocal += 1 / float64(firstRank)
		}
		metrics.MeanRecall += float64(len(found)) / float64(len(relevant))
	}
	metrics.HitRate = float64(hitCases) / float64(len(rankings))
	metrics.MeanRecall /= float64(len(rankings))
	metrics.MeanReciprocal /= float64(len(rankings))
	if totalMatches > 0 {
		metrics.LocatableRate = float64(locatableMatches) / float64(totalMatches)
	}
	return metrics, nil
}
