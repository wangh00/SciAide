package tool

import "testing"

func TestValidateSchemaSeparatesContractFromInstance(t *testing.T) {
	validator := JSONSchemaValidator{}
	valid := []byte(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`)
	if err := validator.ValidateSchema(valid); err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(valid, []byte(`{}`)); err == nil {
		t.Fatal("missing required instance field was accepted")
	}
	for _, schema := range []string{
		`{"required":["value","value"]}`,
		`{"properties":{"nested":{"required":["x","x"]}}}`,
		`{"items":{"required":["x","x"]}}`,
		`{"required":[12]}`,
		`{"type":"object","additionalProperties":false,"required":["x"],"properties":{}}`,
		`{"properties":{"nested":{"additionalProperties":false,"required":["x"]}}}`,
		`{"unknownAssertion":true}`,
		`{} {}`,
		`{`,
	} {
		if err := validator.ValidateSchema([]byte(schema)); err == nil {
			t.Errorf("accepted invalid schema: %s", schema)
		}
	}
}
