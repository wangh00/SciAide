package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// JSONSchemaValidator implements the deliberately bounded JSON Schema subset
// accepted by P2 builtin tools: type, properties, required,
// additionalProperties, items, enum, const, min/max, length and pattern.
// Unsupported assertion keywords fail closed instead of being ignored.
type JSONSchemaValidator struct{}

func (JSONSchemaValidator) Validate(schema, instance []byte) error {
	var schemaValue, instanceValue any
	decoder := json.NewDecoder(bytes.NewReader(schema))
	decoder.UseNumber()
	if err := decoder.Decode(&schemaValue); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	if err := validateSchemaSyntax(schemaValue, "$schema"); err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(instance))
	decoder.UseNumber()
	if err := decoder.Decode(&instanceValue); err != nil {
		return fmt.Errorf("invalid instance: %w", err)
	}
	return validateSchemaNode(schemaValue, instanceValue, "$")
}

func validateSchemaSyntax(rawSchema any, path string) error {
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema node must be an object", path)
	}
	for keyword := range schema {
		if _, supported := supportedSchemaKeywords[keyword]; !supported {
			return fmt.Errorf("%s: unsupported schema keyword %q", path, keyword)
		}
	}
	if raw, exists := schema["type"]; exists {
		typeName, ok := raw.(string)
		if !ok || !supportedJSONType(typeName) {
			return fmt.Errorf("%s: unsupported schema type %v", path, raw)
		}
	}
	if raw, exists := schema["properties"]; exists {
		properties, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: properties must be an object", path)
		}
		for name, child := range properties {
			if err := validateSchemaSyntax(child, path+".properties."+name); err != nil {
				return err
			}
		}
	}
	if child, exists := schema["items"]; exists {
		if err := validateSchemaSyntax(child, path+".items"); err != nil {
			return err
		}
	}
	if raw, exists := schema["required"]; exists {
		required, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s: required must be an array", path)
		}
		for _, item := range required {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("%s: required entries must be strings", path)
			}
		}
	}
	if raw, exists := schema["additionalProperties"]; exists {
		if _, ok := raw.(bool); !ok {
			return fmt.Errorf("%s: additionalProperties must be boolean in P2", path)
		}
	}
	if raw, exists := schema["enum"]; exists {
		if values, ok := raw.([]any); !ok || len(values) == 0 {
			return fmt.Errorf("%s: enum must be a non-empty array", path)
		}
	}
	if raw, exists := schema["pattern"]; exists {
		pattern, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%s: pattern must be a string", path)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s: invalid pattern", path)
		}
	}
	for _, keyword := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
		if raw, exists := schema[keyword]; exists {
			if _, ok := raw.(json.Number); !ok {
				return fmt.Errorf("%s: %s must be a number", path, keyword)
			}
		}
	}
	for _, keyword := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
		if raw, exists := schema[keyword]; exists {
			number, ok := raw.(json.Number)
			if !ok {
				return fmt.Errorf("%s: %s must be an integer", path, keyword)
			}
			limit, err := number.Int64()
			if err != nil || limit < 0 || limit > math.MaxInt {
				return fmt.Errorf("%s: invalid %s", path, keyword)
			}
		}
	}
	if raw, exists := schema["uniqueItems"]; exists {
		if _, ok := raw.(bool); !ok {
			return fmt.Errorf("%s: uniqueItems must be boolean", path)
		}
	}
	return nil
}

var supportedSchemaKeywords = map[string]struct{}{
	"$schema": {}, "$id": {}, "title": {}, "description": {}, "default": {}, "examples": {},
	"type": {}, "properties": {}, "required": {}, "additionalProperties": {}, "items": {},
	"enum": {}, "const": {}, "minimum": {}, "maximum": {}, "exclusiveMinimum": {}, "exclusiveMaximum": {},
	"minLength": {}, "maxLength": {}, "pattern": {}, "minItems": {}, "maxItems": {}, "uniqueItems": {},
}

func validateSchemaNode(rawSchema, instance any, path string) error {
	schema, ok := rawSchema.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema node must be an object", path)
	}
	for keyword := range schema {
		if _, supported := supportedSchemaKeywords[keyword]; !supported {
			return fmt.Errorf("%s: unsupported schema keyword %q", path, keyword)
		}
	}
	if expected, exists := schema["type"]; exists {
		typeName, ok := expected.(string)
		if !ok || !matchesJSONType(typeName, instance) {
			return fmt.Errorf("%s: expected %v", path, expected)
		}
	}
	if constant, exists := schema["const"]; exists && !reflect.DeepEqual(constant, instance) {
		return fmt.Errorf("%s: value does not match const", path)
	}
	if enum, exists := schema["enum"]; exists {
		values, ok := enum.([]any)
		if !ok {
			return fmt.Errorf("%s: enum must be an array", path)
		}
		matched := false
		for _, candidate := range values {
			if reflect.DeepEqual(candidate, instance) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: value is outside enum", path)
		}
	}
	if object, ok := instance.(map[string]any); ok {
		if err := validateObjectSchema(schema, object, path); err != nil {
			return err
		}
	}
	if array, ok := instance.([]any); ok {
		if err := validateArraySchema(schema, array, path); err != nil {
			return err
		}
	}
	if value, ok := instance.(string); ok {
		if err := validateStringSchema(schema, value, path); err != nil {
			return err
		}
	}
	if value, ok := instance.(json.Number); ok {
		if err := validateNumberSchema(schema, value, path); err != nil {
			return err
		}
	}
	return nil
}

