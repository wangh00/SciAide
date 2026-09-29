package workflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const evidenceGapInstruction = "对用户要求的字段及准备加入报告的字段，缺失时逐项填写 evidenceGaps（无缺项可省略）：field 为具体指标，status 为 not_retrieved（当前片段未找到）、unreadable（材料无法可靠读取）或 explicitly_not_reported（原文明确说未报告），required 表示是否属于原始核心交付。query 是针对该字段的简短检索词，优先用原文学术词和同义词，不复述整段限制；not_retrieved 即使整体 coverage 足够也要补查，不能因它是次要字段就直接写未报告。explicitly_not_reported 必须提供 reference 和 supportingQuote，逐字引用当前候选中明确说明缺失的连续原句；补查无命中也不能证明全文未报告。unreadable 不触发重复关键词检索。已尝试的查询见 localEvidenceSearchQueries，剩余额度见 localEvidenceSearchRoundsRemaining。补查后仍缺少核心字段时 coverage 必须不足；非核心缺项标为当前材料未核验，不强行扩展课题。相对指标 OR/RR/HR 不等于绝对发生率，不得混填；遇到相关指标应说明指标差别，而非声称原文没有任何结果。"

type evidenceGap struct {
	Field           string `json:"field"`
	Status          string `json:"status"`
	Required        bool   `json:"required"`
	Query           string `json:"query,omitempty"`
	Reference       string `json:"reference,omitempty"`
	SupportingQuote string `json:"supportingQuote,omitempty"`
}

func evidenceGapSchema() map[string]any {
	return map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"field", "status", "required"},
		"properties": map[string]any{
			"field":           map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
			"status":          map[string]any{"type": "string", "enum": []string{"not_retrieved", "unreadable", "explicitly_not_reported"}},
			"required":        map[string]any{"type": "boolean"},
			"query":           map[string]any{"type": "string", "maxLength": 200},
			"reference":       map[string]any{"type": "string"},
			"supportingQuote": map[string]any{"type": "string", "maxLength": 700},
		},
	}}
}

func validateEvidenceGaps(output, input json.RawMessage) error {
	var out struct {
		Gaps     []evidenceGap `json:"evidenceGaps"`
		Coverage struct {
			Sufficient bool `json:"sufficientForClaimedScope"`
		} `json:"coverage"`
	}
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return err
	}
	_ = json.Unmarshal(input, &in)
	for _, g := range out.Gaps {
		if g.Status == "not_retrieved" && strings.TrimSpace(g.Query) == "" {
			return fmt.Errorf("缺项 %s 尚未检索到，必须提供针对该字段的 query；不能直接判定原文未报告", g.Field)
		}
		if g.Required && g.Status != "explicitly_not_reported" && out.Coverage.Sufficient {
			return fmt.Errorf("核心字段 %s 尚未核验，coverage.sufficientForClaimedScope 不能为 true", g.Field)
		}
		if g.Status != "explicitly_not_reported" {
			continue
		}
		valid := false
		for _, c := range in.Candidates {
			if c.Reference == g.Reference && strings.TrimSpace(g.SupportingQuote) != "" && strings.Contains(c.Quote, g.SupportingQuote) {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("缺项 %s 的原文未报告判定必须附当前候选的 reference 和连续 supportingQuote；无明确原句时改为 not_retrieved 或 unreadable", g.Field)
		}
	}
	return nil
}

// Legacy free-text gaps still get one bounded follow-up. Queries are data,
// never model instructions or document identities. No absence conclusion is
// inferred from keyword matching; this only requests additional retrieval.
func gapFollowupQueries(gaps []evidenceGap, notes []string, previous []string) []string {
	queries := []string{}
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q != "" && len([]rune(q)) <= 200 && !containsQuery(previous, q) {
			queries = appendUnique(queries, q)
		}
	}
	for _, g := range gaps {
		if g.Status == "not_retrieved" {
			add(g.Query)
		}
	}
	if len(gaps) == 0 {
		for _, note := range notes {
			lower := strings.ToLower(note)
			missing := false
			for _, word := range []string{"未报告", "未给出", "未单独给出", "未找到", "未检索", "缺失", "无三种", "not reported", "not retrieved", "not found", "missing"} {
				missing = missing || strings.Contains(lower, word)
			}
			if !missing {
				continue
			}
			if strings.Contains(lower, "脱落") || strings.Contains(lower, "可接受性") || strings.Contains(lower, "dropout") || strings.Contains(lower, "acceptability") {
				add("acceptability dropping out dropout odds ratio")
			} else {
				add(note)
			}
		}
	}
	return queries
}

// Inspect the frozen draft independently of model-supplied gap declarations.
// This is a provenance guard, not a semantic claim that a quote proves absence.
// The reviewer must still assess the meaning of the exact source sentence.
func unsupportedReportAbsenceClaims(input json.RawMessage) []string {
	var in struct {
		Context struct {
			Markdown string `json:"markdown"`
		} `json:"context"`
		Screening struct {
			Gaps []evidenceGap `json:"evidenceGaps"`
		} `json:"evidenceScreening"`
		Evidence []tool.CitationRef `json:"evidenceContext"`
	}
	_ = json.Unmarshal(input, &in)
	claims := []string{}
	for _, line := range strings.Split(in.Context.Markdown, "\n") {
		if !strings.Contains(line, "未报告") && !strings.Contains(strings.ToLower(line), "not reported") {
			continue
		}
		supported := false
		for _, g := range in.Screening.Gaps {
			if g.Status != "explicitly_not_reported" || strings.TrimSpace(g.Field) == "" || !strings.Contains(line, g.Field) || g.Reference == "" || !strings.Contains(line, g.Reference) || strings.TrimSpace(g.SupportingQuote) == "" {
				continue
			}
			for _, c := range in.Evidence {
				if c.Reference == g.Reference && strings.Contains(c.Quote, g.SupportingQuote) {
					supported = true
				}
			}
		}
		if !supported {
			runes := []rune(strings.TrimSpace(line))
			if len(runes) > 400 {
				runes = runes[:400]
			}
			claims = append(claims, string(runes))
			if len(claims) == 20 {
				break
			}
		}
	}
	return claims
}
