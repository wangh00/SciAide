package connectors

import (
	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/researchsources"
	"net/http"
)

func Default() []appresearch.Connector { return defaultWithClient(NewClient()) }

// DefaultWithTransport keeps fixed endpoints and redirect/credential checks,
// while allowing controlled HTTP fixtures to exercise the real adapters.
func DefaultWithTransport(transport http.RoundTripper) []appresearch.Connector {
	client := NewClient()
	if transport != nil {
		client.http.Transport = transport
	}
	return defaultWithClient(client)
}

func defaultWithClient(client *Client) []appresearch.Connector {
	result := []appresearch.Connector{}
	for _, source := range researchsources.Definitions() {
		switch source.ID {
		case "openalex":
			result = append(result, newOpenAlex(client, source.Endpoint))
		case "crossref":
			result = append(result, newCrossref(client, source.Endpoint))
		case "arxiv":
			result = append(result, newArXiv(client, source.Endpoint, source.MinSpacing))
		case "pubmed":
			result = append(result, newPubMed(client, source.Endpoint, source.MinSpacing))
		case "europepmc":
			result = append(result, newEuropePMC(client, source.Endpoint))
		case "semantic-scholar":
			result = append(result, newSemanticScholar(client, source.Endpoint, source.MinSpacing))
		case "eric":
			result = append(result, newERIC(client, source.Endpoint, source.MinSpacing))
		case "core":
			result = append(result, newCORE(client, source.Endpoint, source.MinSpacing))
		}
	}
	return result
}
