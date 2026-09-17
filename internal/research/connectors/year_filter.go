package connectors

import (
	"fmt"
	research "github.com/wangh00/SciAide/internal/app/research"
	"net/url"
	"strings"
)

// arXiv's submittedDate describes submission, not publication. Do not turn a
// publication constraint into a different filter without the user's consent.
func applyPublicationYears(source string, q url.Values, y research.PublicationYears) {
	if !y.Active() {
		return
	}
	from, to := y.From, y.To
	if from == 0 {
		from = 1000
	}
	if to == 0 {
		to = 3000
	}
	switch source {
	case "pubmed":
		q.Set("datetype", "pdat")
		q.Set("mindate", fmt.Sprint(from))
		q.Set("maxdate", fmt.Sprint(to))
	case "crossref":
		q.Set("filter", fmt.Sprintf("from-pub-date:%d-01-01,until-pub-date:%d-12-31", from, to))
	case "openalex":
		q.Set("filter", fmt.Sprintf("from_publication_date:%d-01-01,to_publication_date:%d-12-31", from, to))
	case "europepmc":
		q.Set("query", fmt.Sprintf("(%s) AND FIRST_PDATE:[%d-01-01 TO %d-12-31]", strings.TrimSpace(q.Get("query")), from, to))
	case "semantic-scholar":
		q.Set("year", fmt.Sprintf("%d:%d", from, to))
	}
}
