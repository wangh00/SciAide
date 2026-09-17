package modelutil

import (
	"encoding/json"
	"github.com/wangh00/SciAide/internal/model"
	"strings"
	"testing"
)

func TestAliasBuilderRejectsDuplicatesAndCollisions(t *testing.T) {
	for _, names := range [][]string{{"builtin.knowledge.search", "knowledge_search"}, {"builtin.a.b_c", "builtin.a_b.c"}, {"builtin.knowledge.search", "builtin.knowledge.search"}} {
		var defs []model.ToolDefinition
		for _, name := range names {
			defs = append(defs, model.ToolDefinition{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)})
		}
		if _, _, err := BuildToolAliases(defs); err == nil {
			t.Fatalf("accepted collision: %v", names)
		}
	}
}

func TestExactNamesDoNotRecoverHistoricalSpellings(t *testing.T) {
	aliases := map[string]string{"resource_open": "builtin.resource.open", "skill_load": "builtin.skill.load"}
	for _, value := range []string{"resource_open__", " resource_open", "builtin_resource_open", "builtin.resource_open", "builtin_skill_load_", "builtin_skill_load_0123456789ab"} {
		if got := ResolveProviderToolName(value, aliases); got != value {
			t.Fatalf("recovered obsolete name %q -> %q", value, got)
		}
	}
}

func TestBuiltinNamesAreShortAndResolveOnlyDeclaredTools(t *testing.T) {
	for canonical, alias := range map[string]string{
		"builtin.knowledge.search":         "knowledge_search",
		"builtin.resource.open":            "resource_open",
		"builtin.resource.search":          "resource_search",
		"builtin.python.kernel.execute":    "python_kernel_execute",
		"builtin.skill.resource.read_text": "skill_resource_read_text",
	} {
		if got := ProviderToolName(canonical); got != alias {
			t.Fatalf("%s => %s", canonical, got)
		}
		if got := ResolveProviderToolName(alias, map[string]string{alias: canonical}); got != canonical {
			t.Fatal(got)
		}
		if got := ResolveProviderToolName(alias, nil); got != alias {
			t.Fatal("undeclared tool resolved")
		}
	}
	for _, bad := range []string{"knowledge_search__", "knowledge_searc", "builtin_knowledge_search_"} {
		if got := ResolveProviderToolName(bad, map[string]string{"knowledge_search": "builtin.knowledge.search"}); got != bad {
			t.Fatalf("guessed short name: %s", got)
		}
	}
}

func TestSingleTrailingSeparatorRequiresDeclaredTargetAndExactWins(t *testing.T) {
	aliases := map[string]string{"knowledge_search": "builtin.knowledge.search"}
	if ResolveProviderToolName("knowledge_search_", aliases) != "builtin.knowledge.search" {
		t.Fatal("single separator not tolerated")
	}
	if ResolveProviderToolName("knowledge_search_", nil) != "knowledge_search_" {
		t.Fatal("undeclared target resolved")
	}
	aliases["knowledge_search_"] = "external_exact"
	if ResolveProviderToolName("knowledge_search_", aliases) != "external_exact" {
		t.Fatal("exact match lost precedence")
	}
}

func TestExternalToolNamesKeepStableNamespace(t *testing.T) {
	left, right := ProviderToolName("mcp.server.a_b"), ProviderToolName("mcp.server.a.b")
	if left == right || !strings.HasPrefix(left, "mcp_server_") || len(left) > 64 {
		t.Fatalf("external collision: %s %s", left, right)
	}
	if ProviderToolName("submit_stage_result") != "submit_stage_result" {
		t.Fatal("submission name changed")
	}
}
