package wails

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestConditionalViewOmitsUnchangedPayloadWithoutHostCache(t *testing.T) {
	data := map[string]any{"code": strings.Repeat("complete code", 10000), "status": "running"}
	first, err := conditionalView(data, "", nil)
	if err != nil || first.Unchanged || len(first.Value) < 100000 {
		t.Fatal(err)
	}
	same, err := conditionalView(data, first.Revision, nil)
	if err != nil || !same.Unchanged || len(same.Value) != 0 {
		t.Fatal(err, same)
	}
	wire, _ := json.Marshal(same)
	if len(wire) > 120 {
		t.Fatal("unchanged response too large", len(wire))
	}
	data["status"] = "completed"
	next, err := conditionalView(data, first.Revision, nil)
	if err != nil || next.Unchanged || next.Revision == first.Revision || !strings.Contains(string(next.Value), "complete code") {
		t.Fatal("changed snapshot lost data", err)
	}
	if _, err := conditionalView(data, next.Revision, errors.New("scope rejected")); err == nil {
		t.Fatal("conditional read bypassed access error")
	}
}

func TestObjectViewOnlySendsChangedFields(t *testing.T) {
	data := map[string]any{"steps": strings.Repeat("original code", 100000), "elapsed": 1, "error": "old"}
	first, err := conditionalObjectView(data, "", nil)
	if err != nil || len(first.Value) < 100000 {
		t.Fatal(err)
	}
	data["elapsed"] = 2
	delete(data, "error")
	next, err := conditionalObjectView(data, first.Revision, nil)
	if err != nil || len(next.Value) != 0 || len(next.Patch) > 100 || !strings.Contains(string(next.Patch), `"elapsed":2`) || len(next.Removed) != 1 || next.Removed[0] != "error" {
		t.Fatal("bad field update", next, err)
	}
	same, err := conditionalObjectView(data, next.Revision, nil)
	if err != nil || !same.Unchanged {
		t.Fatal("unchanged mismatch", same, err)
	}
	if _, err := conditionalObjectView(data, next.Revision, errors.New("scope rejected")); err == nil {
		t.Fatal("scope check bypassed")
	}
	null, err := conditionalObjectView(nil, next.Revision, nil)
	if err != nil || string(null.Value) != "null" || null.Unchanged {
		t.Fatal("missing run not propagated")
	}
	restored, err := conditionalObjectView(data, null.Revision, nil)
	if err != nil || len(restored.Value) == 0 {
		t.Fatal("null baseline did not reset")
	}
}
