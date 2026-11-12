package research

import (
	"fmt"
	"strings"
)

// ProviderQueryWithPublicationYears formats only sources whose year constraint
// is part of the query text. Other providers use separate request parameters;
// keep their PublicationYears and YearFilterApplied audit fields alongside it.
// The caller validates years before dispatch. The connector and audit share this
// formatter so the saved expression is not a pre-filter approximation.
func ProviderQueryWithPublicationYears(source, query string, years PublicationYears) string {
	if !years.Active() {
		return query
	}
	from, to := years.From, years.To
	if from == 0 {
		from = 1000
	}
	if to == 0 {
		to = 3000
	}
	switch source {
	case "eric":
		return fmt.Sprintf("(%s) AND publicationdateyear:[%d TO %d]", strings.TrimSpace(query), from, to)
	case "core":
		return fmt.Sprintf("(%s) AND yearPublished>=%d AND yearPublished<=%d", strings.TrimSpace(query), from, to)
	case "europepmc":
		return fmt.Sprintf("(%s) AND FIRST_PDATE:[%d-01-01 TO %d-12-31]", strings.TrimSpace(query), from, to)
	default:
		return query
	}
}
