package bootstrap

import (
	"strings"
	"testing"
)

func TestApplicationRegistersWebToolsAndConfiguration(t *testing.T) {
	a, e := New(Options{RootDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	tools, e := a.ToolFacade.ListTools()
	if e != nil {
		t.Fatal(e)
	}
	found := map[string]bool{}
	for _, d := range tools {
		if d.QualifiedName == "builtin.mcp.list" || d.QualifiedName == "builtin.tools.search" {
			found[d.QualifiedName] = true
			if len(d.Permissions) != 0 || !d.Idempotent {
				t.Fatal("discovery must not grant MCP execution permission")
			}
		}
		if strings.HasPrefix(d.QualifiedName, "builtin.web.") {
			found[d.QualifiedName] = true
			if len(d.Permissions) == 0 {
				t.Fatal("web tool missing permissions")
			}
		}
	}
	if !found["builtin.web.search"] || !found["builtin.web.open"] {
		t.Fatalf("missing web tools: %v", found)
	}
	if !found["builtin.mcp.list"] || !found["builtin.tools.search"] {
		t.Fatalf("missing MCP discovery tools: %v", found)
	}
}
