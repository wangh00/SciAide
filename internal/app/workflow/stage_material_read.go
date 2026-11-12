package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const StageMaterialAddressMode = "json_pointer"

// Text values use decoded UTF-8; other values keep the original JSON bytes
// (including numeric precision/whitespace). ValueSHA256 hashes those exact
// content bytes. InputSHA256 always hashes the whole original frozen input.
type ResearchStageMaterial struct {
	Pointer     string
	Content     string
	Encoding    string
	InputSHA256 string
	ValueSHA256 string
	// Legacy navigation only: these are whole-input JSON coordinates, NOT
	// offsets in Content (decoded strings have a different coordinate system).
	OriginalJSONOffset    int
	OriginalJSONEndOffset int
}

func ResolveResearchStageMaterial(input json.RawMessage, pointer string) (ResearchStageMaterial, error) {
	if !utf8.Valid(input) || !json.Valid(input) || utf8.RuneCountInString(pointer) > 2048 || pointer != "" && !strings.HasPrefix(pointer, "/") {
		return ResearchStageMaterial{}, fmt.Errorf("无效的阶段材料 JSON pointer")
	}
	path := []string{}
	if pointer != "" {
		for _, token := range strings.Split(pointer[1:], "/") {
			var decoded strings.Builder
			for i := 0; i < len(token); i++ {
				if token[i] != '~' {
					decoded.WriteByte(token[i])
					continue
				}
				if i+1 >= len(token) || token[i+1] != '0' && token[i+1] != '1' {
					return ResearchStageMaterial{}, fmt.Errorf("JSON pointer 含非法转义")
				}
				i++
				if token[i] == '0' {
					decoded.WriteByte('~')
				} else {
					decoded.WriteByte('/')
				}
			}
			path = append(path, decoded.String())
		}
	}
	raw := json.RawMessage(bytes.TrimSpace(input))
	for _, key := range path {
		d := json.NewDecoder(bytes.NewReader(raw))
		token, err := d.Token()
		if err != nil {
			return ResearchStageMaterial{}, fmt.Errorf("JSON pointer 不能穿过标量")
		}
		var selected json.RawMessage
		switch token {
		case json.Delim('{'):
			matches := 0
			for d.More() {
				name, err := d.Token()
				if err != nil {
					return ResearchStageMaterial{}, err
				}
				var value json.RawMessage
				if err := d.Decode(&value); err != nil {
					return ResearchStageMaterial{}, err
				}
				if name == key {
					selected = value
					matches++
				}
			}
			if matches != 1 {
				return ResearchStageMaterial{}, fmt.Errorf("JSON pointer 对象键不存在或重复")
			}
		case json.Delim('['):
			n, err := strconv.Atoi(key)
			if err != nil || n < 0 || strconv.Itoa(n) != key {
				return ResearchStageMaterial{}, fmt.Errorf("JSON pointer 数组下标必须是规范非负整数")
			}
			for i := 0; d.More(); i++ {
				var value json.RawMessage
				if err := d.Decode(&value); err != nil {
					return ResearchStageMaterial{}, err
				}
				if i == n {
					selected = value
				}
			}
			if selected == nil {
				return ResearchStageMaterial{}, fmt.Errorf("JSON pointer 数组下标越界")
			}
		default:
			return ResearchStageMaterial{}, fmt.Errorf("JSON pointer 不能穿过标量")
		}
		if _, err := d.Token(); err != nil {
			return ResearchStageMaterial{}, err
		}
		if _, err := d.Token(); err != io.EOF {
			return ResearchStageMaterial{}, fmt.Errorf("无效的 JSON pointer 容器")
		}
		raw = selected
	}
	content, encoding := string(raw), "json"
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &content); err != nil {
			return ResearchStageMaterial{}, err
		}
		encoding = "text"
	}
	a, b, ok := researchJSONValueByteRange(input, path)
	if !ok {
		return ResearchStageMaterial{}, fmt.Errorf("无法定位原始 JSON 材料范围")
	}
	return ResearchStageMaterial{Pointer: pointer, Content: content, Encoding: encoding, InputSHA256: hashJSON(input), ValueSHA256: hashJSON([]byte(content)), OriginalJSONOffset: utf8.RuneCount(input[:a]), OriginalJSONEndOffset: utf8.RuneCount(input[:b])}, nil
}

func ReadResearchStageMaterial(input json.RawMessage, pointer string, offset int, nodeID string) (json.RawMessage, error) {
	m, err := ResolveResearchStageMaterial(input, pointer)
	if err != nil {
		return nil, err
	}
	runes := []rune(m.Content)
	if offset < 0 || offset > len(runes) {
		return nil, fmt.Errorf("阶段材料值内分页位置越界，请核对 pointer 和 nextOffset")
	}
	end := offset + min(6000, len(runes)-offset)
	for {
		page := mustJSON(map[string]any{"section": "stage_input", "nodeId": nodeID, "addressMode": StageMaterialAddressMode, "pointer": pointer, "encoding": m.Encoding, "sha256": m.InputSHA256, "valueSha256": m.ValueSHA256, "offset": offset, "nextOffset": end, "totalCharacters": len(runes), "hasMore": end < len(runes), "content": string(runes[offset:end])})
		if utf8.RuneCount(page) < tool.MaxModelContextRunes-1000 {
			return page, nil
		}
		if end-offset <= 1 {
			return nil, fmt.Errorf("pointer 元数据超过单页上下文上限；请读取较短父级 pointer，或沿原 offset 接口定位")
		}
		end = offset + (end-offset)/2
	}
}
