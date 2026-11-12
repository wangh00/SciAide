package workflow

import (
	"strings"
	"testing"
)

func TestOptionalFileInputOmissionAndDefaults(t *testing.T) {
	required := Port{Name: "data", Type: TypeArray, FileKind: "tabular", Required: true, MinItems: 1, MaxItems: 2}
	optional := Port{Name: "mapping", Type: TypeArray, FileKind: "tabular", MinItems: 1, MaxItems: 2}
	for _, empty := range []string{"", "[]"} {
		optional.Default = raw(empty)
		value, _, err := validateRuntimeInputs([]Port{required, optional}, raw(`{"data":["data.csv"]}`))
		if err != nil || strings.Contains(string(value), "mapping") {
			t.Fatal("unselected optional file not omitted", string(value), err)
		}
	}
	optional.Default = raw(`["map.csv"]`)
	value, _, err := validateRuntimeInputs([]Port{required, optional}, raw(`{"data":["data.csv"]}`))
	if err != nil || !strings.Contains(string(value), "map.csv") {
		t.Fatal("nonempty default lost", err)
	}
	value, _, err = validateRuntimeInputs([]Port{required, optional}, raw(`{"data":["data.csv"],"mapping":[]}`))
	if err != nil || strings.Contains(string(value), "mapping") {
		t.Fatal("cleared optional default resurrected", string(value), err)
	}
	for _, input := range []string{`{"data":[]}`, `{"data":["d.csv"],"mapping":null}`, `{"data":["d.csv"],"mapping":[""]}`, `{"data":["a","b","c"]}`} {
		if _, _, err := validateRuntimeInputs([]Port{required, optional}, raw(input)); err == nil {
			t.Fatal("invalid explicit selection accepted", input)
		}
	}
	optional.Type = TypeString
	optional.MinItems = 0
	optional.MaxItems = 0
	optional.Default = raw(`""`)
	value, _, err = validateRuntimeInputs([]Port{required, optional}, raw(`{"data":["data.csv"]}`))
	if err != nil || strings.Contains(string(value), "mapping") {
		t.Fatal("optional string default", err)
	}
}
