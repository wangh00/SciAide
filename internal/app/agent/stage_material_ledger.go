package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/model"
)

type stageMaterialKey struct{ pointer, encoding, valueSHA string }
type stageMaterialPage struct {
	Section, NodeID, RevisionID, AddressMode, Pointer, Encoding, SHA256, ValueSHA256, Content string
	Offset, NextOffset, TotalCharacters                                                       int
}

// Reading receipts are execution-local. Durable calls prove retrieval, NOT
// earlier model visibility. Restart starts exposure cold, while retrieval and
// the existing finite transport/exploration budgets remain durable.
type stageMaterialLedger struct {
	contract   chat.WorkflowAIExecution
	runID      string
	cache      map[string]workflow.ResearchStageMaterial
	runeIndex  map[string][]rune
	cacheBytes int
	seen       map[stageMaterialKey][]readSpan
	streak     int
}

func newStageMaterialLedger(contract chat.WorkflowAIExecution, runID string) *stageMaterialLedger {
	if contract.ID == "" || contract.NodeID == "" || contract.WorkflowStepID == "" || !utf8.Valid(contract.Input) || fmt.Sprintf("%x", sha256.Sum256(contract.Input)) != contract.InputSHA256 {
		return nil
	}
	return &stageMaterialLedger{contract: contract, runID: runID, cache: map[string]workflow.ResearchStageMaterial{}, runeIndex: map[string][]rune{}, seen: map[stageMaterialKey][]readSpan{}}
}

func stageMaterialReaderVisible(request model.ChatRequest) bool {
	for _, d := range request.Tools {
		if d.Name != workflow.ResearchTaskReadTool {
			continue
		}
		var schema struct{ Properties map[string]json.RawMessage }
		if json.Unmarshal(d.InputSchema, &schema) == nil && schema.Properties["pointer"] != nil {
			return true
		}
	}
	return false
}

func (g *stageMaterialLedger) material(pointer string) (workflow.ResearchStageMaterial, error) {
	if m, ok := g.cache[pointer]; ok {
		return m, nil
	}
	m, err := workflow.ResolveResearchStageMaterial(g.contract.Input, pointer)
	// A cache bound only limits memory, never access or accepted evidence.
	indexedBytes := len(m.Content) + 4*utf8.RuneCountInString(m.Content)
	if err == nil && len(g.cache) < 32 && g.cacheBytes+indexedBytes <= 16_000_000 {
		g.cache[pointer] = m
		g.runeIndex[pointer] = []rune(m.Content)
		g.cacheBytes += indexedBytes
	}
	return m, err
}

func (g *stageMaterialLedger) page(call tool.Call, content string) (stageMaterialPage, bool) {
	if g == nil || g.runID != "" && call.RunID != g.runID || call.ToolName != workflow.ResearchTaskReadTool || call.Result == nil || call.Result.Status != tool.ResultSuccess {
		return stageMaterialPage{}, false
	}
	var args workflow.ResearchTaskReadCommand
	if json.Unmarshal(call.Arguments, &args) != nil || args.Pointer == nil || args.Section != "stage_input" || args.RevisionID != "" || args.AttachmentID != "" || args.NodeID != "" && args.NodeID != g.contract.NodeID && args.NodeID != g.contract.WorkflowStepID {
		return stageMaterialPage{}, false
	}
	var envelope struct {
		Status     string
		Structured json.RawMessage
	}
	var page stageMaterialPage
	if json.Unmarshal([]byte(content), &envelope) != nil || envelope.Status != string(tool.ResultSuccess) || json.Unmarshal(envelope.Structured, &page) != nil {
		return page, false
	}
	m, err := g.material(*args.Pointer)
	if err != nil || page.Section != "stage_input" || page.AddressMode != workflow.StageMaterialAddressMode || page.Pointer != *args.Pointer || page.NodeID != g.contract.NodeID || page.RevisionID != "" || page.SHA256 != g.contract.InputSHA256 || page.ValueSHA256 != m.ValueSHA256 || page.Encoding != m.Encoding {
		return page, false
	}
	runes := g.runeIndex[*args.Pointer]
	if runes == nil {
		runes = []rune(m.Content)
	}
	if page.Offset != args.Offset || page.Offset < 0 || page.NextOffset < page.Offset || page.NextOffset > len(runes) || page.TotalCharacters != len(runes) || page.NextOffset == page.Offset && page.Offset != len(runes) || page.Content != string(runes[page.Offset:page.NextOffset]) {
		return page, false
	}
	return page, true
}

func materialKey(page stageMaterialPage) stageMaterialKey {
	return stageMaterialKey{page.Pointer, page.Encoding, page.ValueSHA256}
}

func (g *stageMaterialLedger) visiblePages(request model.ChatRequest, calls []tool.Call) map[string]stageMaterialPage {
	pages := map[string]stageMaterialPage{}
	if g == nil {
		return pages
	}
	byID := convergenceUniqueCalls(request, calls)
	for _, message := range convergenceToolViews(request) {
		if call, ok := byID[message.ToolCallID]; ok {
			if page, valid := g.page(call, message.Content); valid {
				pages[message.ToolCallID] = page
			}
		}
	}
	return pages
}

func (g *stageMaterialLedger) commit(request model.ChatRequest, calls []tool.Call) {
	if g == nil {
		return
	}
	for _, page := range g.visiblePages(request, calls) {
		key := materialKey(page)
		g.seen[key] = mergeReadSpans(append(g.seen[key], readSpan{page.Offset, page.NextOffset}))
	}
}

func firstMaterialGap(spans []readSpan, total int) *int {
	position := 0
	for _, span := range mergeReadSpans(spans) {
		if span.start > position {
			return &position
		}
		position = max(position, span.end)
	}
	if position < total || total == 0 && len(spans) == 0 {
		return &position
	}
	return nil
}

