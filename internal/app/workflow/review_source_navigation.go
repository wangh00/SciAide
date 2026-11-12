package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Build the immutable structural index while the source reader's cancellation
// is still available. SubmissionMenu only encodes this already-built view.
func (c *ReviewSourceCatalog) prepareNavigation(ctx context.Context) error {
	wanted := map[string]bool{}
	for _, segment := range c.Segments {
		wanted[segment.Pointer] = true
	}
	ranges, err := reviewNavigationJSONRanges(ctx, c.frozenInput, wanted)
	if err != nil {
		return err
	}
	sources := [][]any{}
	last := ""
	for index, segment := range c.Segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(sources) == 0 || last != segment.Pointer {
			r, ok := ranges[segment.Pointer]
			if !ok {
				return fmt.Errorf("review source has no exact original JSON range")
			}
			if c.Version == reviewSourceProtocolV1 && utf8.RuneCountInString(segment.Pointer) > 2048 {
				// Frozen v1 ignored this reader error and emitted zero navigation
				// coordinates. Preserve that historical view; never broaden its
				// material reader or scientific acceptance during a replay.
				r = [2]int{}
			}
			flags := 0
			if segment.Requirement {
				flags |= 1
			}
			if segment.WholeScalar {
				flags |= 2
			}
			sources = append(sources, []any{segment.Pointer, index + 1, []int{}, flags, r[0], r[1]})
			last = segment.Pointer
		}
		row := sources[len(sources)-1]
		row[2] = append(row[2].([]int), segment.End)
	}
	c.navigationRows = mustJSON(sources)
	if c.Version == ReviewSourceProtocolVersion {
		grouped, err := groupedReviewNavigation(ctx, sources)
		if err != nil {
			return err
		}
		if len(grouped)+96 < len(c.navigationRows) {
			c.navigationGroups = grouped
			// Keep ordinary v2 captures byte-stable. Only the genuinely wide
			// layout uses the second lossless encoding; it is selected solely
			// when it materially reduces the already-grouped menu.
			if len(grouped) > 200_000 {
				compact, compactErr := compactReviewNavigation(ctx, sources)
				if compactErr != nil {
					return compactErr
				}
				if len(compact)+96 < len(grouped) {
					c.navigationTrie = compact
				}
			}
		}
	}
	return ctx.Err()
}

// One pass over the ORIGINAL serialization. Token offsets include its actual
// string escaping/whitespace; rune coordinates are accumulated monotonically,
// not by reparsing and hashing the entire input for every individual scalar.
func reviewNavigationJSONRanges(ctx context.Context, input json.RawMessage, wanted map[string]bool) (map[string][2]int, error) {
	result := map[string][2]int{}
	if len(wanted) == 0 {
		return result, ctx.Err()
	}
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	lastByte, lastRune := 0, 0
	var walk func(string) error
	walk = func(pointer string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := int(d.InputOffset())
		for start < len(input) && strings.ContainsRune(" \r\n\t:,", rune(input[start])) {
			start++
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok {
						return fmt.Errorf("invalid review source object key")
					}
					name = strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
					if err := walk(pointer + "/" + name); err != nil {
						return err
					}
				}
			case '[':
				for i := 0; d.More(); i++ {
					if err := walk(pointer + "/" + strconv.Itoa(i)); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("invalid review source container")
			}
			_, err := d.Token()
			return err
		}
		if wanted[pointer] {
			end := int(d.InputOffset())
			if start < lastByte || start >= end || end > len(input) {
				return fmt.Errorf("invalid review source serialized coordinates")
			}
			a := lastRune + utf8.RuneCount(input[lastByte:start])
			b := a + utf8.RuneCount(input[start:end])
			result[pointer] = [2]int{a, b}
			lastByte, lastRune = end, b
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing review source JSON")
	}
	if len(result) != len(wanted) {
		return nil, fmt.Errorf("review navigation did not locate every source")
	}
	return result, ctx.Err()
}

