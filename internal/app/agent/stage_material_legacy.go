package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/model"
)

// Semantic navigation for old routes does NOT add the v9 selector. Only
// descriptors in the host-owned input view, verified against the frozen bytes,
// are mapped back to legacy global offsets. No new permission or IO is added.
func (g *stageMaterialLedger) withLegacyProgress(request model.ChatRequest, calls []tool.Call, observation *chat.ContextUsageObservation, limit int, raw *stageReadConvergence) model.ChatRequest {
	if raw == nil {
		return request
	}
	type target struct {
		Pointer string `json:"pointer"`
		Start   int    `json:"startOffset"`
		End     int    `json:"endOffset"`
	}
	targets := map[string]target{}
	discoveryTruncated := false
	var walk func(json.RawMessage, int)
	walk = func(data json.RawMessage, depth int) {
		if depth > 64 || len(targets) >= 64 {
			discoveryTruncated = true
			return
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(data, &object) == nil && object != nil {
			if link := object["$read"]; link != nil {
				var d struct {
					Section, NodeID, SHA256 string
					Pointer                 *string
					Offset, EndOffset       int
				}
				if json.Unmarshal(link, &d) == nil && d.Pointer != nil && d.Section == "stage_input" && d.SHA256 == g.contract.InputSHA256 && (d.NodeID == "" || d.NodeID == g.contract.NodeID) {
					if m, err := g.material(*d.Pointer); err == nil && m.OriginalJSONOffset == d.Offset && m.OriginalJSONEndOffset == d.EndOffset {
						targets[*d.Pointer] = target{*d.Pointer, d.Offset, d.EndOffset}
					}
				}
			}
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(object[key], depth+1)
			}
			return
		}
		var array []json.RawMessage
		if json.Unmarshal(data, &array) == nil {
			for _, value := range array {
				walk(value, depth+1)
			}
		}
	}
	for _, m := range request.Messages {
		if !m.HostToolReferences || m.Role != model.RoleUser || !strings.Contains(m.Content, `"$read"`) {
			continue
		}
		start, end := strings.LastIndex(m.Content, "<stage_input>\n"), strings.LastIndex(m.Content, "\n</stage_input>")
		if start >= 0 && end > start {
			walk(json.RawMessage(m.Content[start+len("<stage_input>\n"):end]), 0)
		}
	}
	if len(targets) == 0 {
		return request
	}
	visible := []readSpan{}
	for _, span := range raw.visiblePages(request, calls) {
		visible = append(visible, span)
	}
	clip := func(spans []readSpan, t target) []readSpan {
		out := []readSpan{}
		for _, s := range spans {
			a, b := max(t.Start, s.start), min(t.End, s.end)
			if a < b {
				out = append(out, readSpan{a - t.Start, b - t.Start})
			}
		}
		return out
	}
	pointers := make([]string, 0, len(targets))
	for p := range targets {
		pointers = append(pointers, p)
	}
	sort.Strings(pointers)
	records := []map[string]any{}
	for _, pointer := range pointers {
		if len(records) >= 16 {
			break
		}
		t := targets[pointer]
		past, current := clip(raw.seen, t), clip(visible, t)
		gap := firstMaterialGap(append(append([]readSpan(nil), past...), current...), t.End-t.Start)
		if gap != nil {
			*gap += t.Start
		}
		globalRanges := func(spans []readSpan) [][2]int {
			out := materialRanges(spans)
			for i := range out {
				out[i][0] += t.Start
				out[i][1] += t.Start
			}
			return out
		}
		records = append(records, map[string]any{"pointer": pointer, "startOffset": t.Start, "endOffset": t.End, "previouslyExposedRanges": globalRanges(past), "currentlyVisibleRanges": globalRanges(current), "nextGlobalOffset": gap})
	}
	encoded, _ := json.Marshal(map[string]any{"addressMode": "legacy_whole_json", "coordinateSystem": "original frozen JSON Unicode offsets; send ONLY section=stage_input and the global offset, never pointer on this frozen reader", "inputSha256": g.contract.InputSHA256, "records": records, "additionalVerifiedRecordsOmitted": len(targets) - len(records), "discoveryTruncated": discoveryTruncated, "scientificCompletionAssessment": "not_assessed"})
	return appendMaterialProgress(request, encoded, observation, limit)
}
