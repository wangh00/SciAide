package workflow

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

const researchInputSharingVersion = "source-sharing-v1"
const researchInputSameAsKey = "$same"

// Wire encoding only. Frozen stage input, SHA, readers and scientific checks
// still use the original JSON. Identical source metadata is supplied once,
// never summarized, looked up by a filename, or discarded from storage.
const researchInputSharingInstruction = `
<stage_input_encoding version="source-sharing-v1">
Only researchSourceContext may use {"$same":N} for an EXACTLY IDENTICAL repeated value. N is a zero-based index in <stage_input_shared_paths>; that RFC 6901 path points to the complete literal value in this same stage_input, never another reference. No fact, order, scope or number changes. Report, evidenceContext, requirements and review history are not shared. For review basis use the ORIGINAL value and ORIGINAL path (including through a shared value), never this navigation as evidence. reviewedInputSha256 and task reader hashes/offsets still refer to the original frozen JSON. No extra read is needed for a value already present here.
</stage_input_encoding>
`

func researchInputSharedProjection(input json.RawMessage, review ...bool) (json.RawMessage, string) {
	projected, instruction := researchInputSharedProjectionMode(input, len(review) > 0 && review[0])
	if len(review) > 0 && review[0] {
		baseline, baselineInstruction := researchInputSharedProjectionMode(input, false)
		if len([]rune(string(baseline)+baselineInstruction)) <= len([]rune(string(projected)+instruction)) {
			return baseline, baselineInstruction
		}
	}
	return projected, instruction
}

