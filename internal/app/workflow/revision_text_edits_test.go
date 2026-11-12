package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func revisionEditPayload(base json.RawMessage, edits ...map[string]any) string {
	if edits == nil {
		edits = []map[string]any{}
	}
	return string(mustJSON(map[string]any{"baseSHA256": hashJSON(base), "textEdits": edits}))
}

func TestRevisionTextEditsPreserveUnmentionedFieldsAndNumbers(t *testing.T) {
	base := raw(`{"populationAndSampling":["保留原样，腾写工作"],"n":9007199254740993,"decimal":1.2300,"deliverables":[{"title":"附录1","markdown":"改正绊倒，其余原样"},{"title":"附录2","markdown":"绊倒不改"}],"methodSummary":"完整原方法摘要"}`)
	original := cloneRaw(base)
	payload := revisionEditPayload(base, map[string]any{"path": "/populationAndSampling/0", "before": "腾写", "after": "誊写"}, map[string]any{"path": "/deliverables/0/markdown", "before": "绊倒", "after": "跌倒"})
	got, err := ApplyRevisionTextEdits(base, payload)
	want := raw(strings.Replace(strings.Replace(string(base), "腾写", "誊写", 1), "改正绊倒", "改正跌倒", 1))
	if err != nil || !equalRevisionEditBase(got, want) || !bytes.Equal(base, original) || !strings.Contains(string(got), "9007199254740993") || !strings.Contains(string(got), "1.2300") {
		t.Fatalf("edit lost untouched fields/precision or mutated base: %s %v", got, err)
	}
	reused, err := ApplyRevisionTextEdits(base, revisionEditPayload(base))
	if err != nil || !equalRevisionEditBase(reused, base) {
		t.Fatal("explicit reuse lost author metadata", string(reused), err)
	}
}

