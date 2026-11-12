package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSchemaLengthDiagnosticReportsUnicodeActualAndLimit(t *testing.T) {
	value := []byte(`{"summary":"中🙂文","items":[1,2]}`)
	schema := []byte(`{"type":"object","properties":{"summary":{"type":"string","maxLength":2},"items":{"type":"array","maxItems":1}}}`)
	err := (JSONSchemaValidator{}).ValidateWithDiagnostics(schema, value)
	if err == nil || !strings.Contains(err.Error(), "$.summary: violates maxLength (actual=3, limit=2)") || !strings.Contains(err.Error(), "$.items: violates maxItems (actual=2, limit=1)") {
		t.Fatal("repair receives no exact Unicode/array bounds", err)
	}
}

func TestSchemaDiagnosticsListsIndependentFieldsWithoutChangingAcceptance(t *testing.T) {
	schema := []byte(`{"type":"object","additionalProperties":false,"required":["title","absent"],"properties":{"absent":{"type":"string"},"title":{"type":"string"},"clarification":{"type":"object","required":["questions"],"properties":{"questions":{"type":"array"}}},"skills":{"type":"array","items":{"type":"object","properties":{"limitations":{"type":"array","items":{"type":"string"}}}}}}}`)
	instance := []byte(`{"title":42,"clarificati":null,"clarification":null,"skills":[{"limitations":"not-an-array"}]}`)
	before := append([]byte(nil), instance...)
	err := (JSONSchemaValidator{}).ValidateWithDiagnostics(schema, instance)
	if err == nil || !bytes.Equal(before, instance) {
		t.Fatal("rejected candidate was accepted or mutated", err)
	}
	want := "$.absent: required property is missing\n$.clarificati: unknown property\n$.clarification: expected object\n$.skills[0].limitations: expected array\n$.title: expected string"
	if err.Error() != want || strings.Contains(err.Error(), "questions") {
		t.Fatal("independent diagnostics missing, unstable or fabricated under null", err)
	}
	for i := 0; i < 50; i++ {
		if repeated := (JSONSchemaValidator{}).ValidateWithDiagnostics(schema, instance); repeated == nil || repeated.Error() != want {
			t.Fatal("diagnostics depend on map iteration order", repeated)
		}
	}
}

func TestSchemaDiagnosticsPreservesEveryOriginalAssertion(t *testing.T) {
	for _, tc := range []struct{ schema, value string }{
		{`{"type":"object","properties":{"a":{"type":"integer"}}}`, `{"a":1}`},
		{`{"type":"object","properties":{"a":{"type":"integer"}}}`, `{"a":"1"}`},
		{`{"type":"object","required":["a"]}`, `{}`},
		{`{"type":"object","additionalProperties":false}`, `{"a":1}`},
		{`{"minProperties":2}`, `{"a":1}`},
		{`{"enum":[{"a":1}]}`, `{"a":2}`},
		{`{"const":{"a":1}}`, `{"a":2}`},
		{`{"type":"array","items":{"type":"number","minimum":3},"minItems":2,"uniqueItems":true}`, `[1]`},
		{`{"maxItems":1}`, `[1,2]`},
		{`{"uniqueItems":true}`, `[1,1]`},
		{`{"minLength":3,"pattern":"^a"}`, `"b"`},
		{`{"maxLength":1}`, `"abc"`},
		{`{"maximum":2}`, `3`},
		{`{"exclusiveMinimum":2}`, `2`},
		{`{"exclusiveMaximum":2}`, `2`},
		{`{"type":"boolean"}`, `false`},
		{`{"type":"null"}`, `null`},
		{`{"unknownAssertion":true}`, `{}`},
		{`{"type":"object"}`, `{} {}`},
		{`{"type":"object"}`, `{`},
		{`{`, `{}`},
	} {
		t.Run(fmt.Sprint(tc.schema, "/", tc.value), func(t *testing.T) {
			v := JSONSchemaValidator{}
			original, detailed := v.Validate([]byte(tc.schema), []byte(tc.value)), v.ValidateWithDiagnostics([]byte(tc.schema), []byte(tc.value))
			if (original == nil) != (detailed == nil) {
				t.Fatal("diagnostics changed acceptance", original, detailed)
			}
		})
	}
}

