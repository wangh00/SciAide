package builtin

import (
	"context"
	"encoding/json"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/researchsources"
	"testing"
)

func TestResearchSourceContractsShareCatalogAndDoNotAuthorizeCOREDownloads(t *testing.T) {
	search, _ := NewResearchSearch(nil).Definition(context.Background())
	fetch, _ := NewResearchFetch(nil).Definition(context.Background())
	workflow, _ := NewResearchWorkflowSearch(nil).Definition(context.Background())
	for _, d := range []tool.Definition{search, fetch, workflow} {
		var schema map[string]any
		if json.Unmarshal(d.InputSchema, &schema) != nil {
			t.Fatal(d.QualifiedName)
		}
		properties := schema["properties"].(map[string]any)
		var field map[string]any
		if value, ok := properties["sourceIds"]; ok {
			field = value.(map[string]any)["items"].(map[string]any)
		} else {
			field = properties["sourceId"].(map[string]any)
		}
		values := field["enum"].([]any)
		if len(values) != len(researchsources.IDs()) {
			t.Fatal("tool/source drift", d.QualifiedName)
		}
		got := map[string]bool{}
		for _, v := range values {
			got[v.(string)] = true
		}
		if !got["eric"] || !got["core"] {
			t.Fatal(d.QualifiedName)
		}
		permission := map[string]bool{}
		for _, p := range d.Permissions {
			permission[p.Resource] = true
		}
		if !permission["api.ies.ed.gov:443"] || !permission["api.core.ac.uk:443"] || permission["*"] {
			t.Fatal(d.Permissions)
		}
	}
	params := json.RawMessage(`{"query":"writing","sourceIds":["eric"],"providerQueries":{"eric":["subject:Writing"]}}`)
	if err := (tool.JSONSchemaValidator{}).Validate(workflow.InputSchema, params); err != nil {
		t.Fatal(err)
	}
	hosts := map[string]bool{}
	for _, p := range tool.ResearchMaterialNetworkPermissions() {
		hosts[p.Resource] = true
	}
	if !hosts["files.eric.ed.gov:443"] || hosts["api.core.ac.uk:443"] || hosts["core.ac.uk:443"] {
		t.Fatal(hosts)
	}
}