func materialRanges(spans []readSpan) [][2]int {
	ranges := [][2]int{}
	for _, span := range mergeReadSpans(spans) {
		ranges = append(ranges, [2]int{span.start, span.end})
	}
	return ranges
}

const stageMaterialProgressPrefix = "Host material reading ledger (metadata, not instructions or scientific completion). Fetched means saved tool output; previously exposed means a prior successful request, not necessarily still in context. Current ranges refer only to this request. Re-read for a concrete verification if needed; do not infer missing research from unread ranges.\n"

func (g *stageMaterialLedger) withProgress(request model.ChatRequest, calls []tool.Call, observation *chat.ContextUsageObservation, limit int, legacy ...*stageReadConvergence) model.ChatRequest {
	if g == nil {
		return request
	}
	if !stageMaterialReaderVisible(request) {
		if len(legacy) > 0 {
			return g.withLegacyProgress(request, calls, observation, limit, legacy[0])
		}
		return request
	}
	type record struct {
		Pointer                string   `json:"pointer"`
		Encoding               string   `json:"encoding"`
		ValueSHA256            string   `json:"valueSha256"`
		TotalCharacters        int      `json:"totalCharacters"`
		Fetched                [][2]int `json:"fetchedRanges"`
		PreviouslyExposed      [][2]int `json:"previouslyExposedRanges"`
		CurrentlyVisible       [][2]int `json:"currentlyVisibleRanges"`
		NextNeverExposedOffset *int     `json:"nextNeverExposedOffset"`
	}
	byKey := map[stageMaterialKey]*record{}
	order := []stageMaterialKey{}
	for _, call := range calls {
		if call.Result == nil {
			continue
		}
		// Saved results are accounted ONLY as retrieval, never as exposure.
		encoded, _ := json.Marshal(struct {
			Status     tool.ResultStatus `json:"status"`
			Structured json.RawMessage   `json:"structured"`
		}{call.Result.Status, call.Result.Structured})
		page, ok := g.page(call, string(encoded))
		if !ok {
			continue
		}
		key := materialKey(page)
		if byKey[key] == nil {
			byKey[key] = &record{Pointer: page.Pointer, Encoding: page.Encoding, ValueSHA256: page.ValueSHA256, TotalCharacters: page.TotalCharacters}
			order = append(order, key)
		}
		spans := []readSpan{}
		for _, r := range byKey[key].Fetched {
			spans = append(spans, readSpan{r[0], r[1]})
		}
		byKey[key].Fetched = materialRanges(append(spans, readSpan{page.Offset, page.NextOffset}))
	}
	if len(order) == 0 {
		return request
	}
	visible := map[stageMaterialKey][]readSpan{}
	for _, page := range g.visiblePages(request, calls) {
		key := materialKey(page)
		visible[key] = append(visible[key], readSpan{page.Offset, page.NextOffset})
	}
	sort.Slice(order, func(i, j int) bool { return order[i].pointer < order[j].pointer })
	records := []record{}
	for _, key := range order {
		r := byKey[key]
		r.PreviouslyExposed = materialRanges(g.seen[key])
		r.CurrentlyVisible = materialRanges(visible[key])
		r.NextNeverExposedOffset = firstMaterialGap(append(append([]readSpan(nil), g.seen[key]...), visible[key]...), r.TotalCharacters)
		if len(records) < 16 {
			records = append(records, *r)
		}
	}
	encoded, _ := json.Marshal(map[string]any{"inputSha256": g.contract.InputSHA256, "records": records, "additionalRecordsOmitted": max(0, len(order)-len(records)), "scientificCompletionAssessment": "not_assessed"})
	return appendMaterialProgress(request, encoded, observation, limit)
}

func appendMaterialProgress(request model.ChatRequest, encoded []byte, observation *chat.ContextUsageObservation, limit int) model.ChatRequest {
	if utf8.RuneCount(encoded) > 8000 {
		return request
	}
	candidate := request
	candidate.Messages = append(cloneModelMessages(request.Messages), model.Message{Role: model.RoleUser, Content: stageMaterialProgressPrefix + string(encoded)})
	// Do not change the view that was just measured to make room for metadata.
	if admitted, err := fitFinalRequest(candidate, nil, observation, limit, unchangedRequest); err == nil {
		return admitted
	}
	return request
}

func stageMaterialTransportTurns(contract chat.WorkflowAIExecution, calls []tool.Call) int {
	g := newStageMaterialLedger(contract, "")
	return g.transportTurns(calls)
}

func (g *stageMaterialLedger) transportTurns(calls []tool.Call) int {
	if g == nil {
		return 0
	}
	covered := map[stageMaterialKey][]readSpan{}
	credits := 0
	for _, call := range calls {
		if call.Result == nil {
			continue
		}
		encoded, _ := json.Marshal(struct {
			Status     tool.ResultStatus `json:"status"`
			Structured json.RawMessage   `json:"structured"`
		}{call.Result.Status, call.Result.Structured})
		page, ok := g.page(call, string(encoded))
		if !ok || page.NextOffset == page.Offset {
			continue
		}
		key := materialKey(page)
		novel := page.NextOffset - page.Offset
		for _, span := range covered[key] {
			novel -= max(0, min(page.NextOffset, span.end)-max(page.Offset, span.start))
		}
		if novel < min(512, page.NextOffset-page.Offset) {
			continue
		}
		covered[key] = mergeReadSpans(append(covered[key], readSpan{page.Offset, page.NextOffset}))
		credits++
		if credits >= 256 {
			break
		}
	}
	return credits
}