func TestSchemaDiagnosticsBoundsIssuesAndTraversal(t *testing.T) {
	value := map[string]any{}
	for i := 0; i < 40; i++ {
		value[fmt.Sprintf("field%02d", i)] = "untrusted-value-must-not-appear"
	}
	data, _ := json.Marshal(value)
	err := (JSONSchemaValidator{}).ValidateWithDiagnostics([]byte(`{"type":"object","additionalProperties":false}`), data)
	if err == nil || strings.Count(err.Error(), "unknown property") < 20 || strings.Count(err.Error(), "unknown property") > 21 || !strings.Contains(err.Error(), "diagnostic limit") || strings.Contains(err.Error(), "untrusted-value") {
		t.Fatal("diagnostic limit or value isolation failed", err)
	}
	// A long valid prefix may exhaust the diagnostic traversal. The original
	// strict rejection must still survive, even when no listed field was found.
	array := make([]any, 5000)
	for i := range array {
		array[i] = 1
	}
	array[len(array)-1] = "bad"
	data, _ = json.Marshal(array)
	err = (JSONSchemaValidator{}).ValidateWithDiagnostics([]byte(`{"type":"array","items":{"type":"integer"}}`), data)
	if err == nil || !strings.Contains(err.Error(), "expected integer") {
		t.Fatal("diagnostic traversal cap swallowed the authoritative rejection", err)
	}
}

func TestSchemaDiagnosticsCannotBypassWorkOrTextLimits(t *testing.T) {
	array := make([]any, 20000)
	for i := range array {
		array[i] = i
	}
	schema := []byte(`{"type":"object","required":["missing"],"properties":{"values":{"type":"array","uniqueItems":true,"items":{"type":"integer"}}}}`)
	data, _ := json.Marshal(map[string]any{"values": array})
	err := (JSONSchemaValidator{}).ValidateWithDiagnostics(schema, data)
	if err == nil || !strings.Contains(err.Error(), "$.missing: required property is missing") || !strings.Contains(err.Error(), "diagnostic limit") {
		t.Fatal("long uniqueItems assertion escaped diagnostic work limit", err)
	}
	values := map[string]any{}
	for i := 0; i < 20; i++ {
		values[fmt.Sprint(i)+strings.Repeat("界", 5000)] = nil
	}
	data, _ = json.Marshal(values)
	err = (JSONSchemaValidator{}).ValidateWithDiagnostics([]byte(`{"type":"object","additionalProperties":false}`), data)
	if err == nil || len([]rune(err.Error())) > 6000 || !strings.Contains(err.Error(), "SHA256=") || !strings.Contains(err.Error(), "truncated") {
		t.Fatal("model-controlled key amplified correction text", len([]rune(err.Error())))
	}
	contract, parseErr := parseSchema([]byte(`{"type":"object","additionalProperties":false,"properties":{"z":{"type":"integer","const":1}}}`))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	values = map[string]any{"z": false}
	for i := 0; i < 19; i++ {
		values[fmt.Sprintf("a%02d", i)] = nil
	}
	d := schemaDiagnostics{}
	d.walk(contract, values, "$")
	if len(d.issues) != 20 || !d.limited {
		t.Fatal("two assertions at one node exceeded the shared diagnostic limit", len(d.issues), d.limited)
	}
}

func TestSchemaDiagnosticsKeepsPrimaryAfterPartialTraversal(t *testing.T) {
	primary := errors.New("$.z: expected string")
	d := schemaDiagnostics{issues: []error{errors.New("$.a[0]: expected integer")}, visited: 4096, limited: true}
	err := d.result(primary)
	if err == nil || !strings.Contains(err.Error(), "$.a[0]: expected integer") || !strings.Contains(err.Error(), primary.Error()) || !strings.Contains(err.Error(), "diagnostic limit") || !errors.Is(err, primary) {
		t.Fatal("primary lost after partially useful diagnostics", err)
	}
	d = schemaDiagnostics{issues: []error{errors.New(primary.Error())}}
	if err := d.result(primary); !errors.Is(err, primary) || strings.Count(err.Error(), primary.Error()) != 1 {
		t.Fatal("same-text diagnostic lost the authoritative error identity", err)
	}
}
