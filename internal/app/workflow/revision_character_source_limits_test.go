package workflow

import (
	"fmt"
	"strings"
	"testing"
)

func TestCharacterSourceUniqueAndRuneCountBounds(t *testing.T) {
	base := raw(`{"items":["original"]}`)
	for _, count := range []int{256, 257} {
		characters := make([]rune, count)
		for i := range characters {
			characters[i] = rune(0x4e00 + i)
		}
		input := mustJSON(map[string]any{"_userRevision": map[string]any{"changes": []string{string(characters)}}})
		source := revisionCharacterSource(input, base)
		if count == 256 && (source == nil || len(source.Characters) != 256) {
			t.Fatal("exact unique-character limit was rejected", source)
		}
		if count == 257 && source != nil {
			t.Fatal("oversized unique-character menu was offered")
		}
	}
	for _, count := range []int{20000, 20001} {
		input := mustJSON(map[string]any{"_userRevision": map[string]any{"changes": []string{strings.Repeat("界", count)}}})
		source := revisionCharacterSource(input, base)
		if count == 20000 && (source == nil || len(source.Characters) != 1) {
			t.Fatal("exact Unicode-rune limit was rejected")
		}
		if count == 20001 && source != nil {
			t.Fatal("oversized Unicode-rune source was offered")
		}
	}
}

func TestCharacterSourceEmptyASCIIMenuAndExactRuneOffsets(t *testing.T) {
	base := raw(`{"items":["original"]}`)
	for _, input := range []string{`{}`, `{"_userRevision":{"changes":[]}}`, `{"_userRevision":{"changes":["plain ASCII 123\n"]}}`, `{"_userRevision":{"changes":["　"]}}`} {
		if source := revisionCharacterSource(raw(input), base); source != nil {
			t.Fatal("empty, ASCII-only, or whitespace-only source acquired a character menu", input)
		}
	}
	input := mustJSON(map[string]any{"_userRevision": map[string]any{"changes": []string{"A😀誊"}}})
	source := revisionCharacterSource(input, base)
	if source == nil || len(source.Characters) != 2 {
		t.Fatal("graphic Unicode source was not retained")
	}
	want := map[string]int{"😀": 1, "誊": 2}
	for _, c := range source.Characters {
		offset, ok := want[c.Text]
		if !ok || c.Offset != offset || c.Pointer != "/_userRevision/changes/0" || c.ID != fmt.Sprintf("U+%04X", []rune(c.Text)[0]) {
			t.Fatal("source uses byte positions or lost its original code point", c)
		}
	}
	other := revisionCharacterSource(input, raw(`{"items":["different baseline"]}`))
	if other == nil || other.SHA256 == source.SHA256 {
		t.Fatal("character source digest was not bound to the original baseline")
	}
}
