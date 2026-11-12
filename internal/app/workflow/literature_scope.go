package workflow

import (
	"encoding/json"
	"fmt"
	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/researchsources"
	"strings"
	"time"
)

type literatureScope struct {
	Years        research.PublicationYears `json:"publicationYears"`
	YearEvidence string                    `json:"yearEvidence"`
}

const literatureRetrievalLegacyVersion = "dynamic-literature-query-expansion-v5"
const literatureRetrievalVersion = "dynamic-literature-query-expansion-v6"

func literatureRetrievalSchema() json.RawMessage {
	s := decodeObject(literatureScopeSchema())
	p := s["properties"].(map[string]any)
	p["retrievalProfile"] = map[string]any{"type": "string", "enum": research.RetrievalProfiles()}
	p["sourceRationale"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 800}
	p["sourceIds"] = map[string]any{"type": "array", "maxItems": len(researchsources.IDs()), "uniqueItems": true, "items": map[string]any{"type": "string", "enum": researchsources.IDs()}, "description": "明确限定或选择的完整来源 ID，按优先顺序；只用三库等要求必须列出对应三项。没有来源限制且明确采用学科默认配置时填 []。理由或 providerQueries 不能代替选源。"}
	s["required"] = append(s["required"].([]any), "retrievalProfile", "sourceRationale", "sourceIds")
	return mustJSON(s)
}

const literatureRetrievalInstruction = "同时输出 retrievalProfile、sourceRationale 和 sourceIds：根据冻结研究问题的主体学科明确选择 education、biomedical、computing、general 或 interdisciplinary，不因单个 AI/大学/医院关键词判学科，不因某来源失败改变问题学科。无法确定选 general，真实横跨学科才选 interdisciplinary；sourceRationale 解释和研究对象/干预的关系，不承诺穷尽。先核对 originalRequest、已确认澄清和 userIntent 中仍有效的来源边界：明确只用某些数据库时，sourceIds 必须逐项列出完整集合，理由中写三库、providerQueries 提供三库表达式都不算完成选源。providerQueries 只为 sourceIds 中实际选择且支持原生查询的来源提供表达式，不能附带未选来源；未限定数据库且采用学科默认分配时才提供默认必需来源的表达式。可用 ID 为 openalex、crossref、arxiv、pubmed、europepmc、semantic-scholar、eric、core；需要 ERIC/OpenAlex/Crossref 时填 [\"eric\",\"openalex\",\"crossref\"]。仅未限定数据库且采用学科默认分配时填 []；不要把优先来源误作排他范围，也不得静默扩大明确范围。sourceIds 会冻结并实际传给检索，补查可取子集但不能扩展明确集合，改变范围须重新规划。CORE 仍须显式选择。每次检索调用最多14次实际来源HTTP，含重试/重定向，缺失摘要不自动逐条补全；未安排或失败的来源不是无文献。"

const literatureScopeInstruction = "同时提取研究要求指定的发表年份：publicationYears.from/to 为整数，未指定填 0。明确年份的 yearEvidence 必须逐字摘录已冻结 originalRequest 中的发表时间原句（originalRequest 不存在时才沿用 researchContext），包括原有标点；不要把另一个字段的用户原话填入这个引文字段，即使两处年份相同。userIntent.userRequests、已确认澄清和后续用户变更用于先核对范围，不以 AI 扩写的 originalRequest 新增年限。相对时间是独立通道：用户只说近十年等且未更改时，使用 researchTimeContext.publicationYears，并从 userRequests 摘录相对时间原句；若长讨论省略 userRequests 且 userIntent.readSection=planning_discussion，则逐字引用 researchTimeContext.relativeQuote，必要时读取该冻结讨论，不要求不存在的 userRequests；说明含当前年的年份宽边界。不要把随访年限、患者年龄、实验日期当作发表年份，不得自行缩小年限。检索式只放人群与干预等学术概念，年份由宿主通过数据库参数过滤，不把长年份列表当关键词。不同查询需覆盖真实互补概念，不根据交付篇幅改变召回范围。"

