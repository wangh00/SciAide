package workflow

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSON pointer pages use trimmed value-relative coordinates. Verify the raw
// position before mapping, including historical root JSON with outer whitespace.
func ReviewSourceJSONPageRange(input json.RawMessage, m ResearchStageMaterial, offset, endOffset int) ([2]int, bool) {
	runes := []rune(string(input))
	a, b := m.OriginalJSONOffset, m.OriginalJSONEndOffset
	if m.Encoding != "json" || a < 0 || b > len(runes) || a > b {
		return [2]int{}, false
	}
	if string(runes[a:b]) != m.Content {
		if m.Pointer != "" {
			return [2]int{}, false
		}
		for a < b && strings.ContainsRune(" \r\n\t", runes[a]) {
			a++
		}
		for b > a && strings.ContainsRune(" \r\n\t", runes[b-1]) {
			b--
		}
		if string(runes[a:b]) != m.Content {
			return [2]int{}, false
		}
	}
	if offset < 0 || endOffset <= offset || endOffset > b-a {
		return [2]int{}, false
	}
	return [2]int{a + offset, a + endOffset}, true
}

// ResolveReviewVisibleMaterial follows only the existing single-hop, exact
// source-sharing view. It never restores unread data from the frozen input.
// Caller supplies the path table actually present in its host-authored view.
func ResolveReviewVisibleMaterial(input, view json.RawMessage, paths []string, pointer string) (ResearchStageMaterial, error) {
	direct, err := ResolveResearchStageMaterial(view, pointer)
	if len(paths) == 0 || !strings.HasPrefix(pointer, "/researchSourceContext/") || researchInputContainsSharingKey(input) {
		return direct, err
	}
	parts := strings.Split(pointer, "/")
	for n := len(parts); n >= 2; n-- {
		prefix := strings.Join(parts[:n], "/")
		ref, e := ResolveResearchStageMaterial(view, prefix)
		if e != nil || ref.Encoding != "json" {
			continue
		}
		var object map[string]json.RawMessage
		var index int
		if json.Unmarshal([]byte(ref.Content), &object) != nil || len(object) != 1 || json.Unmarshal(object[researchInputSameAsKey], &index) != nil || index < 0 || index >= len(paths) {
			continue
		}
		target := paths[index]
		original, e1 := ResolveResearchStageMaterial(input, prefix)
		literal, e2 := ResolveResearchStageMaterial(view, target)
		if e1 != nil || e2 != nil || original.Encoding != literal.Encoding {
			continue
		}
		equal := original.Content == literal.Content
		if original.Encoding == "json" {
			equal = equalRevisionEditBase(json.RawMessage(original.Content), json.RawMessage(literal.Content))
		}
		if !equal {
			continue
		}
		suffix := ""
		if n < len(parts) {
			suffix = "/" + strings.Join(parts[n:], "/")
		}
		return ResolveResearchStageMaterial(view, target+suffix)
	}
	return direct, err
}

// ReviewSourceRangesFromJSON maps only fully visible serialized JSON tokens
// back to decoded scalar runes. It handles escapes and surrogate pairs without
// guessing missing bytes; adjacent visible pages may jointly cover a token.
func ReviewSourceRangesFromJSON(input json.RawMessage, m ResearchStageMaterial, visible [][2]int) [][2]int {
	covered := func(a, b int) bool {
		position := a
		for _, r := range visible {
			if r[0] <= position && r[1] > position {
				position = r[1]
			}
		}
		return position >= b
	}
	if len(visible) == 0 {
		return nil
	}
	if m.Encoding != "text" {
		if covered(m.OriginalJSONOffset, m.OriginalJSONEndOffset) {
			return [][2]int{{0, utf8.RuneCountInString(m.Content)}}
		}
		return nil
	}
	inputRunes := []rune(string(input))
	a, b := m.OriginalJSONOffset, m.OriginalJSONEndOffset
	if a < 0 || b > len(inputRunes) || b-a < 2 || inputRunes[a] != '"' || inputRunes[b-1] != '"' {
		return nil
	}
	value := inputRunes[a+1 : b-1]
	index := 0
	ranges := [][2]int{}
	for pos := 0; pos < len(value); {
		end := pos + 1
		if value[pos] == '\\' {
			if end >= len(value) {
				return nil
			}
			end++
			if value[pos+1] == 'u' {
				end = pos + 6
				if end > len(value) {
					return nil
				}
				first, e := strconv.ParseUint(string(value[pos+2:end]), 16, 16)
				if e != nil {
					return nil
				}
				if first >= 0xd800 && first <= 0xdbff && pos+12 <= len(value) && value[pos+6] == '\\' && value[pos+7] == 'u' {
					second, e := strconv.ParseUint(string(value[pos+8:pos+12]), 16, 16)
					if e == nil && second >= 0xdc00 && second <= 0xdfff {
						end = pos + 12
					}
				}
			}
		}
		if covered(a+1+pos, a+1+end) {
			if len(ranges) > 0 && ranges[len(ranges)-1][1] == index {
				ranges[len(ranges)-1][1] = index + 1
			} else {
				ranges = append(ranges, [2]int{index, index + 1})
			}
		}
		pos = end
		index++
	}
	if index != utf8.RuneCountInString(m.Content) {
		return nil
	}
	return ranges
}
