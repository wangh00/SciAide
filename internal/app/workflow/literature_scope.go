package workflow

import (
	"encoding/json"
	"fmt"
	research "github.com/wangh00/SciAide/internal/app/research"
	"strings"
)

type literatureScope struct {
	Years        research.PublicationYears `json:"publicationYears"`
	YearEvidence string                    `json:"yearEvidence"`
}

const literatureScopeInstruction = "同时提取 originalRequest 原始研究要求明确指定的发表年份：publicationYears.from/to 为整数，未指定填 0；yearEvidence 逐字摘录 originalRequest 中明确约束文献发表年份的原句（没有 originalRequest 时才使用 researchContext）。不要把随访年限、患者年龄、实验日期当作发表年份，不得自行缩小年限。检索式只放人群与干预等学术概念，年份由宿主通过数据库参数过滤，不把长年份列表当关键词。不同查询需覆盖真实互补概念，不根据交付篇幅改变召回范围。"

func literatureScopeSchema() json.RawMessage {
	s := decodeObject(raw(`{"type":"object","additionalProperties":false,"required":["queries","rationale","publicationYears","yearEvidence"],"properties":{"queries":{"type":"array","minItems":2,"maxItems":4,"uniqueItems":true,"items":{"type":"string","minLength":3,"maxLength":500}},"rationale":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"string","minLength":1,"maxLength":800}},"publicationYears":{"type":"object","additionalProperties":false,"required":["from","to"],"properties":{"from":{"type":"integer","minimum":0,"maximum":3000},"to":{"type":"integer","minimum":0,"maximum":3000}}},"yearEvidence":{"type":"string","maxLength":1000}}}`))
	p := s["properties"].(map[string]any)
	for _, name := range []string{"queries", "rationale"} {
		p[name].(map[string]any)["minItems"] = 1
		p[name].(map[string]any)["maxItems"] = 2
	}
	p["providerQueries"] = literatureProviderQueriesSchema()
	s["required"] = []string{"queries", "rationale", "publicationYears", "yearEvidence", "providerQueries"}
	return mustJSON(s)
}

func literatureProviderQueriesSchema() map[string]any {
	p := map[string]any{}
	for _, source := range []string{"pubmed", "europepmc", "crossref", "openalex"} {
		p[source] = map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "items": map[string]any{"type": "string", "minLength": 3, "maxLength": 500}}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"pubmed", "europepmc", "crossref", "openalex"}, "properties": p}
}

func validateLiteratureScope(output, input json.RawMessage) error {
	var plan struct {
		Queries         []string            `json:"queries"`
		ProviderQueries map[string][]string `json:"providerQueries"`
	}
	if json.Unmarshal(output, &plan) != nil {
		return fmt.Errorf("invalid literature query plan")
	}
	if plan.ProviderQueries != nil {
		for _, source := range []string{"pubmed", "europepmc", "crossref", "openalex"} {
			values := plan.ProviderQueries[source]
			if len(values) != len(plan.Queries) || len(values) < 1 || len(values) > 2 {
				return fmt.Errorf("%s query count must match the 1-2 planned queries", source)
			}
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("empty provider query")
				}
				if source == "openalex" && (strings.ContainsAny(value, "*?[]") || strings.Contains(value, "TITLE_ABS:")) {
					return fmt.Errorf("OpenAlex plan requires native Boolean phrases without foreign fields or wildcards")
				}
			}
		}
	}
	var scope literatureScope
	if err := json.Unmarshal(output, &scope); err != nil {
		return err
	}
	if err := scope.Years.Validate(); err != nil {
		return err
	}
	if !scope.Years.Active() {
		return nil
	}
	var contains func(any) bool
	contains = func(v any) bool {
		switch x := v.(type) {
		case string:
			return strings.Contains(x, scope.YearEvidence)
		case []any:
			for _, v := range x {
				if contains(v) {
					return true
				}
			}
		case map[string]any:
			for _, v := range x {
				if contains(v) {
					return true
				}
			}
		}
		return false
	}
	inputObject := decodeObject(input)
	var evidenceInput any = inputObject
	if original, ok := inputObject["originalRequest"]; ok {
		evidenceInput = original
	}
	if strings.TrimSpace(scope.YearEvidence) == "" || !contains(evidenceInput) {
		return fmt.Errorf("publication range requires an exact quote from frozen research requirements")
	}
	for _, year := range []int{scope.Years.From, scope.Years.To} {
		if year != 0 && !strings.Contains(scope.YearEvidence, fmt.Sprint(year)) {
			return fmt.Errorf("publication year is not grounded in the quoted requirement")
		}
	}
	return nil
}

func frozenLiteratureYears(detail RunDetail) research.PublicationYears {
	for _, step := range detail.Steps {
		if step.NodeID == "literature_query_expansion" && step.Status == StepCompleted {
			var output struct {
				Analysis literatureScope `json:"analysis"`
			}
			if json.Unmarshal(step.Output, &output) == nil {
				return output.Analysis.Years
			}
		}
	}
	return research.PublicationYears{}
}

func automaticLiteratureExclusion(c literatureCandidate, years research.PublicationYears) string {
	if strings.EqualFold(c.WorkType, "peer-review") {
		return "来源明确标记为同行评审记录，不是研究论文"
	}
	if years.Active() && len(c.Years) > 0 {
		for _, year := range c.Years {
			if years.Contains(year) {
				return ""
			}
		}
		// Conflicting or missing dates need a model check, even if the preferred
		// record alone would fall outside the range.
		for _, year := range c.Years {
			if year != c.Years[0] {
				return ""
			}
		}
		return fmt.Sprintf("来源一致的发表年份 %d 不符合冻结年限 %d-%d", c.Years[0], years.From, years.To)
	}
	return ""
}
