// Package researchsources owns the fixed public-source contracts shared by
// connector assembly, tool schemas and material-download permissions.
// Changing this catalog never edits a persisted workflow/tool snapshot.
package researchsources

import "time"

type Definition struct {
	ID                     string
	Endpoint               string
	QueryMode              string
	PublicationYears       bool
	SearchIncludesMetadata bool
	// First-attempt search HTTP reservation, not including retries or enrichment.
	SearchHTTPRequests      int
	ExplicitSelection       bool
	OpenAlexMayInheritHosts bool
	PlanQuery               bool
	RequiredPlanQuery       bool
	MinSpacing              time.Duration
	FullTextHosts           []string
}

var definitions = []Definition{
	{ID: "openalex", OpenAlexMayInheritHosts: true, Endpoint: "https://api.openalex.org/works", QueryMode: "native_query", PublicationYears: true, PlanQuery: true, RequiredPlanQuery: true, FullTextHosts: []string{"www.nature.com", "jamanetwork.com"}},
	{ID: "crossref", OpenAlexMayInheritHosts: true, Endpoint: "https://api.crossref.org/works", QueryMode: "ranked_bibliographic", PublicationYears: true, PlanQuery: true, RequiredPlanQuery: true, FullTextHosts: []string{"www.nature.com", "jamanetwork.com"}},
	{ID: "arxiv", OpenAlexMayInheritHosts: true, Endpoint: "https://export.arxiv.org/api/query", QueryMode: "arxiv_boolean", MinSpacing: 3 * time.Second, FullTextHosts: []string{"arxiv.org", "export.arxiv.org"}},
	{ID: "pubmed", SearchHTTPRequests: 2, OpenAlexMayInheritHosts: true, Endpoint: "https://eutils.ncbi.nlm.nih.gov/entrez/eutils", QueryMode: "native_query", PublicationYears: true, PlanQuery: true, RequiredPlanQuery: true, MinSpacing: 350 * time.Millisecond, FullTextHosts: []string{"pmc.ncbi.nlm.nih.gov", "www.ncbi.nlm.nih.gov"}},
	{ID: "europepmc", OpenAlexMayInheritHosts: true, Endpoint: "https://www.ebi.ac.uk/europepmc/webservices/rest", QueryMode: "native_query", PublicationYears: true, PlanQuery: true, RequiredPlanQuery: true, FullTextHosts: []string{"europepmc.org", "www.ebi.ac.uk"}},
	{ID: "semantic-scholar", OpenAlexMayInheritHosts: true, Endpoint: "https://api.semanticscholar.org/graph/v1/paper", QueryMode: "ranked_bibliographic", PublicationYears: true, MinSpacing: time.Second, FullTextHosts: []string{"www.semanticscholar.org", "semanticscholar.org"}},
	{ID: "eric", SearchIncludesMetadata: true, Endpoint: "https://api.ies.ed.gov/eric/", QueryMode: "native_query", PublicationYears: true, PlanQuery: true, MinSpacing: time.Second, FullTextHosts: []string{"files.eric.ed.gov"}},
	// CORE is anonymous metadata discovery only in this contract. It is not part
	// of an omitted-source search, and no CORE/repository PDF host is authorized.
	{ID: "core", SearchIncludesMetadata: true, Endpoint: "https://api.core.ac.uk/v3", QueryMode: "native_query", PublicationYears: true, ExplicitSelection: true, PlanQuery: true, MinSpacing: 6 * time.Second},
}

func Definitions() []Definition {
	out := make([]Definition, len(definitions))
	for i, d := range definitions {
		d.FullTextHosts = append([]string(nil), d.FullTextHosts...)
		out[i] = d
	}
	return out
}

func Lookup(id string) (Definition, bool) {
	for _, d := range definitions {
		if d.ID == id {
			d.FullTextHosts = append([]string(nil), d.FullTextHosts...)
			return d, true
		}
	}
	return Definition{}, false
}

func IDs() []string {
	out := make([]string, 0, len(definitions))
	for _, d := range definitions {
		out = append(out, d.ID)
	}
	return out
}

func PlanQueryIDs(requiredOnly bool) []string {
	out := []string{}
	for _, d := range definitions {
		if d.PlanQuery && (!requiredOnly || d.RequiredPlanQuery) {
			out = append(out, d.ID)
		}
	}
	return out
}