func literatureScopeSchema() json.RawMessage {
	s := decodeObject(raw(`{"type":"object","additionalProperties":false,"required":["queries","rationale","publicationYears","yearEvidence"],"properties":{"queries":{"type":"array","minItems":2,"maxItems":4,"uniqueItems":true,"items":{"type":"string","minLength":3,"maxLength":500}},"rationale":{"type":"array","minItems":2,"maxItems":4,"items":{"type":"string","minLength":1,"maxLength":800}},"publicationYears":{"type":"object","additionalProperties":false,"required":["from","to"],"properties":{"from":{"type":"integer","minimum":0,"maximum":3000},"to":{"type":"integer","minimum":0,"maximum":3000}}},"yearEvidence":{"type":"string","maxLength":1000}}}`))
	p := s["properties"].(map[string]any)
	p["yearEvidence"].(map[string]any)["description"] = "明确年份引用冻结 originalRequest 的发表时间原句及原标点；无 originalRequest 时引用 researchContext。宿主相对时间分支引用 userRequests 的相对原句；长讨论省略 userRequests 且 userIntent.readSection=planning_discussion 时引用 researchTimeContext.relativeQuote。用户原话仍用于核对范围，不能仅因字段来源不同就改变研究范围。"
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
	for _, source := range researchsources.PlanQueryIDs(false) {
		p[source] = map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "items": map[string]any{"type": "string", "minLength": 3, "maxLength": 500}}
	}
	// Required provider keys depend on the separately frozen sourceIds field;
	// explicit source contracts must not force the model to emit projections
	// for forbidden databases. validateLiteratureScope applies that
	// cross-field requirement after the JSON shape has been checked.
	return map[string]any{"type": "object", "additionalProperties": false, "properties": p}
}

