package workflow

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tiny shared targets can pin a large parent inventory. Expand ONLY aliases
// to their already-visible, original-bound literals before retiring the table.
// Other current-view fields (including issued citation markers) stay intact;
// never reconstruct this view from the full frozen input.
func unshareResearchSourceForPaging(prompt string, input json.RawMessage) (string, bool) {
	start, end := strings.LastIndex(prompt, "<stage_input>\n"), strings.LastIndex(prompt, "\n</stage_input>")
	if start < 0 || end < start || !utf8.Valid(input) || !json.Valid(input) || researchInputContainsSharingKey(input) {
		return "", false
	}
	prefix, view := prompt[:start], json.RawMessage(prompt[start+len("<stage_input>\n"):end])
	if !json.Valid(view) || !utf8.Valid(view) || strings.Count(prefix, researchInputSharingInstruction) != 1 {
		return "", false
	}
	targetPaths, valid := stageProjectionSharedTargets(prefix, view)
	if !valid || len(targetPaths) == 0 {
		return "", false
	}
	const open, close = "<stage_input_shared_paths>\n", "\n</stage_input_shared_paths>"
	tableStart, tableEnd := strings.Index(prefix, open), strings.Index(prefix, close)
	var pointers []string
	if json.Unmarshal([]byte(prefix[tableStart+len(open):tableEnd]), &pointers) != nil {
		return "", false
	}
	root, ok := sharedPageObject(view)
	if !ok || len(root["researchSourceContext"]) == 0 || sharedPageContainsRead(root["researchSourceContext"]) {
		return "", false
	}
	for key, value := range root {
		if key != "researchSourceContext" && researchInputContainsSharingKey(value) {
			return "", false // No other root may depend on the retired table.
		}
	}
	targets := make([]json.RawMessage, len(pointers))
	for i, pointer := range pointers {
		path := strings.Split(pointer[1:], "/")
		for j := range path {
			path[j] = strings.NewReplacer("~1", "/", "~0", "~").Replace(path[j])
		}
		original, found := stageValueAt(input, path)
		literal, visible := stageValueAt(view, path)
		if !found || !visible || sharedPageContainsRead(literal) || !sharedPageLiteralEqual(literal, original) {
			return "", false
		}
		targets[i] = original
	}
	originalSource, found := stageValueAt(input, []string{"researchSourceContext"})
	if !found {
		return "", false
	}
	expanded, aliases, valid := expandSharedPageAliases(root["researchSourceContext"], originalSource, targets)
	if !valid || aliases == 0 {
		return "", false
	}
	root["researchSourceContext"] = expanded
	encoded, err := json.Marshal(root)
	if err != nil {
		return "", false
	}
	// Exact host blocks only. All aliases are gone, not left pointing to a page.
	// The common projector can now page original-addressed parents as usual.
	prefix = strings.Replace(prefix, researchInputSharingInstruction, "", 1)
	tableStart, tableEnd = strings.Index(prefix, open), strings.Index(prefix, close)
	prefix = prefix[:tableStart] + prefix[tableEnd+len(close):]
	prefix = strings.Replace(prefix, researchInputReviewSharingNote, "", 1)
	return prefix + "<stage_input>\n" + string(encoded) + prompt[end:], true
}

func expandSharedPageAliases(raw, original json.RawMessage, targets []json.RawMessage) (json.RawMessage, int, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, 0, false
	}
	if !researchInputContainsSharingKey(raw) {
		return raw, 0, true
	}
	if raw[0] == '{' {
		fields, ok := sharedPageObject(raw)
		if !ok {
			return nil, 0, false
		}
		if index, ref := fields[researchInputSameAsKey]; ref {
			i, err := strconv.Atoi(string(index))
			if len(fields) != 1 || err != nil || i < 0 || i >= len(targets) || strconv.Itoa(i) != string(index) {
				return nil, 0, false
			}
			if !sharedPageLiteralEqual(targets[i], original) {
				return nil, 0, false
			}
			return cloneRaw(targets[i]), 1, true
		}
		expected, _ := sharedPageObject(original)
		count := 0
		for key, child := range fields {
			value, n, valid := expandSharedPageAliases(child, expected[key], targets)
			if !valid {
				return nil, 0, false
			}
			fields[key], count = value, count+n
		}
		if count == 0 {
			return raw, 0, true
		}
		return mustJSON(fields), count, true
	}
	if raw[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, 0, false
		}
		var expected []json.RawMessage
		_ = json.Unmarshal(original, &expected)
		count := 0
		for i, child := range items {
			var source json.RawMessage
			if i < len(expected) {
				source = expected[i]
			}
			value, n, valid := expandSharedPageAliases(child, source, targets)
			if !valid {
				return nil, 0, false
			}
			items[i], count = value, count+n
		}
		if count == 0 {
			return raw, 0, true
		}
		return mustJSON(items), count, true
	}
	return raw, 0, true
}

func sharedPageContainsRead(raw json.RawMessage) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := d.Token()
		if err != nil {
			return false
		}
		if token == "$read" {
			return true
		}
	}
}

// RawMessage leaves avoid float64 coercion: large integers, decimal spellings,
// JSON escapes and array order must match. Duplicate object keys are rejected.
func sharedPageLiteralEqual(view, original json.RawMessage) bool {
	view, original = bytes.TrimSpace(view), bytes.TrimSpace(original)
	if len(view) == 0 || len(original) == 0 {
		return false
	}
	if view[0] == '{' {
		fields, ok := sharedPageObject(view)
		if !ok {
			return false
		}
		if original[0] != '{' {
			return false
		}
		expected, ok := sharedPageObject(original)
		if !ok || len(fields) != len(expected) {
			return false
		}
		for key, child := range fields {
			if !sharedPageLiteralEqual(child, expected[key]) {
				return false
			}
		}
		return true
	}
	if view[0] == '[' {
		if original[0] != '[' {
			return false
		}
		var actual, expected []json.RawMessage
		if json.Unmarshal(view, &actual) != nil || json.Unmarshal(original, &expected) != nil || len(actual) != len(expected) {
			return false
		}
		for i, child := range actual {
			if !sharedPageLiteralEqual(child, expected[i]) {
				return false
			}
		}
		return true
	}
	var actual, expected bytes.Buffer
	return json.Compact(&actual, view) == nil && json.Compact(&expected, original) == nil && bytes.Equal(actual.Bytes(), expected.Bytes())
}

func sharedPageObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, exists := result[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		result[name] = value
	}
	token, err = d.Token()
	return result, err == nil && token == json.Delim('}')
}
