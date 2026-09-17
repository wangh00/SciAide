package builtin

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/resource"
)

func TestResourceSchemaStableWhileIssuedActionsGrow(t *testing.T) {
	engine, repo, resolver, request := resourceFixture(t)
	if err := os.WriteFile(filepath.Join(resolver.root, "data.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := engine.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	session, _, _ := repo.GetSession(context.Background(), request.RunID)
	newAction := resource.NewAction(session.Scope, resource.Seed{Kind: "menu", Label: "new page"}, false, engine.now())
	if err := repo.PutActions(context.Background(), session.Scope, []resource.Action{newAction}); err != nil {
		t.Fatal(err)
	}
	next, err := engine.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Definitions, next.Definitions) {
		t.Fatal("resource discovery changed tool schema")
	}
	for _, d := range next.Definitions {
		if strings.Contains(string(d.InputSchema), `"enum"`) {
			t.Fatal("run IDs in schema")
		}
	}
	args := resourceArgs(map[string]string{"actionId": newAction.ID})
	if first.Allows(resource.OpenTool, args) || !next.Allows(resource.OpenTool, args) {
		t.Fatal("request-bound action set not enforced")
	}
	if _, err := invokeResource(t, engine, resolver, "res_"+strings.Repeat("0", 32)); err == nil {
		t.Fatal("forged ID executed")
	}
	if next.Allows(resource.SearchTool, args) {
		t.Fatal("open capability used as search")
	}
}
