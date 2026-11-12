package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const evidenceWorkPagingVersion = "evidence-work-pages-v1"

func canReadEvidenceWork(node CompiledNode) bool {
	for _, t := range node.AllowedTools {
		if t.QualifiedName == ResearchTaskReadTool {
			return true
		}
	}
	return false
}

// Model view only: the complete original JSON, hashes and validation paths
// remain saved. No new permission or reader protocol is introduced.
func evidenceWorkModelProjection(input json.RawMessage) (json.RawMessage, string) {
	root := decodeObject(input)
	if root["_evidenceWorkPaging"] != evidenceWorkPagingVersion {
		return input, ""
	}
	type work struct {
		parent map[string]any
		path   []string
		text   string
	}
	items := []work{}
	for _, channel := range [][2]string{{"_selectedEvidence", "analyses"}, {"evidenceWork", "documentAnalyses"}} {
		container, _ := root[channel[0]].(map[string]any)
		rows, _ := container[channel[1]].([]any)
		for i, row := range rows {
			doc, _ := row.(map[string]any)
			text, _ := doc["workProduct"].(string)
			if text != "" {
				items = append(items, work{doc, []string{channel[0], channel[1], strconv.Itoa(i), "workProduct"}, text})
			}
		}
	}
	total := 0
	for _, item := range items {
		total += len(item.text)
	}
	if total <= 12*1024 {
		return input, ""
	}
	changed := false
	inputHash := hashJSON(input)
	for _, item := range items {
		start, end, ok := researchJSONValueByteRange(input, item.path)
		if !ok {
			continue
		}
		// Offsets belong to the ORIGINAL saved JSON, not this projected view.
		link := map[string]any{"$read": map[string]any{"section": "stage_input", "offset": utf8.RuneCount(input[:start]), "endOffset": utf8.RuneCount(input[:end]), "pointer": "/" + strings.Join(item.path, "/"), "sha256": inputHash, "characters": utf8.RuneCountInString(item.text)}}
		if len(mustJSON(link))+64 >= len(mustJSON(item.text)) {
			continue
		}
		item.parent["workProduct"] = link
		changed = true
	}
	if !changed {
		return input, ""
	}
	projected := mustJSON(root)
	instruction := "\n<evidence_work_pages version=\"" + evidenceWorkPagingVersion + "\">\nworkProduct 中 {$read:{section,offset,endOffset,pointer,sha256,characters}} 是宿主的原文分页导航，不是缺失材料或科学判断。完整工作原文保存在当前 stage_input。仅用已授权 builtin.research.task.read(section=stage_input,offset=给定值) 读取；当前阶段可省略nodeId。按返回 nextOffset 继续至 endOffset，核对 sha256；片段是原JSON字符串（含转义），不得把页面截断当作原文缺失。围绕本轮要求核读相关评价，不机械重读已有完整材料；判某项未完成前先读对应工作。审查仍使用原 pointer 与原文quote，不引用导航/哈希作为科学证据。哈希和依据位置始终对应完整保存输入，不是投影视图。\n</evidence_work_pages>\n"
	if len(projected)+len(instruction) >= len(input) {
		return input, ""
	}
	return projected, instruction
}

// Find exact byte ranges without reserializing, including non-ASCII, escaped
// values, array separators and original whitespace. Used for existing reader
// character offsets; the source remains immutable.
func researchJSONValueByteRange(raw json.RawMessage, path []string) (int, int, bool) {
	if len(path) == 0 {
		return 0, len(raw), json.Valid(raw)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil {
		return 0, 0, false
	}
	index := 0
	for d.More() {
		key := ""
		switch token {
		case json.Delim('{'):
			k, e := d.Token()
			if e != nil {
				return 0, 0, false
			}
			key, _ = k.(string)
		case json.Delim('['):
			key = strconv.Itoa(index)
			index++
		default:
			return 0, 0, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return 0, 0, false
		}
		if key != path[0] {
			continue
		}
		end := int(d.InputOffset())
		start := end - len(value)
		a, b, ok := researchJSONValueByteRange(value, path[1:])
		return start + a, start + b, ok
	}
	return 0, 0, false
}

func boundedSelectedEvidenceInput(args map[string]any) (json.RawMessage, error) {
	full := mustJSON(args)
	// This is admission to the immutable stage snapshot, NOT to a model
	// request. The authorized stage reader and the agent's actual request
	// budget handle the complete input (quotes, analyses and revision history),
	// not just workProduct. A fixed byte cap here used to stop coverage before
	// that generic paging path could run, especially for Unicode material.
	if err := tool.ValidateWorkflowArguments(full); err != nil {
		return nil, fmt.Errorf("selected evidence exceeds persisted snapshot capacity; sources and complete analysis work remain saved: %w", err)
	}
	if args["_evidenceWorkPaging"] != evidenceWorkPagingVersion && len(full) > 220*1024 {
		return nil, fmt.Errorf("literature screening context budget exhausted; candidates and complete analysis work remain saved")
	}
	return full, nil
}
