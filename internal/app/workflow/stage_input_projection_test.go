package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStageInputProjectionKeepsOriginalJSONAddressAndRequirements(t *testing.T) {
	rawValue := strings.Repeat("中文😀\n转义\"\\ /~ ", 2000)
	input := mustJSON(map[string]any{"researchContract": map[string]any{"successCriteria": []string{"original condition"}}, "originalRequest": "human goal", "evidenceWork": map[string]any{"documentAnalyses": []any{map[string]any{"documentId": "d", "workProduct": rawValue}}}, "researchRecordAudit": map[string]any{"status": "available", "queries": []any{map[string]any{"sourceRecordCount": 80, "candidateCount": 61, "groups": strings.Repeat("mapping", 6000)}}}})
	original := string(input)
	prompt := "host\n<stage_input>\n" + original + "\n</stage_input>schema"
	view, changed := ProjectResearchStagePrompt(prompt, input, 12_000)
	if !changed || utf8.RuneCountInString(view) > 12_000 || !strings.Contains(view, "original condition") || !strings.Contains(view, "human goal") {
		t.Fatal("not bounded or changed requirements", utf8.RuneCountInString(view))
	}
	start := strings.LastIndex(view, "<stage_input>\n") + len("<stage_input>\n")
	end := strings.LastIndex(view, "\n</stage_input>")
	var root any
	if json.Unmarshal([]byte(view[start:end]), &root) != nil {
		t.Fatal("invalid projection")
	}
	svc, repo := stageMemoryService()
	repo.detail.Steps[0].Input = input
	links := 0
	var check func(any)
	check = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if value, ok := x["$read"].(map[string]any); ok {
				links++
				offset, end := int(value["offset"].(float64)), int(value["endOffset"].(float64))
				start := offset
				var all strings.Builder
				for offset < end {
					b, err := svc.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_input", Offset: offset})
					if err != nil {
						t.Fatal(err)
					}
					var page struct {
						Content, SHA256 string
						NextOffset      int
					}
					_ = json.Unmarshal(b, &page)
					if page.SHA256 != value["sha256"] || page.NextOffset <= offset {
						t.Fatal("bad page")
					}
					r := []rune(page.Content)
					all.WriteString(string(r[:min(len(r), end-offset)]))
					offset = page.NextOffset
				}
				if all.String() != string([]rune(original)[start:end]) || !json.Valid([]byte(all.String())) {
					t.Fatal("value not fully recoverable")
				}
				return
			}
			for _, v := range x {
				check(v)
			}
		case []any:
			for _, v := range x {
				check(v)
			}
		}
	}
	check(root)
	if links == 0 || string(input) != original {
		t.Fatal("source modified or missing links")
	}
	again, _ := ProjectResearchStagePrompt(view, input, 8000)
	if strings.Count(again, "<stage_input_pages version=") != 1 {
		t.Fatal("encoding grew on retry")
	}
	if got, ok := ProjectResearchStagePrompt(prompt, input, len([]rune(prompt))+1); ok || got != prompt {
		t.Fatal("short prompt changed")
	}
}

func TestStageInputProjectionDoesNotHideOversizedRequirements(t *testing.T) {
	input := mustJSON(map[string]any{"researchContract": strings.Repeat("keep exact requirements", 1000)})
	prompt := "<stage_input>\n" + string(input) + "\n</stage_input>"
	if got, changed := ProjectResearchStagePrompt(prompt, input, 4000); changed || got != prompt {
		t.Fatal("requirements silently paged")
	}
}

