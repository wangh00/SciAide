package research

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const sourceQueryProjectionVersion = "source-query-v1"

// Compile once for both dispatch and the persisted audit. A planned expression
// changes the input's origin, not the syntax understood by the endpoint.
func compileSourceQuery(sourceID, logical string, planned map[string]string, years PublicationYears) (SourceSearch, error) {
	query := strings.TrimSpace(logical)
	origin := "logical_query"
	if expression, provided := planned[sourceID]; provided {
		query, origin = strings.TrimSpace(expression), "planned_provider_query"
	}
	projection := SourceSearch{
		SourceID: sourceID, LogicalQuery: logical, InputQuery: query, QueryMode: "native_query",
		QueryOrigin: origin, QueryProjectionVersion: sourceQueryProjectionVersion,
	}
	if sourceID == "crossref" || sourceID == "semantic-scholar" {
		projection.QueryMode = "ranked_bibliographic"
	} else if sourceID == "arxiv" {
		projection.QueryMode = "arxiv_boolean"
	}
	if query == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > MaxQueryRunes {
		return projection, fmt.Errorf("source query must contain between 1 and %d valid characters", MaxQueryRunes)
	}
	switch projection.QueryMode {
	case "ranked_bibliographic":
		ranked, err := RankedBibliographicQuery(query)
		if err != nil {
			return projection, err
		}
		projection.EffectiveQuery = ranked.Query
		projection.QueryLimitations = ranked.Limitations
	case "arxiv_boolean":
		expression, err := ArXivQuery(query)
		if err != nil {
			return projection, err
		}
		projection.ProviderQuery = expression
	}
	if projection.EffectiveQuery == "" {
		projection.EffectiveQuery = query
	}
	if projection.ProviderQuery == "" {
		projection.ProviderQuery = ProviderQueryWithPublicationYears(sourceID, projection.EffectiveQuery, years)
	}
	return projection, nil
}
