package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	research "github.com/wangh00/SciAide/internal/app/research"
)

func TestNonemptySourcePageWithoutUsableRecordsIsNotEmpty(t *testing.T) {
	cases := []struct {
		id, identityField, recordID string
		valid                       map[string]any
		connector                   func(*Client, string) research.Connector
	}{
		{"crossref", "DOI", "10.5555/retained", map[string]any{"DOI": "10.5555/retained", "title": []string{"Surviving writing feedback study"}, "abstract": "Original abstract retained.", "issued": map[string]any{"date-parts": [][]int{{2025}}}}, func(c *Client, b string) research.Connector { return newCrossref(c, b) }},
		{"semantic-scholar", "paperId", "KeepPaper", map[string]any{"paperId": "KeepPaper", "title": "Surviving writing feedback study", "abstract": "Original abstract retained.", "year": 2025}, func(c *Client, b string) research.Connector { return newSemanticScholar(c, b, 0) }},
		{"openalex", "id", "W123", map[string]any{"id": "https://openalex.org/W123", "title": "Surviving writing feedback study", "publication_year": 2025, "abstract_inverted_index": map[string][]int{"Original": {0}, "abstract": {1}, "retained.": {2}}}, func(c *Client, b string) research.Connector { return newOpenAlex(c, b) }},
		{"europepmc", "id", "MED/123", map[string]any{"id": "123", "source": "MED", "title": "Surviving writing feedback study", "abstractText": "Original abstract retained.", "pubYear": "2025"}, func(c *Client, b string) research.Connector { return newEuropePMC(c, b) }},
		{"eric", "id", "EJ1234567", map[string]any{"id": "EJ1234567", "title": "Surviving writing feedback study", "description": "Original abstract retained.", "publicationdateyear": 2025}, func(c *Client, b string) research.Connector { return newERIC(c, b, 0) }},
		{"core", "id", "123", map[string]any{"id": 123, "title": "Surviving writing feedback study", "abstract": "Original abstract retained.", "yearPublished": 2025}, func(c *Client, b string) research.Connector { return newCORE(c, b, 0) }},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			withoutID := cloneRecordFields(tc.valid)
			delete(withoutID, tc.identityField)
			withoutTitle := cloneRecordFields(tc.valid)
			delete(withoutTitle, "title")
			badPages := map[string][]any{
				"missing-identity": {withoutID},
				"missing-title":    {withoutTitle},
				"split-fields":     {withoutID, withoutTitle},
				"null-record":      {nil},
			}
			if tc.id == "openalex" {
				preferred := cloneRecordFields(tc.valid)
				preferred["display_name"] = " \t"
				badPages["blank-preferred-title"] = []any{preferred}
			}
			if tc.id != "eric" && tc.id != "core" {
				// The provider may over-return. A usable item outside the admitted
				// converted-record window must not make an unusable page cacheable.
				badPages["valid-only-outside-limit"] = []any{withoutTitle, withoutTitle, withoutTitle, withoutTitle, withoutTitle, tc.valid}
				badPages["source-cannot-override-limit"] = badPages["valid-only-outside-limit"]
			}
			for name, bad := range badPages {
				t.Run(name, func(t *testing.T) {
					broken := recordPageJSON(t, tc.id, bad)
					if name == "source-cannot-override-limit" {
						var object map[string]any
						if err := json.Unmarshal([]byte(broken), &object); err != nil {
							t.Fatal(err)
						}
						object["searchLimit"] = 1000
						object["SearchLimit"] = 1000
						encoded, err := json.Marshal(object)
						if err != nil {
							t.Fatal(err)
						}
						broken = string(encoded)
					}
					// A valid record alongside an invalid one must not be discarded.
					good := recordPageJSON(t, tc.id, []any{withoutID, tc.valid})
					if name == "valid-only-outside-limit" || name == "source-cannot-override-limit" {
						// Unlike title-less records with IDs, records the adapter drops
						// for missing IDs must not consume the converted-work window.
						good = recordPageJSON(t, tc.id, []any{withoutID, withoutID, withoutID, withoutID, withoutID, tc.valid})
					}
					var calls atomic.Int32
					server, client := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if calls.Add(1) == 1 {
							fmt.Fprint(w, broken)
							return
						}
						fmt.Fprint(w, good)
					})
					defer server.Close()
					service, err := research.NewService([]research.Connector{tc.connector(client, server.URL)})
					if err != nil {
						t.Fatal(err)
					}
					command := research.SearchCommand{Query: "peer feedback writing", SourceIDs: []string{tc.id}, Limit: 5}
					first, err := service.Search(context.Background(), command)
					if err != nil || !first.Partial || len(first.Works) != 0 || first.Sources[0].Status != research.SearchFailed || first.Sources[0].ErrorCode != research.FailureInvalidData || first.Sources[0].Retryable || calls.Load() != 1 {
						t.Fatalf("nonempty unusable page became empty/success or retried: result=%+v HTTP=%d err=%v", first, calls.Load(), err)
					}
					for attempt := 0; attempt < 2; attempt++ {
						result, err := service.Search(context.Background(), command)
						if err != nil || result.Partial || len(result.Works) != 1 || calls.Load() != 2 {
							t.Fatalf("explicit retry/cache failed or valid mixed-page record lost: result=%+v HTTP=%d err=%v", result, calls.Load(), err)
						}
						w := result.Works[0]
						if w.SourceRecordID != tc.recordID || w.Title != "Surviving writing feedback study" || w.Abstract != "Original abstract retained." || w.Year != 2025 {
							t.Fatalf("valid record was rewritten: %+v", w)
						}
					}
				})
			}
		})
	}
}

func cloneRecordFields(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for k, v := range value {
		result[k] = v
	}
	return result
}

func recordPageJSON(t *testing.T, source string, records []any) string {
	t.Helper()
	var value any
	switch source {
	case "crossref":
		value = map[string]any{"message": map[string]any{"items": records}}
	case "semantic-scholar":
		value = map[string]any{"data": records}
	case "openalex":
		value = map[string]any{"results": records}
	case "europepmc":
		value = map[string]any{"resultList": map[string]any{"result": records}}
	case "eric":
		value = map[string]any{"response": map[string]any{"numFound": len(records), "docs": records}}
	case "core":
		value = map[string]any{"totalHits": len(records), "results": records}
	default:
		t.Fatal("unexpected fixture source", source)
	}
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