func TestReportOnlyProjectionKeepsFrozenBibliographyVisible(t *testing.T) {
	rows := make([]map[string]any, 0, 24)
	for i := 0; i < 24; i++ {
		rows = append(rows, map[string]any{"id": fmt.Sprintf("candidate-%02d", i), "title": strings.Repeat("candidate title ", 18), "authors": []string{"Author"}, "venue": "Journal", "year": 2024, "doi": fmt.Sprintf("10.1/%02d", i), "sourceIds": []string{"openalex"}})
	}
	input := mustJSON(map[string]any{
		"originalRequest":       "keep the frozen scope",
		"researchContract":      map[string]any{"successCriteria": []string{"candidate inventory"}},
		"researchSourceContext": map[string]any{"bibliography": rows, "indexedReadRanges": make([]any, 16), "retrievedEvidence": strings.Repeat("evidence ", 6000)},
		"_userRevision":         map[string]any{"reuseContext": map[string]any{"kind": "report_only", "revisionId": "r"}, "changes": []string{"repair report"}},
	})
	prompt := "host\n<stage_input>\n" + string(input) + "\n</stage_input>"
	view, changed := ProjectResearchStagePrompt(prompt, input, 16_000)
	if !changed {
		t.Fatal("report-only fixture was not projected")
	}
	start := strings.LastIndex(view, "<stage_input>\n") + len("<stage_input>\n")
	end := strings.LastIndex(view, "\n</stage_input>")
	var projected map[string]any
	if err := json.Unmarshal([]byte(view[start:end]), &projected); err != nil {
		t.Fatal(err)
	}
	source, ok := projected["researchSourceContext"].(map[string]any)
	if !ok {
		t.Fatal("missing source context")
	}
	if _, ok := source["bibliography"].([]any); !ok {
		t.Fatal("report-only projection paged the candidate bibliography", string(view))
	}
}

func TestFinalStorageProjectionDoesNotBroadenReaderPermission(t *testing.T) {
	input := mustJSON(map[string]any{"researchSourceContext": map[string]any{"bibliography": strings.Repeat("record😀", 42000)}, "originalRequest": "unchanged"})
	plain := CompiledNode{ID: "custom", Kind: NodeAgentStage, Prompt: "only this stage", OutputSchema: raw(`{"type":"object"}`)}
	step := Step{Input: input, InputSHA256: hashJSON(input)}
	denied := buildAIStagePrompt(RunDetail{}, step, plain)
	if strings.Contains(denied, stageInputPagesInstruction) {
		t.Fatal("invented reader capability")
	}
	plain.AllowedTools = []ToolSnapshot{{QualifiedName: ResearchTaskReadTool}}
	prompt := buildAIStagePrompt(RunDetail{}, step, plain)
	if !strings.Contains(prompt, stageInputPagesInstruction) || len([]rune(prompt)) > 240000 || !strings.Contains(prompt, "unchanged") {
		t.Fatal("storage projection missed complete assembled input")
	}
}

func TestStageInputProjectionComposesWithExistingWorkPages(t *testing.T) {
	input := mustJSON(map[string]any{
		"originalRequest":     "keep the frozen scope",
		"_evidenceWorkPaging": evidenceWorkPagingVersion,
		"evidenceWork": map[string]any{"documentAnalyses": []any{map[string]any{
			"documentId": "doc", "workProduct": strings.Repeat("原文😀\\\"\n", 4000),
		}}},
		"context": map[string]any{"markdown": strings.Repeat("current report ", 3000)},
	})
	original := string(input)
	initial, instruction := evidenceWorkModelProjection(input)
	if instruction == "" {
		t.Fatal("fixture did not create work pages")
	}
	prompt := "host" + instruction + "<stage_input>\n" + string(initial) + "\n</stage_input>schema"
	view, changed := ProjectResearchStagePrompt(prompt, input, 12_000)
	if !changed || len(view) >= len(prompt) || utf8.RuneCountInString(view) > 12_000 {
		t.Fatal("composed view did not shrink within the fixture budget")
	}
	path := []string{"evidenceWork", "documentAnalyses", "0", "workProduct"}
	want, ok := stageValueAt(initial, path)
	if !ok {
		t.Fatal("missing initial page")
	}
	for _, budget := range []int{12_000, 6_000, 4_000} {
		view, _ = ProjectResearchStagePrompt(view, input, budget)
		got, ok := stageValueAt(projectedStageInputForTest(t, view), path)
		if !ok || !rawJSONEqual(got, want) || !strings.Contains(view, instruction) {
			t.Fatal("later budget projection replaced an existing work page or its contract")
		}
		if strings.Count(view, "<stage_input_pages version=") != 1 {
			t.Fatal("projection instructions accumulated")
		}
	}
	if string(input) != original {
		t.Fatal("composition changed the frozen source")
	}
	t.Logf("work-page composition: prompt=%d view=%d characters; original page unchanged", utf8.RuneCountInString(prompt), utf8.RuneCountInString(view))
}

