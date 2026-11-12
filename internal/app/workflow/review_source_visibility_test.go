package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInitialReviewSourcesLegacyJSONRangesPreserveEscapedUnicode(t *testing.T) {
	input := raw(`{"context":{"markdown":"prefix\n\u8a8a\ud83d\ude00不可跳过否定词suffix"}}`)
	m, err := ResolveResearchStageMaterial(input, "/context/markdown")
	if err != nil {
		t.Fatal(err)
	}
	serialized := []rune(string(input))
	a := strings.Index(string(serialized), `\u8a8a`)
	// Everything before this token is ASCII, so its byte index is a rune index.
	b := a + len(`\u8a8a\ud83d\ude00`)
	if m.Content != "prefix\n誊😀不可跳过否定词suffix" {
		t.Fatal("fixture decoding differs")
	}
	for _, tc := range []struct {
		name  string
		spans [][2]int
		want  [][2]int
	}{
		{"local_page", [][2]int{{a, b}}, [][2]int{{7, 9}}},
		{"adjacent_pages", [][2]int{{a, a + 8}, {a + 8, b}}, [][2]int{{7, 9}}},
		{"truncated_surrogate", [][2]int{{a, b - 1}}, [][2]int{{7, 8}}},
		{"hidden_middle", [][2]int{{a, a + 2}, {a + 3, b}}, [][2]int{{8, 9}}},
		{"not_seen", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReviewSourceRangesFromJSON(input, m, tc.spans)
			if string(mustJSON(got)) != string(mustJSON(tc.want)) {
				t.Fatal("wrong serialized/decoded coordinate mapping", got, tc.want)
			}
		})
	}
}

func TestInitialReviewSourcesRootJSONPageAccountsForOuterWhitespace(t *testing.T) {
	input := raw(" \n {\"context\":{\"markdown\":\"不能删除\"}}\n ")
	root, err := ResolveResearchStageMaterial(input, "")
	if err != nil {
		t.Fatal(err)
	}
	position := strings.Index(root.Content, "不能")
	offset := len([]rune(root.Content[:position]))
	span, ok := ReviewSourceJSONPageRange(input, root, offset+1, offset+2)
	if !ok {
		t.Fatal("valid root JSON page was not mapped")
	}
	leaf, err := ResolveResearchStageMaterial(input, "/context/markdown")
	if err != nil {
		t.Fatal(err)
	}
	ranges := ReviewSourceRangesFromJSON(input, leaf, [][2]int{span})
	if string(mustJSON(ranges)) != "[[1,2]]" {
		t.Fatal("unseen first rune was marked visible by root whitespace shift", ranges)
	}
	root.Content = "forged"
	if _, ok := ReviewSourceJSONPageRange(input, root, 0, 1); ok {
		t.Fatal("unmatched JSON page position was trusted")
	}
}

func TestInitialReviewSourcesSharingFollowsOnlyVisibleIdenticalSingleHop(t *testing.T) {
	text := strings.Repeat("Exact shared source 誊写. ", 80)
	input := mustJSON(map[string]any{"context": map[string]any{"markdown": text}, "researchSourceContext": map[string]any{"a": map[string]any{"body": text}, "b": map[string]any{"body": text}}})
	view, instruction := researchInputSharedProjection(input, true)
	if instruction == "" || !strings.Contains(string(view), `"$same"`) {
		t.Fatal("real sharing path was not exercised")
	}
	a := strings.Index(instruction, "<stage_input_shared_paths>\n") + len("<stage_input_shared_paths>\n")
	b := strings.Index(instruction, "\n</stage_input_shared_paths>")
	var paths []string
	_ = json.Unmarshal([]byte(instruction[a:b]), &paths)
	pointer := "/researchSourceContext/b/body"
	for _, mode := range []string{"valid", "missing_table", "changed_target", "paged_target", "chained_target"} {
		t.Run(mode, func(t *testing.T) {
			v := cloneRaw(view)
			table := append([]string(nil), paths...)
			if mode == "missing_table" {
				table = nil
			}
			if mode == "changed_target" || mode == "paged_target" || mode == "chained_target" {
				replacement := raw(`"Changed text"`)
				if mode == "paged_target" {
					replacement = raw(`{"$read":{"pointer":"/context/markdown"}}`)
				}
				if mode == "chained_target" {
					replacement = raw(`{"$same":0}`)
				}
				for _, target := range paths {
					var ok bool
					v, ok = replaceStageValue(v, strings.Split(target[1:], "/"), replacement)
					if !ok {
						t.Fatal("fixture sharing target missing")
					}
				}
			}
			got, err := ResolveReviewVisibleMaterial(input, v, table, pointer)
			if mode == "valid" {
				if err != nil || got.Encoding != "text" || got.Content != text {
					t.Fatal("actual fully visible shared original was lost", err)
				}
			} else if err == nil && got.Encoding == "text" && got.Content == text {
				t.Fatal("unread/altered/shared target proved source visible", mode)
			}
		})
	}
}
