package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSafeActivityArgumentsRedactsSensitiveFields(t *testing.T) {
	raw := json.RawMessage(`{"path":"analysis/results.csv","apiKey":"sk-do-not-show","nested":{"Authorization":"Bearer do-not-show"},"command":"Invoke-WebRequest https://example.test/?token=do-not-show"}`)
	projected := SafeActivityArguments(raw)
	var value map[string]any
	if err := json.Unmarshal(projected, &value); err != nil {
		t.Fatalf("projection is not JSON: %v", err)
	}
	if value["apiKey"] != "[已隐藏]" {
		t.Fatalf("apiKey was not redacted: %#v", value["apiKey"])
	}
	nested, ok := value["nested"].(map[string]any)
	if !ok || nested["Authorization"] != "[已隐藏]" {
		t.Fatalf("nested authorization was not redacted: %#v", value["nested"])
	}
	if value["path"] != "analysis/results.csv" || strings.Contains(value["command"].(string), "do-not-show") {
		t.Fatalf("safe fields changed: %#v", value)
	}
}

func TestSafeActivityPermissionsAreBoundedAndRedacted(t *testing.T) {
	permissions := SafeActivityPermissions([]PermissionRequirement{{Kind: PermissionNetworkDomain, Resource: "https://example.test/?token=do-not-show"}, {Kind: PermissionWorkspaceRead, Resource: strings.Repeat("x", 600)}})
	if len(permissions) != 2 || strings.Contains(permissions[0].Resource, "do-not-show") || len([]rune(permissions[1].Resource)) > activityMaxResourceRunes+16 {
		t.Fatalf("permissions projection = %#v", permissions)
	}
}

func TestSafeActivityArgumentsBoundsStringsAndPayload(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"code": strings.Repeat("x", 20_000)})
	projected := SafeActivityArguments(raw)
	if len(projected) > ActivityArgumentProjectionBytes {
		t.Fatalf("projection exceeds bound: %d", len(projected))
	}
	var value map[string]any
	if err := json.Unmarshal(projected, &value); err != nil {
		t.Fatal(err)
	}
	code, ok := value["code"].(string)
	if !ok || !strings.Contains(code, "已截断") || len([]rune(code)) > activityMaxStringRunes+16 {
		t.Fatalf("large scalar was not bounded: %#v", value)
	}

	short, _ := json.Marshal(map[string]any{"code": strings.Repeat("y", activityMaxStringRunes+20)})
	projected = SafeActivityArguments(short)
	if !strings.Contains(string(projected), "已截断") {
		t.Fatalf("large scalar was not marked truncated: %s", projected)
	}
}

func TestSafeActivityArgumentsRejectsInvalidJSON(t *testing.T) {
	if got := SafeActivityArguments(json.RawMessage(`{"unterminated"`)); got != nil {
		t.Fatalf("invalid JSON projection = %s", got)
	}
}
