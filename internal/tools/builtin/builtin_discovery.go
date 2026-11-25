package builtin

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func searchOptionalBuiltinDefinitions(defs []tool.Definition, allowed map[string]bool, query string, offset, limit int) (tool.Result, error) {
	if query == "" {
		return tool.Result{}, tool.NewUserFacingError("请提供 query；优先使用当前目录中的完整 builtin 工具名。")
	}
	type match struct {
		def   tool.Definition
		score int
	}
	matches := []match{}
	exact := strings.HasPrefix(query, "builtin.")
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, d := range defs {
		if !tool.OptionalBuiltinDefinition(d.QualifiedName) || (allowed != nil && !allowed[d.QualifiedName]) {
			continue
		}
		score := 0
		if exact {
			if d.QualifiedName != query {
				continue
			}
			score = 1000
		} else {
			hay := strings.ToLower(d.QualifiedName + " " + d.Description)
			for _, term := range terms {
				if strings.Contains(hay, term) {
					score++
				}
			}
			if score == 0 {
				continue
			}
		}
		matches = append(matches, match{d, score})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].def.QualifiedName < matches[j].def.QualifiedName
	})
	start := min(offset, len(matches))
	end := start + min(limit, len(matches)-start)
	items := []map[string]string{}
	for _, m := range matches[start:end] {
		items = append(items, map[string]string{"name": m.def.QualifiedName, "description": tool.SafeActivityText(m.def.Description, 500), "fingerprint": tool.DefinitionFingerprint(m.def)})
	}
	raw, err := json.Marshal(map[string]any{"kind": "builtin", "tools": items, "total": len(matches), "nextOffset": end, "hasMore": end < len(matches), "activation": "next_model_turn", "scopeLimited": allowed != nil})
	return tool.Result{Status: tool.ResultSuccess, Structured: raw}, err
}