func researchInputSharedProjectionMode(input json.RawMessage, review bool) (json.RawMessage, string) {
	var root map[string]json.RawMessage
	if json.Unmarshal(input, &root) != nil || root["researchSourceContext"] == nil || researchInputContainsSharingKey(input) {
		return input, ""
	}
	seen := map[string]string{}
	sharedPaths := []string{}
	sharedIDs := map[string]int{}
	// Count exact occurrences to amortize the one-time pointer-table cost.
	// Repeated short IDs can be profitable across many documents, but a pair
	// of small values with a long pointer usually is not. No semantic matching.
	occurrences := map[string]int{}
	var count func(json.RawMessage)
	count = func(raw json.RawMessage) {
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			return
		}
		value := compact.Bytes()
		if len(value) == 0 {
			return
		}
		if (value[0] == '{' || value[0] == '[' || value[0] == '"') && len([]rune(string(value))) >= 32 {
			occurrences[string(value)]++
		}
		switch value[0] {
		case '{':
			var fields map[string]json.RawMessage
			if json.Unmarshal(value, &fields) == nil {
				for _, child := range fields {
					count(child)
				}
			}
		case '[':
			var entries []json.RawMessage
			if json.Unmarshal(value, &entries) == nil {
				for _, child := range entries {
					count(child)
				}
			}
		}
	}
	count(root["researchSourceContext"])
	// Reviews already carry exact selected evidence separately. Reuse those
	// literal values too; never summarize or omit a unique source/history.
	if review {
		var seed func(json.RawMessage, string)
		seed = func(raw json.RawMessage, path string) {
			var b bytes.Buffer
			if json.Compact(&b, raw) != nil {
				return
			}
			v := b.Bytes()
			if len(v) == 0 {
				return
			}
			key := string(v)
			if occurrences[key] > 0 && len([]rune(key)) > len([]rune(path))+100 {
				seen[key] = path
			}
			switch v[0] {
			case '{':
				var obj map[string]json.RawMessage
				_ = json.Unmarshal(v, &obj)
				names := []string{}
				for k := range obj {
					names = append(names, k)
				}
				sort.Strings(names)
				for _, k := range names {
					seed(obj[k], path+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"))
				}
			case '[':
				var arr []json.RawMessage
				_ = json.Unmarshal(v, &arr)
				for i, x := range arr {
					seed(x, path+"/"+strconv.Itoa(i))
				}
			}
		}
		for _, key := range []string{"context", "evidenceContext", "evidenceScreening", "researchHandoff"} {
			if raw, ok := root[key]; ok {
				seed(raw, "/"+key)
			}
		}
	}
	var share func(json.RawMessage, string) (json.RawMessage, bool)
	share = func(raw json.RawMessage, pointer string) (json.RawMessage, bool) {
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			return raw, false
		}
		value := compact.Bytes()
		// Short values do not repay the pointer/encoding overhead. Raw JSON
		// comparison preserves number spellings and distinct escaped strings;
		// equality is deliberately stricter than semantic resemblance.
		eligible := len(value) > 0 && (value[0] == '{' || value[0] == '[' || value[0] == '"') && len([]rune(string(value))) >= 32
		key := string(value)
		if eligible {
			if target, ok := seen[key]; ok {
				id, known := sharedIDs[target]
				if !known {
					id = len(sharedPaths)
				}
				ref, _ := json.Marshal(map[string]int{researchInputSameAsKey: id})
				if len([]rune(string(ref)))+8 < len([]rune(key)) {
					if !known {
						sharedIDs[target] = id
						sharedPaths = append(sharedPaths, target)
					}
					return ref, true
				}
			}
			// Keep the first profitable repeated subtree ENTIRELY literal. This
			// permits subsequent whole-value sharing without reference chains,
			// and avoids expanding one repeated array into many child references.
			ref, _ := json.Marshal(map[string]int{researchInputSameAsKey: len(sharedPaths)})
			saving := len([]rune(key)) - len([]rune(string(ref)))
			if copies := occurrences[key] - 1; copies > 0 && saving > 8 && copies*saving > len([]rune(pointer))+64 {
				seen[key] = pointer
				return raw, false
			}
		}
		changed := false
		switch value[0] {
		case '{':
			var fields map[string]json.RawMessage
			if json.Unmarshal(value, &fields) != nil {
				return raw, false
			}
			names := make([]string, 0, len(fields))
			for name := range fields {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				child, replaced := share(fields[name], pointer+"/"+strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1"))
				if replaced {
					fields[name], changed = child, true
				}
			}
			if changed {
				value, _ = json.Marshal(fields)
			}
		case '[':
			var entries []json.RawMessage
			if json.Unmarshal(value, &entries) != nil {
				return raw, false
			}
			for i := range entries {
				child, replaced := share(entries[i], pointer+"/"+strconv.Itoa(i))
				if replaced {
					entries[i], changed = child, true
				}
			}
			if changed {
				value, _ = json.Marshal(entries)
			}
		}
		// Targets are registered before descending and left entirely literal;
		// changed parents cannot become a target, so there are no chains/cycles.
		if changed {
			return append(json.RawMessage(nil), value...), true
		}
		return raw, false
	}
	projected, changed := share(root["researchSourceContext"], "/researchSourceContext")
	if !changed {
		return input, ""
	}
	root["researchSourceContext"] = projected
	encoded, err := json.Marshal(root)
	paths, _ := json.Marshal(sharedPaths)
	instruction := researchInputSharingInstruction + "\n<stage_input_shared_paths>\n" + string(paths) + "\n</stage_input_shared_paths>\n"
	if review {
		instruction += "\nReview shared-path targets may also point to literal current context/evidenceContext/evidenceScreening/researchHandoff values. They remain exact same-input copies, not missing data or new scientific evidence.\n"
	}
	if err != nil || len([]rune(string(input)))-len([]rune(string(encoded))) < len([]rune(instruction))+1024 {
		return input, ""
	}
	return encoded, instruction
}

// An untrusted object with the reserved key is ordinary data. In that case do
// not enable the encoding at all; never let it become a host-issued reference.
func researchInputContainsSharingKey(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if token == researchInputSameAsKey {
			return true
		}
	}
}
