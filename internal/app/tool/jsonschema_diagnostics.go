package tool

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// ValidateWithDiagnostics has exactly the same acceptance rule as Validate.
// On rejection it lists independent field defects together, so a bounded model
// correction is not spent discovering one sibling error at a time. It never
// changes the instance, guesses fields, or interprets scientific correctness.
func (v JSONSchemaValidator) ValidateWithDiagnostics(schema, instance []byte) error {
	primary := v.Validate(schema, instance)
	if primary == nil {
		return nil
	}
	contract, err := parseSchema(schema)
	if err != nil {
		return primary
	}
	decoder := json.NewDecoder(bytes.NewReader(instance))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || requireJSONEOF(decoder, "instance") != nil {
		return primary
	}
	d := schemaDiagnostics{}
	d.walk(contract, value, "$")
	return d.result(primary)
}

func (d *schemaDiagnostics) result(primary error) error {
	foundPrimary := false
	for index, issue := range d.issues {
		if issue.Error() == primary.Error() {
			d.issues[index] = primary
			foundPrimary = true
		}
	}
	if !foundPrimary {
		d.issues = append(d.issues, primary)
	}
	// At most 20 collected fields plus the authoritative primary rejection.
	// Bound each line as well as the count: model-controlled property names
	// must not multiply a small candidate into a huge correction prompt.
	for index, issue := range d.issues {
		text := []rune(issue.Error())
		if len(text) > 256 {
			message := fmt.Sprintf("%s [path truncated; SHA256=%x] %s", string(text[:80]), sha256.Sum256([]byte(issue.Error())), string(text[len(text)-80:]))
			d.issues[index] = &schemaDiagnosticSummary{message: message, cause: issue}
			d.limited = true
		}
	}
	if d.limited {
		d.issues = append(d.issues, errors.New("Schema diagnostic limit reached; remaining fields are not listed. The candidate is still rejected; check the complete frozen schema."))
	}
	return errors.Join(d.issues...)
}

type schemaDiagnosticSummary struct {
	message string
	cause   error
}

func (e *schemaDiagnosticSummary) Error() string { return e.message }
func (e *schemaDiagnosticSummary) Unwrap() error { return e.cause }

type schemaDiagnostics struct {
	issues  []error
	visited int
	limited bool
}

func (d *schemaDiagnostics) stop() bool {
	if len(d.issues) >= 20 || d.visited >= 4096 {
		d.limited = true
		return true
	}
	return false
}

func (d *schemaDiagnostics) add(err error) bool {
	if d.stop() {
		return false
	}
	d.issues = append(d.issues, err)
	return true
}

func (d *schemaDiagnostics) walk(rawSchema, value any, path string) {
	if d.stop() {
		return
	}
	d.visited++
	schema := rawSchema.(map[string]any) // parseSchema already checked every node.
	// Reuse the authoritative validator for this node's own assertions; child
	// assertions are visited separately so one defect cannot mask its siblings.
	local := make(map[string]any, len(schema))
	for key, item := range schema {
		if key != "properties" && key != "required" && key != "additionalProperties" && key != "items" && key != "uniqueItems" && key != "const" && key != "enum" {
			local[key] = item
		}
	}
	if constant, exists := schema["const"]; exists {
		if equal, complete := d.equal(constant, value); !complete {
			return
		} else if !equal {
			if !d.add(fmt.Errorf("%s: value does not match const", path)) {
				return
			}
		}
	}
	if values, exists := schema["enum"].([]any); exists {
		matched := false
		for _, candidate := range values {
			equal, complete := d.equal(candidate, value)
			if !complete {
				return
			}
			if equal {
				matched = true
				break
			}
		}
		if !matched {
			if !d.add(fmt.Errorf("%s: value is outside enum", path)) {
				return
			}
		}
	}
	if err := validateSchemaNode(local, value, path); err != nil {
		if !d.add(err) {
			return
		}
		if expected, ok := schema["type"].(string); ok && !matchesJSONType(expected, value) {
			return // No fictitious child defects under a value of the wrong type.
		}
	}
	if object, ok := value.(map[string]any); ok {
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, item := range required {
			if d.stop() {
				return
			}
			d.visited++
			if _, exists := object[item.(string)]; !exists {
				if !d.add(fmt.Errorf("%s.%s: required property is missing", path, item)) {
					return
				}
			}
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if d.stop() {
				return
			}
			d.visited++
			child, declared := properties[key]
			if declared {
				d.walk(child, object[key], path+"."+key)
			} else if allowed, set := schema["additionalProperties"].(bool); set && !allowed {
				if !d.add(fmt.Errorf("%s.%s: unknown property", path, key)) {
					return
				}
			}
		}
	}
	if array, ok := value.([]any); ok {
		if unique, _ := schema["uniqueItems"].(bool); unique {
			duplicate := false
			for i := 0; i < len(array) && !duplicate; i++ {
				for j := i + 1; j < len(array); j++ {
					equal, complete := d.equal(array[i], array[j])
					if !complete {
						return
					}
					if equal {
						duplicate = true
						break
					}
				}
			}
			if duplicate {
				if !d.add(fmt.Errorf("%s: array items must be unique", path)) {
					return
				}
			}
		}
		if child, exists := schema["items"]; exists {
			for index, item := range array {
				if d.stop() {
					return
				}
				d.walk(child, item, fmt.Sprintf("%s[%d]", path, index))
			}
		}
	}
}

// Compare parsed JSON values with DeepEqual semantics, charging EVERY visited
// component/pair. A long uniqueItems array cannot bypass the diagnostic budget.
func (d *schemaDiagnostics) equal(a, b any) (bool, bool) {
	if d.stop() {
		return false, false
	}
	d.visited++
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false, true
		}
		for key, value := range left {
			other, exists := right[key]
			if !exists {
				return false, true
			}
			if same, complete := d.equal(value, other); !complete || !same {
				return same, complete
			}
		}
		return true, true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false, true
		}
		for i, value := range left {
			if same, complete := d.equal(value, right[i]); !complete || !same {
				return same, complete
			}
		}
		return true, true
	default:
		return reflect.DeepEqual(a, b), true
	}
}