// Share literal RFC 6901 pointer prefixes, not source values. Partitioning is
// deterministic and bounded (16384 catalog segments, 128 candidate rows per
// group); every row remains in its original order, with all coordinates intact.
// A group uses one FULL prefix, never a reference to another group's prefix.
func groupedReviewNavigation(ctx context.Context, rows [][]any) (json.RawMessage, error) {
	n := len(rows)
	rowSizes := make([]int, n+1)
	type prefixInfo struct {
		text    string
		encoded int
	}
	pool := []prefixInfo{{"", 2}}
	ids := map[string]int{"": 0}
	adjacent := make([]int, n)
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rowSizes[i+1] = rowSizes[i] + len(mustJSON(row))
		if i == 0 {
			continue
		}
		left, right := rows[i-1][0].(string), row[0].(string)
		k := 0
		for k < len(left) && k < len(right) && left[k] == right[k] {
			k++
		}
		// '/' is an ASCII boundary and never lies inside an escaped token.
		prefix := left[:strings.LastIndexByte(left[:k], '/')+1]
		id, ok := ids[prefix]
		if !ok {
			id = len(pool)
			ids[prefix] = id
			pool = append(pool, prefixInfo{prefix, len(mustJSON(prefix))})
		}
		adjacent[i] = id
	}
	best := make([]int, n+1)
	ends := make([]int, n)
	prefixes := make([]int, n)
	for i := n - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		common := 0
		for j := i; j < n && j < i+128; j++ {
			if j > i {
				id := adjacent[j]
				if j == i+1 || len(pool[id].text) < len(pool[common].text) {
					common = id
				}
			}
			p := pool[common].encoded
			count := j - i + 1
			// Exact JSON group length plus its separator and the best suffix.
			cost := p + rowSizes[j+1] - rowSizes[i] - count*(p-2) + count + 5 + best[j+1]
			if ends[i] == 0 || cost < best[i] {
				best[i], ends[i], prefixes[i] = cost, j+1, common
			}
		}
	}
	groups := []any{}
	for i := 0; i < n; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end, prefix := ends[i], pool[prefixes[i]].text
		group := make([][]any, 0, end-i)
		for _, row := range rows[i:end] {
			copy := append([]any(nil), row...)
			copy[0] = row[0].(string)[len(prefix):]
			group = append(group, copy)
		}
		groups = append(groups, []any{prefix, group})
		i = end
	}
	return mustJSON(groups), ctx.Err()
}

// compactReviewNavigation keeps the complete v2 source catalog while avoiding
// repeated long JSON-pointer prefixes and per-row serialized-coordinate noise.
// Every row remains in source order. Segment IDs are global: the first segment
// of the first row is 1, and each next row starts after the previous row's
// segmentEnds length. Because every trie leaf is an exact pointer, the model
// can read a source by reconstructing that pointer; json offsets are therefore
// not needed in this compact layout.
func compactReviewNavigation(ctx context.Context, rows [][]any) (json.RawMessage, error) {
	components := []string{}
	componentIDs := map[string]int{}
	type nodeKey struct {
		parent int
		part   string
	}
	nodeIDs := map[nodeKey]int{}
	nodes := [][]int{}
	rowValues := make([][]any, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pointer, ok := row[0].(string)
		if !ok || pointer == "" || !strings.HasPrefix(pointer, "/") {
			return nil, fmt.Errorf("invalid review pointer for compact navigation")
		}
		parent := 0 // node 0 is the empty root
		for _, part := range strings.Split(pointer[1:], "/") {
			componentID, exists := componentIDs[part]
			if !exists {
				componentID = len(components)
				componentIDs[part] = componentID
				components = append(components, part)
			}
			key := nodeKey{parent: parent, part: part}
			nodeID, exists := nodeIDs[key]
			if !exists {
				nodeID = len(nodes) + 1 // zero is the root
				nodeIDs[key] = nodeID
				nodes = append(nodes, []int{parent, componentID})
			}
			parent = nodeID
		}
		ends, ok := row[2].([]int)
		if !ok {
			return nil, fmt.Errorf("invalid review segment ends for compact navigation")
		}
		flags, ok := row[3].(int)
		if !ok {
			return nil, fmt.Errorf("invalid review flags for compact navigation")
		}
		// Pack the two low flag bits into the node number. This removes one
		// repeated JSON cell per source row without changing authority:
		// node = packed >> 2, flags = packed & 3.
		rowValues = append(rowValues, []any{parent*4 + flags, ends})
	}
	value := map[string]any{
		"pointerComponents": components,
		"pointerNodes":      nodes,
		"sourceColumns":     []string{"pointerNodeFlags", "segmentEnds"},
		"sources":           rowValues,
	}
	return mustJSON(value), ctx.Err()
}
