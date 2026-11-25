package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func sharedInventoryPageFixture(t *testing.T) (json.RawMessage, string, json.RawMessage) {
	t.Helper()
	rows := []any{}
	for i := 0; i < 100; i++ {
		fields := map[string]any{}
		for j := 0; j < 12; j++ {
			fields[fmt.Sprintf("attribute%02d", j)] = fmt.Sprintf("记录%03d/字段%02d：中文😀、转义\"\\及未核实状态，不得当作科学结论。", i, j)
		}
		rows = append(rows, map[string]any{"id": i, "fingerprint": strings.Repeat("abcdef01", 8), "metadata": fields, "largeNumber": json.RawMessage(`9007199254740993`), "decimal": json.RawMessage(`1.2300`)})
	}
	input := mustJSON(map[string]any{"originalRequest": "保持原始要求，不自动批准", "context": map[string]any{"markdown": "Short unverified report."}, "researchSourceContext": map[string]any{"a~/b": rows, "currentReference": "[K-ORIGINAL]"}, "researchRecordAudit": strings.Repeat("existing audit 原文 ", 1000)})
	view, instruction := researchInputSharedProjection(input)
	if instruction == "" {
		t.Fatal("fixture has no shared inventory")
	}
	a, b, ok := researchJSONValueByteRange(input, []string{"researchRecordAudit"})
	if !ok {
		t.Fatal("audit source missing")
	}
	page := mustJSON(map[string]any{"$read": map[string]any{"section": "stage_input", "pointer": "/researchRecordAudit", "offset": utf8.RuneCount(input[:a]), "endOffset": utf8.RuneCount(input[:b]), "sha256": hashJSON(input)}, "characters": utf8.RuneCount(input[a:b])})
	view, ok = replaceStageValue(view, []string{"researchRecordAudit"}, page)
	if !ok {
		t.Fatal("audit page missing")
	}
	return input, "host" + instruction + "<stage_input>\n" + string(view) + "\n</stage_input>schema", page
}

func TestSharedInventoryPagingPreservesCurrentLiteralOverrides(t *testing.T) {
	input, prompt, _ := sharedInventoryPageFixture(t)
	view := projectedStageInputForTest(t, prompt)
	view, ok := replaceStageValue(view, []string{"researchSourceContext", "currentReference"}, raw(`"[K-CURRENT]"`))
	if !ok {
		t.Fatal("current literal missing")
	}
	a, b := strings.LastIndex(prompt, "<stage_input>\n")+len("<stage_input>\n"), strings.LastIndex(prompt, "\n</stage_input>")
	prompt = prompt[:a] + string(view) + prompt[b:]
	projected, changed := ProjectResearchStagePrompt(prompt, input, 8000)
	if !changed || utf8.RuneCountInString(projected) > 8000 {
		t.Fatal("current citation marker blocked valid shared paging")
	}
	literal, ok := stageValueAt(projectedStageInputForTest(t, projected), []string{"researchSourceContext", "currentReference"})
	if !ok || string(literal) != `"[K-CURRENT]"` {
		t.Fatal("projection rebuilt current fields from older frozen input", string(literal))
	}
}

func TestSharedInventoryPagingDeclinesUnsafeSharing(t *testing.T) {
	input, prompt, priorPage := sharedInventoryPageFixture(t)
	base := projectedStageInputForTest(t, prompt)
	for _, name := range []string{"unknown_index", "fractional_index", "negative_zero", "extra_reference_field", "changed_target", "alias_wrong_original", "alias_outside_scope", "existing_source_page", "target_chain", "unknown_encoding"} {
		t.Run(name, func(t *testing.T) {
			view := cloneRaw(base)
			candidate := prompt
			replace := func(path []string, value json.RawMessage) {
				var ok bool
				view, ok = replaceStageValue(view, path, value)
				if !ok {
					t.Fatal("fixture path missing", path)
				}
			}
			switch name {
			case "unknown_index":
				replace([]string{"researchSourceContext", "a~/b", "1", "fingerprint"}, raw(`{"$same":99999}`))
			case "fractional_index":
				replace([]string{"researchSourceContext", "a~/b", "1", "fingerprint"}, raw(`{"$same":0.5}`))
			case "negative_zero":
				replace([]string{"researchSourceContext", "a~/b", "1", "fingerprint"}, raw(`{"$same":-0}`))
			case "extra_reference_field":
				replace([]string{"researchSourceContext", "a~/b", "1", "fingerprint"}, raw(`{"$same":0,"extra":true}`))
			case "changed_target":
				replace([]string{"researchSourceContext", "a~/b", "0", "fingerprint"}, raw(`"changed source"`))
			case "alias_wrong_original":
				replace([]string{"researchSourceContext", "a~/b", "1", "metadata", "attribute00"}, raw(`{"$same":0}`))
			case "alias_outside_scope":
				var root map[string]json.RawMessage
				_ = json.Unmarshal(view, &root)
				root["foreign"] = raw(`{"$same":0}`)
				view = mustJSON(root)
			case "existing_source_page":
				replace([]string{"researchSourceContext", "a~/b", "1", "metadata", "attribute00"}, priorPage)
			case "target_chain":
				replace([]string{"researchSourceContext", "a~/b", "0", "fingerprint"}, raw(`{"$same":0}`))
			case "unknown_encoding":
				candidate = strings.Replace(candidate, "source-sharing-v1", "unknown-sharing-version", 1)
			}
			a, b := strings.LastIndex(candidate, "<stage_input>\n")+len("<stage_input>\n"), strings.LastIndex(candidate, "\n</stage_input>")
			candidate = candidate[:a] + string(view) + candidate[b:]
			if _, ok := unshareResearchSourceForPaging(candidate, input); ok {
				t.Fatal("unsafe view was reconstructed", name)
			}
		})
	}
}

