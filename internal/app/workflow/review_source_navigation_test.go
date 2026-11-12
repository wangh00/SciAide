package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestReviewNavigationCoordinatesMatchOriginalReader(t *testing.T) {
	input := raw(" \n {\"context\" : {\"a/~😀<&\" : \"é\\u4e2d\\n\\\"😀\\\\\",\"number\" : 9007199254740993, \"bool\" : false, \"nested\" : [\"a\",\"b\"]},\"originalRequest\":\"原要求\"} \n")
	c, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range decodeReviewNavigationRows(t, c.SubmissionMenu()) {
		m, err := ResolveResearchStageMaterial(input, row[0].(string))
		if err != nil {
			t.Fatal(err)
		}
		a, _ := row[4].(json.Number).Int64()
		b, _ := row[5].(json.Number).Int64()
		if int(a) != m.OriginalJSONOffset || int(b) != m.OriginalJSONEndOffset {
			t.Fatal("single-pass navigation moved an original serialized range", row[0])
		}
	}
}

func TestReviewNavigationLongPrefixesLimitsAndCancellation(t *testing.T) {
	values := map[string]any{}
	for i := 0; i < 3000; i++ {
		values[fmt.Sprintf("field%04d", i)] = "source"
	}
	input := mustJSON(map[string]any{"context": map[string]any{strings.Repeat("p", 1800) + "😀/~<&": values}})
	started := time.Now()
	c, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil {
		t.Fatal(err)
	}
	rows := decodeReviewNavigationRows(t, c.SubmissionMenu())
	if len(rows) != 3000 || len(c.SubmissionMenu()) >= 200000 {
		t.Fatal("long common prefix was expanded or sources were omitted")
	}
	for _, row := range rows {
		if utf8.RuneCountInString(row[0].(string)) > 2048 {
			t.Fatal("fixture crossed the original reader bound")
		}
	}
	t.Logf("3000 original sources with an 1800-character common prefix: %s, navigation=%d bytes", time.Since(started), len(c.SubmissionMenu()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if c, err := BuildReviewSourceCatalog(ctx, input, sourceSubmissionBinding(input)); err != context.Canceled || c != nil {
		t.Fatal("cancelled build exposed a partial catalog", err)
	}
	if raw, err := groupedReviewNavigation(ctx, [][]any{{"/context/a", 1, []int{1}, 0, 0, 1}}); err != context.Canceled || raw != nil {
		t.Fatal("cancelled grouping exposed a partial view", err)
	}
	tooWide := mustJSON(map[string]any{"context": map[string]any{strings.Repeat("p", 1900): func() map[string]any {
		m := map[string]any{}
		for i := 0; i < 9000; i++ {
			m[fmt.Sprint(i)] = "v"
		}
		return m
	}()}})
	if c, err := BuildReviewSourceCatalog(t.Context(), tooWide, sourceSubmissionBinding(tooWide)); err == nil || c != nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatal("source path resource limit did not fail closed", err)
	}
	long := mustJSON(map[string]any{"context": map[string]any{strings.Repeat("x", 2050): "source"}})
	binding := sourceSubmissionBinding(long)
	if c, err := BuildReviewSourceCatalog(t.Context(), long, binding); err == nil || c != nil || !strings.Contains(err.Error(), "2048 rune") {
		t.Fatal("unreadable v2 source was offered", err)
	}
	binding.PromptVersion = "dynamic-independent-review-v9|review-source-v1"
	legacy, err := BuildReviewSourceCatalog(t.Context(), long, binding)
	if err != nil {
		t.Fatal("legacy path was retroactively rejected", err)
	}
	row := decodeReviewNavigationRows(t, legacy.SubmissionMenu())[0]
	if row[4] != json.Number("0") || row[5] != json.Number("0") {
		t.Fatal("legacy reader-error coordinates changed")
	}
	history := mustJSON(map[string]any{"context": map[string]any{"markdown": "current source"}, "reviewHistory": map[string]any{strings.Repeat("x", 65536): "not a permitted source"}})
	for _, version := range []string{"review-source-v1", ReviewSourceProtocolVersion} {
		b := sourceSubmissionBinding(history)
		b.PromptVersion = "dynamic-independent-review-v9|" + version
		c, err := BuildReviewSourceCatalog(t.Context(), history, b)
		if err != nil || len(c.Segments) != 1 {
			t.Fatal("excluded history consumed source limits", err)
		}
	}
}

func TestCapturedReviewNavigationKeepsV1MenuByteForByte(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_CONTEXT_REPLAY")
	if dir == "" {
		t.Skip("no read-only capture supplied")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var data struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(read("captured-review-contract.json"), &data) != nil {
		t.Fatal("invalid capture")
	}
	raw := read("captured-review-binding.json")
	var binding ReviewSourceBinding
	var refs struct{ CitationRefs []tool.CitationRef }
	if json.Unmarshal(raw, &binding) != nil || json.Unmarshal(raw, &refs) != nil {
		t.Fatal("invalid binding capture")
	}
	c, err := BuildReviewSourceCatalog(t.Context(), data.Input, binding)
	if err != nil {
		t.Fatal(err)
	}
	c.CitationAliases = map[string]string{}
	for _, ref := range refs.CitationRefs {
		c.CitationAliases[ref.Reference] = citation.KnowledgeReference(binding.WorkflowRunID, ref.IndexVersionID, ref.ChunkID, ref.QuoteSHA256)
	}
	if !bytes.Equal(c.SubmissionMenu(), read("navigation-before.json")) {
		t.Fatal("frozen v1 menu changed after coordinate optimization")
	}
}

func TestReviewSourceNavigationVersionKeepsFrozenV1(t *testing.T) {
	input := raw(`{"context":{"markdown":"原文😀"}}`)
	binding := sourceSubmissionBinding(input)
	current, err := BuildReviewSourceCatalog(t.Context(), input, binding)
	if err != nil {
		t.Fatal(err)
	}
	binding.PromptVersion = "dynamic-independent-review-v9|review-source-v1"
	legacy, err := BuildReviewSourceCatalog(t.Context(), input, binding)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != "review-source-v2" || legacy.Version != "review-source-v1" || current.Segments[0].ID == legacy.Segments[0].ID {
		t.Fatal("protocol identity was inherited across versions")
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(ReviewSourceInstruction(binding.PromptVersion)))); got != "40f390731afb132b4ede8d8107924077d939eb65165e99b9b5e569844b2d2ff7" {
		t.Fatal("frozen v1 instructions changed", got)
	}
	if !strings.Contains(ReviewSourceInstruction(""), "contract (review-source-v2)") || !strings.Contains(ReviewSourceInstruction(""), "prefix-groups-v1") {
		t.Fatal("new execution has no explicit grouped navigation contract")
	}
	for _, version := range []string{binding.PromptVersion, "dynamic-independent-review-v9|review-source-v2"} {
		node := CompiledNode{PromptVersion: version}
		if !IsReviewSourceProtocol(version) || reportExecutionPromptVersion(node, "new host prompt") != version {
			t.Fatal("frozen version upgraded", version)
		}
	}
	for _, version := range []string{"", "x|review-source-v3", "x|review-source-v2|other", "X|review-source-v2"} {
		binding.PromptVersion = version
		if IsReviewSourceProtocol(version) {
			t.Fatal("unknown or data-derived protocol accepted", version)
		}
		if c, err := BuildReviewSourceCatalog(t.Context(), input, binding); err == nil || c != nil {
			t.Fatal("unbound catalog exposed source IDs", version)
		}
	}
}

// A short report can still carry a wide, deeply named provenance inventory.
// This deliberately does not use one large prose field as its size proxy.
func wideReviewNavigationInput() json.RawMessage {
	records := []any{}
	for i := 0; i < 100; i++ {
		fields := map[string]any{"peerReviewed": true, "preciseCount": json.Number("9007199254740993")}
		for j := 0; j < 22; j++ {
			fields[fmt.Sprintf("originalCatalogAttribute%02d", j)] = fmt.Sprintf("value-%d-%d", i, j)
		}
		fields["escaped/~字段😀<&"] = "原始誊写😀，不得改写。"
		records = append(records, map[string]any{"savedSourceRecordIdentity": fmt.Sprintf("record-%03d", i), "originalProviderSnapshot": fields})
	}
	return mustJSON(map[string]any{
		"originalRequest":       "Keep every source and original requirement; this is a short abstract-level map.",
		"context":               map[string]any{"markdown": "A short synthetic report; not scientific acceptance."},
		"researchSourceContext": map[string]any{"retrievedCandidateInventory": map[string]any{"savedRawSourceSnapshots": records}},
	})
}

func veryWideReviewNavigationInput() json.RawMessage {
	records := []any{}
	for i := 0; i < 360; i++ {
		fields := map[string]any{}
		for j := 0; j < 28; j++ {
			fields[fmt.Sprintf("originalCatalogField%02d", j)] = fmt.Sprintf("record-%03d-value-%02d", i, j)
		}
		records = append(records, map[string]any{"savedSourceRecordIdentity": fmt.Sprintf("record-%03d", i), "originalProviderSnapshot": fields})
	}
	return mustJSON(map[string]any{"context": map[string]any{"markdown": "短报告"}, "researchSourceContext": map[string]any{"retrievedCandidateInventory": map[string]any{"savedRawSourceSnapshots": records}}})
}

func TestVeryWideReviewNavigationUsesLosslessPointerTrie(t *testing.T) {
	input := veryWideReviewNavigationInput()
	current, err := BuildReviewSourceCatalog(t.Context(), input, sourceSubmissionBinding(input))
	if err != nil {
		t.Fatal(err)
	}
	menu := current.SubmissionMenu()
	var decoded map[string]any
	if err := json.Unmarshal(menu, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["navigationEncoding"] != "pointer-trie-v1" {
		t.Fatalf("wide menu did not switch to trie: bytes=%d encoding=%v", len(menu), decoded["navigationEncoding"])
	}
	rows := decodeReviewNavigationRows(t, menu)
	segmentIndex := 0
	for _, row := range rows {
		pointer, _ := row[0].(string)
		first := int64(row[1].(int))
		if int(first) != segmentIndex+1 {
			t.Fatal("trie row segment order changed", pointer, first, segmentIndex)
		}
		ends := row[2].([]int)
		flags, _ := row[3].(int)
		for i, end := range ends {
			segment := current.Segments[segmentIndex+i]
			if segment.Pointer != pointer || segment.End != end || (flags&1 != 0) != segment.Requirement || (flags&2 != 0) != segment.WholeScalar {
				t.Fatal("trie changed source identity or boundaries", pointer, i)
			}
		}
		segmentIndex += len(ends)
	}
	if segmentIndex != len(current.Segments) {
		t.Fatalf("trie omitted segments: got=%d want=%d", segmentIndex, len(current.Segments))
	}
	binding := sourceSubmissionBinding(input)
	binding.PromptVersion = "dynamic-independent-review-v9|review-source-v1"
	legacy, err := BuildReviewSourceCatalog(t.Context(), input, binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(menu) >= len(legacy.SubmissionMenu()) {
		t.Fatalf("trie did not reduce wide menu: current=%d legacy=%d", len(menu), len(legacy.SubmissionMenu()))
	}
}

func decodeReviewNavigationRows(t *testing.T, raw json.RawMessage) [][]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var menu map[string]any
	if err := decoder.Decode(&menu); err != nil {
		t.Fatal(err)
	}
	rows := [][]any{}
	if encoding, _ := menu["navigationEncoding"].(string); encoding != "" {
		if encoding == "pointer-trie-v1" {
			components := menu["pointerComponents"].([]any)
			nodeRows := menu["pointerNodes"].([]any)
			pointer := func(nodeID int) string {
				parts := []string{}
				for nodeID > 0 {
					node := nodeRows[nodeID-1].([]any)
					parentValue, _ := node[0].(json.Number).Int64()
					componentValue, _ := node[1].(json.Number).Int64()
					parent, component := int(parentValue), int(componentValue)
					parts = append(parts, components[component].(string))
					nodeID = parent
				}
				for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
					parts[i], parts[j] = parts[j], parts[i]
				}
				return "/" + strings.Join(parts, "/")
			}
			firstSegment := 1
			for _, rawRow := range menu["sources"].([]any) {
				row := rawRow.([]any)
				packedValue, _ := row[0].(json.Number).Int64()
				nodeID, flags := int(packedValue>>2), int(packedValue&3)
				ends := row[1].([]any)
				decodedEnds := make([]int, len(ends))
				for i, value := range ends {
					endValue, _ := value.(json.Number).Int64()
					decodedEnds[i] = int(endValue)
				}
				rows = append(rows, []any{pointer(nodeID), firstSegment, decodedEnds, flags, json.Number("0"), json.Number("0")})
				firstSegment += len(decodedEnds)
			}
		} else if encoding != "prefix-groups-v1" {
			t.Fatalf("unknown navigation encoding %q", encoding)
		} else {
			for _, rawGroup := range menu["sourceGroups"].([]any) {
				group := rawGroup.([]any)
				prefix := group[0].(string)
				for _, rawRow := range group[1].([]any) {
					row := append([]any(nil), rawRow.([]any)...)
					row[0] = prefix + row[0].(string)
					rows = append(rows, row)
				}
			}
		}
	} else {
		for _, row := range menu["sources"].([]any) {
			rows = append(rows, row.([]any))
		}
	}
	return rows
}

func TestWideReviewNavigationLosslesslySharesPointerPrefixes(t *testing.T) {
	input := wideReviewNavigationInput()
	before := append(json.RawMessage(nil), input...)
	binding := sourceSubmissionBinding(input)
	current, err := BuildReviewSourceCatalog(t.Context(), input, binding)
	if err != nil {
		t.Fatal(err)
	}
	binding.PromptVersion = "dynamic-independent-review-v9|review-source-v1"
	legacy, err := BuildReviewSourceCatalog(t.Context(), input, binding)
	if err != nil {
		t.Fatal(err)
	}
	currentMenu, legacyMenu := current.SubmissionMenu(), legacy.SubmissionMenu()
	if len(current.Segments) < 2500 || len(current.Segments) != len(legacy.Segments) {
		t.Fatal("fixture must retain thousands of distinct scalar sources")
	}
	if len(currentMenu)*100 >= len(legacyMenu)*65 {
		t.Fatalf("wide source navigation still duplicates paths: current=%d legacy=%d segments=%d", len(currentMenu), len(legacyMenu), len(current.Segments))
	}
	if !reflect.DeepEqual(decodeReviewNavigationRows(t, currentMenu), decodeReviewNavigationRows(t, legacyMenu)) {
		t.Fatal("prefix encoding changed a pointer, ordering, coordinate, flag or segment boundary")
	}
	for i, source := range current.Segments {
		old := legacy.Segments[i]
		if source.Pointer != old.Pointer || source.Text != old.Text || source.Start != old.Start || source.End != old.End || source.Requirement != old.Requirement || source.WholeScalar != old.WholeScalar {
			t.Fatal("navigation changed original source content or authority")
		}
	}
	if !bytes.Equal(input, before) || !bytes.Equal(currentMenu, current.SubmissionMenu()) || strings.Contains(string(currentMenu), "原始誊写") {
		t.Fatal("navigation mutated input, cache or duplicated source text")
	}
	t.Logf("wide input=%d legacy navigation=%d current navigation=%d scalar rows=%d segments=%d", len(input), len(legacyMenu), len(currentMenu), len(decodeReviewNavigationRows(t, currentMenu)), len(current.Segments))
}
