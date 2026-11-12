package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestEvidenceWorkLargeProductsRemainSavedAndReadable(t *testing.T) {
	work := strings.Repeat("未知领域😀；原文没有此细节\n引号\"\\", 350)
	rows := []any{}
	for i := 0; i < 20; i++ {
		rows = append(rows, map[string]any{"documentId": fmt.Sprint(i), "finding": "bounded conclusion", "limitations": "unknown", "references": []string{}, "workProduct": work})
	}
	args := map[string]any{"_evidenceWorkPaging": evidenceWorkPagingVersion, "_selectedEvidence": map[string]any{"phase": "coverage", "analyses": rows}, "evidenceContext": "original quotations retained"}
	full, err := boundedSelectedEvidenceInput(args)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= 220*1024 || strings.Count(string(full), "workProduct") != 20 {
		t.Fatal("fixture insufficient or full work lost", len(full))
	}
	view, instruction := evidenceWorkModelProjection(full)
	if len(view) > 40*1024 || instruction == "" || !strings.Contains(string(view), "original quotations retained") {
		t.Fatal("large products were not paged", len(view))
	}
	if !rawJSONEqual(full, mustJSON(args)) {
		t.Fatal("persisted input changed")
	}
	s, f := stageMemoryService()
	f.detail.Steps[0].Input = full
	var projection struct {
		Info struct {
			Analyses []struct {
				Work struct {
					Read struct {
						Offset, EndOffset int
						SHA               string `json:"sha256"`
						Pointer           string
					} `json:"$read"`
				} `json:"workProduct"`
			} `json:"analyses"`
		} `json:"_selectedEvidence"`
	}
	if err = json.Unmarshal(view, &projection); err != nil {
		t.Fatal(err)
	}
	for _, entry := range projection.Info.Analyses {
		r := entry.Work.Read
		var recovered strings.Builder
		offset := r.Offset
		for offset < r.EndOffset {
			page, err := s.ReadResearchTask(context.Background(), "current-chat", ResearchTaskReadCommand{Section: "stage_input", Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tool.BuildModelContextSnapshot("call", "", tool.Result{Status: tool.ResultSuccess, Structured: page}), "tool result truncated") {
				t.Fatal("reader result truncated")
			}
			var p struct {
				Content, SHA256 string
				NextOffset      int
			}
			_ = json.Unmarshal(page, &p)
			if p.SHA256 != r.SHA || r.SHA != hashJSON(full) || p.NextOffset <= offset {
				t.Fatal("reader identity/progress wrong")
			}
			text := []rune(p.Content)
			recovered.WriteString(string(text[:min(len(text), r.EndOffset-offset)]))
			offset = p.NextOffset
		}
		var decoded string
		if err = json.Unmarshal([]byte(recovered.String()), &decoded); err != nil || decoded != work {
			t.Fatal("unicode/escaped work changed", err)
		}
	}
	// No capability: don't create an unreadable link or silently discard work.
	delete(args, "_evidenceWorkPaging")
	plain := mustJSON(args)
	got, inst := evidenceWorkModelProjection(plain)
	if !rawJSONEqual(got, plain) || inst != "" {
		t.Fatal("invented unauthorized reader")
	}
	if _, err = boundedSelectedEvidenceInput(args); err == nil {
		t.Fatal("unreadable oversized input accepted")
	}
}

func TestEvidenceWorkProjectionRetainsShortProductsAndOriginalPaths(t *testing.T) {
	input := raw(`{"_evidenceWorkPaging":"evidence-work-pages-v1","evidenceWork":{"documentAnalyses":[{"workProduct":"short actual appraisal"}]}}`)
	view, inst := evidenceWorkModelProjection(input)
	if !rawJSONEqual(input, view) || inst != "" {
		t.Fatal("short work paged needlessly")
	}
	source := raw(" {\n\"名\": [ \"保留\", {\"workProduct\":\"中文😀\\n\\\"尾\"} ] }")
	a, b, ok := researchJSONValueByteRange(source, []string{"名", "1", "workProduct"})
	var actual string
	err := json.Unmarshal(source[a:b], &actual)
	if !ok || err != nil || actual != "中文😀\n\"尾" {
		t.Fatal(a, b, ok, err, actual)
	}
}

func TestEvidenceWorkManyMediumProductsDoNotExhaustCoverage(t *testing.T) {
	rows := []any{}
	for i := 0; i < 100; i++ {
		rows = append(rows, map[string]any{"documentId": fmt.Sprint(i), "workProduct": strings.Repeat("a", 900)})
	}
	args := map[string]any{"_evidenceWorkPaging": evidenceWorkPagingVersion, "_selectedEvidence": map[string]any{"phase": "coverage", "analyses": rows}, "candidates": strings.Repeat("b", 150*1024)}
	full, err := boundedSelectedEvidenceInput(args)
	if err != nil {
		t.Fatal(err)
	}
	view, inst := evidenceWorkModelProjection(full)
	if len(full) < 220*1024 || len(view) > 220*1024 || inst == "" {
		t.Fatal("medium work projection not bounded", len(full), len(view))
	}
}
