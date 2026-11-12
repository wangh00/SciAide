package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const revisionCharacterSourceVersion = "revision-character-source-v1"

type RevisionSourceCharacter struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Pointer string `json:"pointer"`
	Offset  int    `json:"offset"`
}

// A literal copying menu, not inferred editing intent or scientific authority.
// Sources are existing visible correction strings in the frozen stage input.
type RevisionCharacterSource struct {
	Version    string                    `json:"version"`
	SHA256     string                    `json:"sha256"`
	Characters []RevisionSourceCharacter `json:"characters"`
}

func revisionCharacterSource(input, base json.RawMessage) *RevisionCharacterSource {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return nil
	}
	var value struct {
		Review struct {
			Corrections []struct {
				Text string `json:"text"`
			} `json:"corrections"`
		} `json:"_reviewCorrections"`
		User struct {
			Changes []string `json:"changes"`
		} `json:"_userRevision"`
	}
	if json.Unmarshal(input, &value) != nil {
		return nil
	}
	texts, pointers := []string{}, []string{}
	for i, correction := range value.Review.Corrections {
		texts = append(texts, correction.Text)
		pointers = append(pointers, fmt.Sprintf("/_reviewCorrections/corrections/%d/text", i))
	}
	// An explicit review handoff, even with no assigned corrections, must
	// never reactivate an older user revision as this stage's copying source.
	if _, reviewHandoff := fields["_reviewCorrections"]; !reviewHandoff {
		for i, change := range value.User.Changes {
			texts = append(texts, change)
			pointers = append(pointers, fmt.Sprintf("/_userRevision/changes/%d", i))
		}
	}
	if len(texts) > 100 {
		return nil
	}
	seen := map[rune]RevisionSourceCharacter{}
	count := 0
	for i, text := range texts {
		count += utf8.RuneCountInString(text)
		if count > 20000 {
			return nil
		}
		for offset, r := range []rune(text) {
			if r < 128 || !unicode.IsGraphic(r) || unicode.IsSpace(r) {
				continue
			}
			if _, ok := seen[r]; !ok {
				seen[r] = RevisionSourceCharacter{ID: fmt.Sprintf("U+%04X", r), Text: string(r), Pointer: pointers[i], Offset: offset}
			}
			if len(seen) > 256 {
				return nil
			} // Optional menu must not crowd out evidence.
		}
	}
	if len(seen) == 0 {
		return nil
	}
	keys := make([]rune, 0, len(seen))
	for r := range seen {
		keys = append(keys, r)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	result := &RevisionCharacterSource{Version: revisionCharacterSourceVersion}
	for _, r := range keys {
		result.Characters = append(result.Characters, seen[r])
	}
	result.SHA256 = hashJSON(mustJSON(map[string]any{"version": result.Version, "baseSHA256": hashJSON(base), "characters": result.Characters}))
	return result
}

func (base RevisionEditBase) CharacterEditSchema() json.RawMessage {
	if base.Characters == nil {
		return nil
	}
	ids := make([]string, 0, len(base.Characters.Characters))
	for _, c := range base.Characters.Characters {
		ids = append(ids, c.ID)
	}
	return mustJSON(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"baseSHA256", "sourceSHA256", "characterEdits"}, "properties": map[string]any{
		"baseSHA256":   map[string]any{"type": "string", "enum": []string{hashJSON(base.Analysis)}},
		"sourceSHA256": map[string]any{"type": "string", "enum": []string{base.Characters.SHA256}},
		"characterEdits": map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path", "before", "replacementId"}, "properties": map[string]any{
			"path":          map[string]any{"type": "string", "pattern": "^/", "minLength": 1, "maxLength": 2048},
			"before":        map[string]any{"type": "string", "minLength": 1, "maxLength": 1},
			"replacementId": map[string]any{"type": "string", "enum": ids},
		}}},
	}})
}

func (base RevisionEditBase) ApplyCharacterEdit(text string) (json.RawMessage, error) {
	if base.Characters == nil || !utf8.ValidString(text) || len(text) > 128*1024 {
		return nil, fmt.Errorf("no frozen character source or invalid character edit")
	}
	if _, err := decodeIsolationValue(json.RawMessage(text)); err != nil {
		return nil, err
	}
	if err := (tool.JSONSchemaValidator{}).Validate(base.CharacterEditSchema(), json.RawMessage(text)); err != nil {
		return nil, fmt.Errorf("invalid character selection: %w", err)
	}
	var payload struct {
		BaseSHA256 string                                         `json:"baseSHA256"`
		Edits      []struct{ Path, Before, ReplacementID string } `json:"characterEdits"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, c := range base.Characters.Characters {
		values[c.ID] = c.Text
	}
	edits := []map[string]any{}
	for _, e := range payload.Edits {
		after, ok := values[e.ReplacementID]
		if !ok || utf8.RuneCountInString(after) != 1 {
			return nil, fmt.Errorf("character source is missing or invalid")
		}
		edits = append(edits, map[string]any{"path": e.Path, "before": e.Before, "after": after})
	}
	// Reuse the original atomic merger, scope validator and citation domain.
	return base.Apply(string(mustJSON(map[string]any{"baseSHA256": payload.BaseSHA256, "textEdits": edits})))
}