func TestStageInputProjectionPreservesSharedLiteralTargets(t *testing.T) {
	queries := []any{strings.Repeat("shared literal source😀 ", 600), "scientific status unknown"}
	documents := []any{}
	for i := 0; i < 12; i++ {
		documents = append(documents, map[string]any{"id": i, "queries": queries})
	}
	input := mustJSON(map[string]any{
		"originalRequest":       "unchanged",
		"researchSourceContext": map[string]any{"a~/b": queries, "documents": documents},
		"context":               map[string]any{"markdown": strings.Repeat("current report ", 3000)},
	})
	original := string(input)
	initial, instruction := researchInputSharedProjection(input)
	if instruction == "" || !strings.Contains(string(initial), `"$same"`) {
		t.Fatal("fixture did not share literal targets")
	}
	prompt := "host" + instruction + "<stage_input>\n" + string(initial) + "\n</stage_input>schema"
	view, changed := ProjectResearchStagePrompt(prompt, input, 24_000)
	if !changed || len(view) >= len(prompt) || utf8.RuneCountInString(view) > 24_000 {
		t.Fatalf("shared view did not fit without re-expansion: changed=%v bytes=%d/%d characters=%d", changed, len(view), len(prompt), utf8.RuneCountInString(view))
	}
	if !strings.Contains(view, instruction) {
		t.Fatal("shared-reference interpretation was removed")
	}
	want, _ := stageValueAt(initial, []string{"researchSourceContext"})
	got, ok := stageValueAt(projectedStageInputForTest(t, view), []string{"researchSourceContext"})
	if !ok || !rawJSONEqual(want, got) {
		t.Fatal("projection paged a shared target or replaced an existing alias")
	}
	// The existing independent decoder rejects nonliteral targets and chains.
	expandSharingForTest(t, projectedStageInputForTest(t, view), instruction)
	if smaller, changed := ProjectResearchStagePrompt(view, input, 4_000); changed || smaller != view {
		t.Fatal("an impossible budget changed shared targets or existing pages")
	}
	if string(input) != original {
		t.Fatal("shared composition changed original data")
	}
	t.Logf("shared composition: prompt=%d view=%d characters; undersized budget kept the view unchanged", utf8.RuneCountInString(prompt), utf8.RuneCountInString(view))
}

func TestStageInputProjectionDeclinesUnusableSharedPaths(t *testing.T) {
	input := mustJSON(map[string]any{"context": strings.Repeat("literal ", 4000)})
	for _, table := range []string{`not JSON`, `{}`, `null`, `["invalid"]`, `["/missing"]`, `["/context~2"]`, "[\"/context\"]\n<stage_input_shared_paths>\n[]"} {
		prompt := "host\n<stage_input_shared_paths>\n" + table + "\n</stage_input_shared_paths>\n<stage_input>\n" + string(input) + "\n</stage_input>"
		if got, changed := ProjectResearchStagePrompt(prompt, input, 5_000); changed || got != prompt {
			t.Fatal("unusable shared-reference table was silently discarded", table)
		}
	}
}

func projectedStageInputForTest(t *testing.T, prompt string) json.RawMessage {
	t.Helper()
	start := strings.LastIndex(prompt, "<stage_input>\n")
	end := strings.LastIndex(prompt, "\n</stage_input>")
	if start < 0 || end <= start {
		t.Fatal("missing projected input")
	}
	value := json.RawMessage(prompt[start+len("<stage_input>\n") : end])
	if !json.Valid(value) {
		t.Fatal("invalid projected input")
	}
	return value
}
