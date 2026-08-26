package connectors

import (
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
)

func Default() []appresearch.Connector {
	client := NewClient()
	return []appresearch.Connector{
		newOpenAlex(client, "https://api.openalex.org/works"),
		newCrossref(client, "https://api.crossref.org/works"),
		newArXiv(client, "https://export.arxiv.org/api/query", 3*time.Second),
		newPubMed(client, "https://eutils.ncbi.nlm.nih.gov/entrez/eutils", 350*time.Millisecond),
		newEuropePMC(client, "https://www.ebi.ac.uk/europepmc/webservices/rest"),
		newSemanticScholar(client, "https://api.semanticscholar.org/graph/v1/paper", time.Second),
	}
}