func TestRevisionTextEditsRejectAmbiguousOrUnsafeInputsAtomically(t *testing.T) {
	base := raw(`{"a":"aaa","one":"first unique","list":["second unique"],"n":2,"a/b":{"~key":"unique"}}`)
	edit := func(path, before, after string) map[string]any {
		return map[string]any{"path": path, "before": before, "after": after}
	}
	for _, tc := range []struct{ name, payload string }{
		{"wrong_hash", `{"baseSHA256":"wrong","textEdits":[]}`},
		{"missing_hash", `{"textEdits":[]}`},
		{"null", `null`},
		{"null_edits", fmt.Sprintf(`{"baseSHA256":%q,"textEdits":null}`, hashJSON(base))},
		{"mixed_full", fmt.Sprintf(`{"baseSHA256":%q,"textEdits":[],"markdown":"replacement"}`, hashJSON(base))},
		{"duplicate_keys", fmt.Sprintf(`{"baseSHA256":%q,"textEdits":[],"textEdits":[]}`, hashJSON(base))},
		{"nonunique", revisionEditPayload(base, edit("/a", "a", "b"))},
		{"overlapping", revisionEditPayload(base, edit("/a", "aa", "b"))},
		{"no_match", revisionEditPayload(base, edit("/one", "not present", "b"))},
		{"same_text", revisionEditPayload(base, edit("/one", "first", "first"))},
		{"new_field", revisionEditPayload(base, edit("/new", "x", "y"))},
		{"number", revisionEditPayload(base, edit("/n", "2", "3"))},
		{"array", revisionEditPayload(base, edit("/list", "second", "third"))},
		{"leading_index", revisionEditPayload(base, edit("/list/00", "second", "third"))},
		{"negative_index", revisionEditPayload(base, edit("/list/-1", "second", "third"))},
		{"index_overflow", revisionEditPayload(base, edit("/list/99999999999999999999999", "second", "third"))},
		{"invalid_escape", revisionEditPayload(base, edit("/a~2b", "unique", "changed"))},
		{"scalar_traversal", revisionEditPayload(base, edit("/n/x", "x", "y"))},
		{"relative", revisionEditPayload(base, edit("one", "first", "other"))},
		{"root", revisionEditPayload(base, edit("", "first", "other"))},
		{"duplicate_path", revisionEditPayload(base, edit("/one", "first", "other"), edit("/one", "unique", "value"))},
		{"partial_failure", revisionEditPayload(base, edit("/one", "first", "other"), edit("/list/0", "absent", "wrong"))},
		{"trailing", revisionEditPayload(base) + `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := cloneRaw(base)
			got, err := ApplyRevisionTextEdits(base, tc.payload)
			if err == nil || got != nil || !bytes.Equal(before, base) {
				t.Fatal("unsafe or partial edit accepted", string(got), err)
			}
		})
	}
	got, err := ApplyRevisionTextEdits(base, revisionEditPayload(base, edit("/a~1b/~0key", "unique", "精确修改")))
	if err != nil || !strings.Contains(string(got), "精确修改") {
		t.Fatal("valid escaped pointer rejected", err)
	}
}

func TestRevisionTextEditsStillRequireConfirmedMaterialScope(t *testing.T) {
	s, r, step, _, base := designIsolationFixture(t, "frozen_design_material")
	for _, tc := range []struct {
		path, before, after string
		allowed             bool
	}{
		{"/deliverables/1/markdown", "糀倒", "绊倒", true},
		{"/deliverables/0/markdown", "0–3", "00–3", false},
		{"/populationAndSampling/0", "誊写", "敲写", false},
		{"/deliverables/1/title", "附录1", "改标题", false},
	} {
		value, err := ApplyRevisionTextEdits(base, revisionEditPayload(base, map[string]any{"path": tc.path, "before": tc.before, "after": tc.after}))
		if err != nil {
			t.Fatal(err)
		}
		err = s.validateDesignRevisionWrites(t.Context(), r.detail, step, value)
		if (err == nil) != tc.allowed {
			t.Fatal("edit transport bypassed confirmed write scope", tc.path, err)
		}
	}
}

func TestRevisionTextEditsKeepCitationDomainsExact(t *testing.T) {
	base := raw(`{"a":"old [K-AAAAAAAAAAAA]","b":"unchanged [K-AAAAAAAAAAAA]"}`)
	value := RevisionEditBase{Analysis: base, SHA256: hashJSON(base), CitationAliases: map[string]string{"[K-BBBBBBBBBBBB]": "[K-AAAAAAAAAAAA]"}}
	got, err := value.Apply(revisionEditPayload(base, map[string]any{"path": "/a", "before": "old [K-BBBBBBBBBBBB]", "after": "new [K-BBBBBBBBBBBB]"}))
	if err != nil || strings.Contains(string(got), "[K-AAAAAAAAAAAA]") || strings.Count(string(got), "[K-BBBBBBBBBBBB]") != 2 {
		t.Fatal("current Chat aliases failed to match original source or reissue output", string(got), err)
	}
	for _, alias := range []string{"[K-CCCCCCCCCCCC]", "[K-DDDDDDDDDDDD]"} {
		if _, err := value.Apply(revisionEditPayload(base, map[string]any{"path": "/a", "before": "old " + alias, "after": "new " + alias})); err == nil {
			t.Fatal("unknown/previous-attempt alias was guessed")
		}
	}
	keyed := raw(`{"deliverables":[],"byCitation":{"[K-AAAAAAAAAAAA]":"old"}}`)
	value.Analysis, keyed = keyed, cloneRaw(keyed)
	got, err = value.Apply(revisionEditPayload(keyed, map[string]any{"path": "/byCitation/[K-BBBBBBBBBBBB]", "before": "old", "after": "new"}))
	if err != nil || !strings.Contains(string(got), `"[K-BBBBBBBBBBBB]":"new"`) {
		t.Fatal("current citation in a visible object-key pointer did not map exactly", string(got), err)
	}
	_, err = value.Apply(revisionEditPayload(keyed, map[string]any{"path": "/byCitation/[K-BBBBBBBBBBBB]", "before": "old", "after": "new"}, map[string]any{"path": "/byCitation/[K-AAAAAAAAAAAA]", "before": "old", "after": "other"}))
	if err == nil {
		t.Fatal("two alias spellings of one path escaped uniqueness checking")
	}
}
