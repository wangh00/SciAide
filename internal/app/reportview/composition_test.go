package reportview

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

func partsFixture() map[string]any {
	body, first, last := "  中文🧪正文\r\n\n", "## 附录甲\n记录😀", "## 附录乙\n证据"
	start := len([]rune(body))
	return map[string]any{
		"context":   map[string]any{"markdown": body, "confidence": "low", "exactNumber": json.Number("9007199254740993")},
		"unrelated": "keep this unchanged",
		"reportComposition": Composition{ViewVersion: PartsVersion, Pointer: "/context/markdown", Sections: []ManagedSection{
			{Start: start, End: start + len([]rune(first)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(first)))},
			{Start: start + len([]rune(first)) + 2, End: start + len([]rune(first)) + 2 + len([]rune(last)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(last)))},
		}},
		"reportRecordAppendices": []string{first + "\n\n", last + " \r\n"},
	}
}

func TestAssembleInputExactAndRejectsInvalidParts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"valid", func(map[string]any) {}},
		{"unknown-version", func(v map[string]any) {
			c := v["reportComposition"].(Composition)
			c.ViewVersion = "future"
			v["reportComposition"] = c
		}},
		{"wrong-pointer", func(v map[string]any) {
			c := v["reportComposition"].(Composition)
			c.Pointer = "/other"
			v["reportComposition"] = c
		}},
		{"missing-part", func(v map[string]any) { v["reportRecordAppendices"] = []string{} }},
		{"changed-part", func(v map[string]any) { v["reportRecordAppendices"].([]string)[0] = "altered" }},
		{"reordered", func(v map[string]any) { r := v["reportRecordAppendices"].([]string); r[0], r[1] = r[1], r[0] }},
		{"overlap", func(v map[string]any) { v["reportComposition"].(Composition).Sections[1].Start-- }},
		{"negative-end", func(v map[string]any) { v["reportComposition"].(Composition).Sections[0].End = -1 }},
		{"bad-hash", func(v map[string]any) { v["reportComposition"].(Composition).Sections[0].SHA256 = "wrong" }},
		{"null-body", func(v map[string]any) { v["context"].(map[string]any)["markdown"] = nil }},
		{"missing-context", func(v map[string]any) { delete(v, "context") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := partsFixture()
			tc.mutate(v)
			input, _ := json.Marshal(v)
			original := string(input)
			out, err := AssembleInput(input)
			if tc.name != "valid" {
				if err == nil {
					t.Fatal("invalid separated report accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Context struct {
					Markdown    string
					ExactNumber json.Number
				}
				Unrelated string
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if got.Context.Markdown != "  中文🧪正文\r\n\n## 附录甲\n记录😀\n\n## 附录乙\n证据 \r\n" || got.Context.ExactNumber != "9007199254740993" || got.Unrelated != "keep this unchanged" || string(input) != original {
				t.Fatal("lossy or mutating assembly")
			}
		})
	}
	for _, raw := range []string{`{"markdown":"unchanged"}`, `{"context":{"markdown":"complete"}}`} {
		out, err := AssembleInput(json.RawMessage(raw))
		if err != nil || string(out) != raw {
			t.Fatal("non-separated report rewritten", err)
		}
	}
}
