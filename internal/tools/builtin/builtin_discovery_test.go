package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestOptionalBuiltinSearchRespectsScopeAndContract(t *testing.T) {
	ctx := context.Background()
	r := tool.NewRegistry()
	d := tool.Definition{QualifiedName: "builtin.knowledge.search", Description: "fixture search", Version: "1", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: tool.RiskModerate}
	if err := r.Register(ctx, discoveryTool{d}); err != nil {
		t.Fatal(err)
	}
	search := NewToolsSearch(r, nil, func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{d.QualifiedName: true}, nil
	})
	result, err := search.Invoke(ctx, tool.Invocation{RunID: "run", Arguments: json.RawMessage(`{"kind":"builtin","query":"builtin.knowledge.search","limit":1}`)})
	if err != nil || !strings.Contains(string(result.Structured), `"kind":"builtin"`) || !strings.Contains(string(result.Structured), d.QualifiedName) || !strings.Contains(string(result.Structured), tool.DefinitionFingerprint(d)) {
		t.Fatal("original discovery cannot load an authorized optional builtin contract", string(result.Structured), err)
	}
	for _, name := range []string{"builtin.knowledge.read", "builtin.research.task.read", "builtin.resource.open"} {
		other := d
		other.QualifiedName = name
		if err := r.Register(ctx, discoveryTool{other}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"builtin.knowledge.read", "builtin.research.task.read", "builtin.resource.open", "builtin.missing.tool"} {
		raw, _ := json.Marshal(map[string]any{"kind": "builtin", "query": name, "limit": 1})
		v, err := search.Invoke(ctx, tool.Invocation{RunID: "run", Arguments: raw})
		if err != nil || strings.Contains(string(v.Structured), `"name":`) {
			t.Fatal("discovery leaked a foreign/core/internal definition", string(v.Structured), err)
		}
	}
	for _, args := range []string{`{"kind":"builtin","query":"knowledge","server":"builtin"}`, `{"kind":"other","query":"knowledge"}`, `{"kind":"builtin"}`} {
		if _, err := search.Invoke(ctx, tool.Invocation{RunID: "run", Arguments: json.RawMessage(args)}); err == nil {
			t.Fatal("ambiguous discovery accepted", args)
		}
	}
}
