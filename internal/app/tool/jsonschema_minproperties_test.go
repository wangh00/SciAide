package tool

import "testing"

func TestJSONSchemaMinimumProperties(t *testing.T) {
	v := JSONSchemaValidator{}
	schema := []byte(`{"type":"object","required":["base"],"minProperties":2,"additionalProperties":false,"properties":{"base":{"type":"string"},"text":{"type":"array","minItems":1,"items":{"type":"string"}},"segments":{"type":"array","minItems":1,"items":{"type":"string"}}}}`)
	for _, s := range []string{`{"base":"x","text":["a"]}`, `{"base":"x","segments":["b"]}`, `{"base":"x","text":["a"],"segments":["b"]}`} {
		if err := v.Validate(schema, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{`{}`, `{"base":"x"}`, `{"base":"x","text":[]}`, `{"base":"x","text":[],"segments":[]}`, `{"base":"x","unknown":1}`, `{"base":"x","text":null}`} {
		if v.Validate(schema, []byte(s)) == nil {
			t.Fatal("invalid empty/unknown payload accepted", s)
		}
	}
	for _, s := range []string{`{"minProperties":-1}`, `{"minProperties":1.5}`, `{"minProperties":"2"}`, `{"properties":{"optional":{"minProperties":-1}}}`} {
		if v.ValidateSchema([]byte(s)) == nil {
			t.Fatal("invalid schema accepted", s)
		}
	}
	if err := v.Validate([]byte(`{"minProperties":2}`), []byte(`"non-object"`)); err != nil {
		t.Fatal("object keyword applied to another type", err)
	}
}