func validateLiteratureScope(output, input json.RawMessage) error {
	var plan struct {
		Queries         []string            `json:"queries"`
		ProviderQueries map[string][]string `json:"providerQueries"`
		SourceIDs       []string            `json:"sourceIds"`
	}
	if json.Unmarshal(output, &plan) != nil {
		return fmt.Errorf("invalid literature query plan")
	}
	if plan.ProviderQueries != nil {
		requiredSources := researchsources.PlanQueryIDs(true)
		selected := map[string]bool{}
		if len(plan.SourceIDs) > 0 {
			requiredSources = nil
			for _, source := range plan.SourceIDs {
				selected[source] = true
				if spec, ok := researchsources.Lookup(source); ok && spec.PlanQuery {
					requiredSources = append(requiredSources, source)
				}
			}
		}
		for _, source := range requiredSources {
			if _, ok := plan.ProviderQueries[source]; !ok {
				return fmt.Errorf("missing %s provider queries", source)
			}
		}
		for source, values := range plan.ProviderQueries {
			spec, ok := researchsources.Lookup(source)
			if !ok || !spec.PlanQuery {
				return fmt.Errorf("unsupported provider query source %s", source)
			}
			if len(plan.SourceIDs) > 0 && !selected[source] {
				return fmt.Errorf("provider query source %s is not selected by sourceIds", source)
			}
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
	var clock struct {
		Time   *ResearchTimeContext `json:"researchTimeContext"`
		Intent struct {
			Requests    []string `json:"userRequests"`
			ReadSection string   `json:"readSection"`
		} `json:"userIntent"`
	}
	_ = json.Unmarshal(input, &clock)
	if clock.Time != nil && clock.Time.RelativeQuote != "" && scope.Years == clock.Time.PublicationYears && strings.Contains(strings.Join(clock.Intent.Requests, "\n"), scope.YearEvidence) && strings.Contains(scope.YearEvidence, clock.Time.RelativeQuote) {
		return validateFrozenPublicationAnchor(scope.Years, clock.Time)
	}
	if clock.Time != nil && clock.Time.RelativeQuote != "" && scope.Years == clock.Time.PublicationYears && clock.Intent.ReadSection == "planning_discussion" && scope.YearEvidence == clock.Time.RelativeQuote {
		return validateFrozenPublicationAnchor(scope.Years, clock.Time)
	}

	var evidenceInput any = inputObject
	if original, ok := inputObject["originalRequest"]; ok {
		evidenceInput = original
	}
	if scope.Years.Active() && (strings.TrimSpace(scope.YearEvidence) == "" || !contains(evidenceInput)) {
		return fmt.Errorf("publication range requires an exact quote from frozen research requirements")
	}
	anchor := time.Time{}
	if clock.Time != nil {
		anchor, _ = time.Parse("2006-01-02", clock.Time.Date)
	}
	if handled, err := validateResolvedPublicationTime(scope, evidenceInput, anchor); handled {
		return err
	}
	if !scope.Years.Active() {
		return nil
	}
	for _, year := range []int{scope.Years.From, scope.Years.To} {
		if year != 0 && !strings.Contains(scope.YearEvidence, fmt.Sprint(year)) {
			return fmt.Errorf("publication year is not grounded in the quoted requirement")
		}
	}
	return nil
}

func validateFrozenPublicationAnchor(years research.PublicationYears, clock *ResearchTimeContext) error {
	if clock.Date == "" {
		// Preserve the existing legacy frozen-metadata contract. New captures
		// always contain a date; do not manufacture one for an old task.
		return nil
	}
	anchor, err := time.Parse("2006-01-02", clock.Date)
	if err != nil {
		return fmt.Errorf("publication time requires a valid frozen research date")
	}
	resolved := resolveFrozenPublicationExpression(clock.RelativeQuote, anchor)
	if !resolved.Resolved || resolved.Years != years {
		return fmt.Errorf("publication time does not match its frozen host date")
	}
	return nil
}

func validateResolvedPublicationTime(scope literatureScope, evidence any, anchor time.Time) (bool, error) {
	handled, matched := false, false
	var failure error
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case string:
			if scope.Years.Active() && !strings.Contains(value, scope.YearEvidence) {
				return
			}
			resolved := resolvePublicationTime(value, anchor)
			if !resolved.Found || !scope.Years.Active() && !resolved.Relevant {
				return
			}
			handled = true
			if !resolved.Resolved {
				failure = fmt.Errorf("publication time requires an unambiguous publication requirement and a valid frozen date for derived years")
				return
			}
			if scope.Years != resolved.Years {
				failure = fmt.Errorf("publication range does not match the frozen requirement: expected %d–%d", resolved.Years.From, resolved.Years.To)
				return
			}
			if !resolved.includesQuote(scope.YearEvidence) {
				failure = fmt.Errorf("publication range requires an exact quote containing the complete time expression")
				return
			}
			matched = true
		case []any:
			for _, item := range value {
				visit(item)
			}
		case map[string]any:
			for _, item := range value {
				visit(item)
			}
		}
	}
	visit(evidence)
	if matched && failure == nil {
		return true, nil
	}
	return handled, failure
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

func frozenLiteratureSourceIDs(detail RunDetail) ([]string, error) {
	enabled := false
	for _, node := range detail.Run.Compilation.Nodes {
		if node.ID == "literature_query_expansion" && node.PromptVersion == literatureRetrievalVersion {
			enabled = true
		}
	}
	if !enabled {
		return nil, nil // Older frozen routes keep their original source behavior.
	}
	for _, step := range detail.Steps {
		if step.NodeID != "literature_query_expansion" || step.Status != StepCompleted {
			continue
		}
		var value struct {
			Analysis struct{ SourceIDs []string } `json:"analysis"`
		}
		if json.Unmarshal(step.Output, &value) != nil || value.Analysis.SourceIDs == nil {
			return nil, fmt.Errorf("frozen sourceIds are missing; do not fall back to profile selection")
		}
		seen := map[string]bool{}
		for _, id := range value.Analysis.SourceIDs {
			if _, ok := researchsources.Lookup(id); !ok || seen[id] {
				return nil, fmt.Errorf("invalid or duplicate frozen sourceIds")
			}
			seen[id] = true
		}
		return value.Analysis.SourceIDs, nil
	}
	return nil, fmt.Errorf("frozen sourceIds plan is not completed")
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