func validateObjectSchema(schema, object map[string]any, path string) error {
	properties := map[string]any{}
	if raw, exists := schema["properties"]; exists {
		var ok bool
		properties, ok = raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: properties must be an object", path)
		}
	}
	if raw, exists := schema["required"]; exists {
		required, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s: required must be an array", path)
		}
		for _, item := range required {
			name, ok := item.(string)
			if !ok {
				return fmt.Errorf("%s: required entries must be strings", path)
			}
			if _, exists := object[name]; !exists {
				return fmt.Errorf("%s.%s: required property is missing", path, name)
			}
		}
	}
	allowAdditional := true
	if raw, exists := schema["additionalProperties"]; exists {
		var ok bool
		allowAdditional, ok = raw.(bool)
		if !ok {
			return fmt.Errorf("%s: additionalProperties must be boolean in P2", path)
		}
	}
	for name, value := range object {
		propertySchema, exists := properties[name]
		if !exists {
			if !allowAdditional {
				return fmt.Errorf("%s.%s: unknown property", path, name)
			}
			continue
		}
		if err := validateSchemaNode(propertySchema, value, path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func validateArraySchema(schema map[string]any, array []any, path string) error {
	if err := checkIntegerLimit(schema, "minItems", len(array), path, true); err != nil {
		return err
	}
	if err := checkIntegerLimit(schema, "maxItems", len(array), path, false); err != nil {
		return err
	}
	if unique, exists := schema["uniqueItems"]; exists {
		required, ok := unique.(bool)
		if !ok {
			return fmt.Errorf("%s: uniqueItems must be boolean", path)
		}
		if required {
			for i := range array {
				for j := i + 1; j < len(array); j++ {
					if reflect.DeepEqual(array[i], array[j]) {
						return fmt.Errorf("%s: array items must be unique", path)
					}
				}
			}
		}
	}
	if itemSchema, exists := schema["items"]; exists {
		for index, value := range array {
			if err := validateSchemaNode(itemSchema, value, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStringSchema(schema map[string]any, value, path string) error {
	length := utf8.RuneCountInString(value)
	if err := checkIntegerLimit(schema, "minLength", length, path, true); err != nil {
		return err
	}
	if err := checkIntegerLimit(schema, "maxLength", length, path, false); err != nil {
		return err
	}
	if raw, exists := schema["pattern"]; exists {
		pattern, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%s: pattern must be a string", path)
		}
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("%s: invalid pattern", path)
		}
		if !expression.MatchString(value) {
			return fmt.Errorf("%s: string does not match pattern", path)
		}
	}
	return nil
}

func validateNumberSchema(schema map[string]any, value json.Number, path string) error {
	number, err := value.Float64()
	if err != nil {
		return fmt.Errorf("%s: invalid number", path)
	}
	for _, constraint := range []struct {
		name             string
		inclusive, lower bool
	}{{"minimum", true, true}, {"maximum", true, false}, {"exclusiveMinimum", false, true}, {"exclusiveMaximum", false, false}} {
		raw, exists := schema[constraint.name]
		if !exists {
			continue
		}
		limitNumber, ok := raw.(json.Number)
		if !ok {
			return fmt.Errorf("%s: %s must be a number", path, constraint.name)
		}
		limit, err := limitNumber.Float64()
		if err != nil {
			return fmt.Errorf("%s: invalid %s", path, constraint.name)
		}
		valid := number > limit
		if !constraint.lower {
			valid = number < limit
		}
		if constraint.inclusive && constraint.lower {
			valid = number >= limit
		}
		if constraint.inclusive && !constraint.lower {
			valid = number <= limit
		}
		if !valid {
			return fmt.Errorf("%s: violates %s", path, constraint.name)
		}
	}
	return nil
}

func checkIntegerLimit(schema map[string]any, keyword string, actual int, path string, minimum bool) error {
	raw, exists := schema[keyword]
	if !exists {
		return nil
	}
	number, ok := raw.(json.Number)
	if !ok {
		return fmt.Errorf("%s: %s must be an integer", path, keyword)
	}
	limit, err := number.Int64()
	if err != nil || limit < 0 || limit > math.MaxInt {
		return fmt.Errorf("%s: invalid %s", path, keyword)
	}
	if (minimum && actual < int(limit)) || (!minimum && actual > int(limit)) {
		return fmt.Errorf("%s: violates %s", path, keyword)
	}
	return nil
}

func matchesJSONType(expected string, value any) bool {
	switch strings.TrimSpace(expected) {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	default:
		return false
	}
}

func supportedJSONType(value string) bool {
	switch strings.TrimSpace(value) {
	case "object", "array", "string", "boolean", "null", "number", "integer":
		return true
	default:
		return false
	}
}