func TestSharedPageLiteralEqualityNeverCoercesNumbersOrDuplicateKeys(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{`9007199254740993`, `9007199254740992`, false},
		{`1.2300`, `1.23`, false},
		{`{"b":9007199254740993,"a":"中😀"}`, `{"a":"中😀","b":9007199254740993}`, true},
		{`{"x":1,"x":2}`, `{"x":2}`, false},
		{`[1,2]`, `[2,1]`, false},
	} {
		if sharedPageLiteralEqual(raw(tc.a), raw(tc.b)) != tc.equal {
			t.Fatal("literal identity changed", tc)
		}
	}
}

func TestSharedInventoryCanBecomeOneOriginalPageWithoutDanglingAliases(t *testing.T) {
	input, prompt, priorPage := sharedInventoryPageFixture(t)
	original := string(input)
	view, changed := ProjectResearchStagePrompt(prompt, input, 8_000)
	if !changed || utf8.RuneCountInString(view) > 8_000 {
		t.Fatalf("shared inventory still pins its large parent: chars=%d changed=%v", utf8.RuneCountInString(view), changed)
	}
	if strings.Contains(view, `"$same"`) || strings.Contains(view, "<stage_input_shared_paths>") || strings.Contains(view, "source-sharing-v1") {
		t.Fatal("paged source left a shared alias or its obsolete contract")
	}
	projected := projectedStageInputForTest(t, view)
	audit, ok := stageValueAt(projected, []string{"researchRecordAudit"})
	if !ok || !rawJSONEqual(audit, priorPage) {
		t.Fatal("an existing unrelated page was changed")
	}
	svc, repo := stageMemoryService()
	repo.detail.Steps[0].Input = input
	decode := func(raw []byte) any {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	reads := 0
	var expand func(any) any
	expand = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$read"].(map[string]any); ok {
				parse := func(key string) int {
					n, err := ref[key].(json.Number).Int64()
					if err != nil {
						t.Fatal(err)
					}
					return int(n)
				}
				offset, end := parse("offset"), parse("endOffset")
				if ref["section"] != "stage_input" || ref["sha256"] != hashJSON(input) {
					t.Fatal("page escaped original input")
				}
				var full strings.Builder
				for offset < end {
					data, err := svc.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_input", Offset: offset})
					if err != nil {
						t.Fatal(err)
					}
					var page struct {
						Content, SHA256 string
						NextOffset      int
					}
					if err := json.Unmarshal(data, &page); err != nil || page.SHA256 != ref["sha256"] || page.NextOffset <= offset {
						t.Fatal("invalid actual reader page", err)
					}
					text := []rune(page.Content)
					full.WriteString(string(text[:min(len(text), end-offset)]))
					offset = page.NextOffset
					reads++
				}
				return decode([]byte(full.String()))
			}
			result := map[string]any{}
			for key, child := range v {
				result[key] = expand(child)
			}
			return result
		case []any:
			result := make([]any, len(v))
			for i, child := range v {
				result[i] = expand(child)
			}
			return result
		}
		return value
	}
	if restored := expand(decode(projected)); !reflect.DeepEqual(restored, decode(input)) || reads < 2 || string(input) != original {
		t.Fatal("original JSON could not be reconstructed through authorized pages")
	}
	if again, changed := ProjectResearchStagePrompt(view, input, 8_000); changed || again != view {
		t.Fatal("repeated projection grew or changed the admitted view")
	}
	t.Logf("shared source prompt=%d -> %d characters; original reader pages=%d", utf8.RuneCountInString(prompt), utf8.RuneCountInString(view), reads)
}
