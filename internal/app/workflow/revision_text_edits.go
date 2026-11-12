package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// RevisionTextEditSchema describes an explicit initial submission, not an
// automatic repair. An empty list explicitly reuses the entire original draft.
func RevisionTextEditSchema(base json.RawMessage) json.RawMessage {
	return mustJSON(map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"baseSHA256", "textEdits"},
		"properties": map[string]any{
			"baseSHA256": map[string]any{"type": "string", "enum": []string{hashJSON(base)}},
			"textEdits": map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"path", "before", "after"},
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "pattern": "^/", "minLength": 1, "maxLength": 2048},
					"before": map[string]any{"type": "string", "minLength": 1, "maxLength": 20000},
					"after":  map[string]any{"type": "string", "maxLength": 20000},
				},
			}},
		},
	})
}

// ApplyRevisionTextEdits never infers edits from prose or a rejected candidate.
// It changes only uniquely located literal text in existing string leaves.
// The caller MUST validate the merged full result against the current frozen
// schema, scientific rules and confirmed write scope before accepting it.
func ApplyRevisionTextEdits(base json.RawMessage, text string) (json.RawMessage, error) {
	return applyRevisionTextEdits(base, text, nil)
}

// Apply keeps the immutable base/hash in Workflow coordinates, translates only
// explicitly edited excerpts through the verified current seed aliases, and
// returns a Chat-domain candidate. The ordinary live validator canonicalizes
// it back before scope/scientific checks; the Chat audit can still resolve its
// own citations. Unknown and previous-attempt aliases are never guessed.
func (base RevisionEditBase) Apply(text string) (json.RawMessage, error) {
	value, err := applyRevisionTextEdits(base.Analysis, text, base.CitationAliases)
	if err != nil || len(base.CitationAliases) == 0 {
		return value, err
	}
	return json.RawMessage(revisionEditAliasReplacer(base.CitationAliases, true).Replace(string(value))), nil
}

func revisionEditAliasReplacer(aliases map[string]string, reverse bool) *strings.Replacer {
	keys := make([]string, 0, len(aliases))
	for key := range aliases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		if reverse {
			pairs = append(pairs, aliases[key], key)
		} else {
			pairs = append(pairs, key, aliases[key])
		}
	}
	return strings.NewReplacer(pairs...)
}

func applyRevisionTextEdits(base json.RawMessage, text string, aliases map[string]string) (json.RawMessage, error) {
	if !utf8.ValidString(text) || len(text) > 512*1024 {
		return nil, fmt.Errorf("revision edit exceeds the UTF-8 submission limit")
	}
	fields, err := decodeIsolationValue(json.RawMessage(text))
	if err != nil {
		return nil, err
	}
	if err := (tool.JSONSchemaValidator{}).Validate(RevisionTextEditSchema(base), json.RawMessage(text)); err != nil {
		return nil, fmt.Errorf("invalid revision edit: %w", err)
	}
	value, err := decodeIsolationValue(base)
	if err != nil {
		return nil, fmt.Errorf("invalid revision edit baseline: %w", err)
	}
	seen := map[string]bool{}
	restore := revisionEditAliasReplacer(aliases, false)
	for _, item := range fields["textEdits"].([]any) {
		edit := item.(map[string]any)
		path, before, after := edit["path"].(string), edit["before"].(string), edit["after"].(string)
		path, before, after = restore.Replace(path), restore.Replace(before), restore.Replace(after)
		if seen[path] || before == after {
			return nil, fmt.Errorf("revision edit paths must be unique and each edit must change text: %s", path)
		}
		seen[path] = true
		parent, key, err := revisionEditLeaf(value, path)
		if err != nil {
			return nil, err
		}
		var original any
		switch container := parent.(type) {
		case map[string]any:
			original = container[key]
		case []any:
			i, _ := strconv.Atoi(key)
			original = container[i]
		}
		current, ok := original.(string)
		start := strings.Index(current, before)
		if !ok || start < 0 || strings.Contains(current[start+1:], before) {
			return nil, fmt.Errorf("revision edit before must match exactly once in the original string at %s; use a longer unique excerpt or submit a full result", path)
		}
		replacement := current[:start] + after + current[start+len(before):]
		switch container := parent.(type) {
		case map[string]any:
			container[key] = replacement
		case []any:
			i, _ := strconv.Atoi(key)
			container[i] = replacement
		}
	}
	return json.Marshal(value)
}

func revisionEditLeaf(value map[string]any, pointer string) (any, string, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, "", fmt.Errorf("revision edit requires an absolute JSON pointer")
	}
	parts := strings.Split(pointer[1:], "/")
	var current any = value
	for i, token := range parts {
		for pos := 0; pos < len(token); pos++ {
			if token[pos] == '~' {
				if pos+1 >= len(token) || token[pos+1] != '0' && token[pos+1] != '1' {
					return nil, "", fmt.Errorf("invalid revision edit JSON pointer escape")
				}
				pos++
			}
		}
		key := strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		var next any
		switch container := current.(type) {
		case map[string]any:
			var exists bool
			next, exists = container[key]
			if !exists {
				return nil, "", fmt.Errorf("revision edit cannot create a field: %s", pointer)
			}
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || strconv.Itoa(index) != key || index >= len(container) {
				return nil, "", fmt.Errorf("invalid revision edit array index: %s", pointer)
			}
			next = container[index]
		default:
			return nil, "", fmt.Errorf("revision edit pointer crosses a scalar: %s", pointer)
		}
		if i == len(parts)-1 {
			return current, key, nil
		}
		current = next
	}
	return nil, "", fmt.Errorf("revision edit requires a string leaf")
}
