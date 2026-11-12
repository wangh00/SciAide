package workflow

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
)

// The resolver owns supported publication-time expressions for both freezing
// human intent and validating model submissions. Quotes remain literal; only
// derived numeric bounds use the frozen host date. It does not interpret
// arbitrary dates as publication requirements or guess ambiguous constraints.
type publicationTimeResolution struct {
	Found, Relevant, Resolved bool
	Relative                  bool
	Quote                     string
	Quotes                    []string
	Years                     research.PublicationYears
}

var relativeResearchYears = regexp.MustCompile(`(?:最近|过去|近约|近)\s*(十|五|三|两|二|一|[0-9]{1,2})\s*年`)
var publicationSinceYear = regexp.MustCompile(`(?:自|从)?\s*((?:19|20|21)[0-9]{2})\s*年?\s*(?:起\s*)?(?:至今|到现在|至目前|到目前|到今天|以来)`)
var publicationClosedYears = regexp.MustCompile(`((?:19|20|21)[0-9]{2})\s*年?\s*(?:—|–|-|至|到|~|～)\s*((?:19|20|21)[0-9]{2})\s*年?`)
var publicationEnglishSince = regexp.MustCompile(`(?i:since)\s+((?:19|20|21)[0-9]{2})`)
var publicationEnglishPast = regexp.MustCompile(`(?i:last|past)\s+([0-9]{1,2})\s+(?i:years?)`)
var publicationTimeNegation = regexp.MustCompile(`(?:不要|排除|不纳入|不检索|不查|不是|不采用|不使用|不包括)(?:检索|纳入|查)?\s*$|(?i:\b(?:exclude|excluding|not)\b[^.!?;,]*)$`)
var publicationTimeMention = regexp.MustCompile(`(?:19|20|21)[0-9]{2}|(?:最近|过去|近约|近)\s*(?:几|若干)\s*年`)

func resolvePublicationTime(text string, anchor time.Time) publicationTimeResolution {
	return resolvePublicationTimeInScope(text, anchor, false)
}

// A host-frozen expression already has a publication role. Its literal text
// and numeric bounds still pass through the exact same resolver.
func resolveFrozenPublicationExpression(text string, anchor time.Time) publicationTimeResolution {
	return resolvePublicationTimeInScope(text, anchor, true)
}

func resolvePublicationTimeInScope(text string, anchor time.Time, boundExpression bool) publicationTimeResolution {
	type expression struct {
		start, end int
		kind       int
		groups     []int
	}
	var expressions []expression
	for _, rule := range []struct {
		pattern *regexp.Regexp
		kind    int
	}{{relativeResearchYears, 0}, {publicationSinceYear, 1}, {publicationClosedYears, 2}, {publicationEnglishSince, 1}, {publicationEnglishPast, 0}} {
		for _, groups := range rule.pattern.FindAllStringSubmatchIndex(text, -1) {
			expressions = append(expressions, expression{groups[0], groups[1], rule.kind, groups})
		}
	}
	sort.Slice(expressions, func(i, j int) bool { return expressions[i].start < expressions[j].start })
	result := publicationTimeResolution{Found: len(expressions) > 0}
	// A later relevant but unsupported/withdrawn scope must clear an old
	// relative anchor; an unrelated birth/enrolment/experimental date must not.
	for _, mention := range publicationTimeMention.FindAllStringIndex(text, -1) {
		clause, _, _ := publicationTimeClause(text, mention[0], mention[1])
		result.Relevant = result.Relevant || isPublicationTimeClause(clause)
	}
	for _, phrase := range []string{"不限制发表年份", "不限发表年份", "取消年份限制", "不限制年份"} {
		result.Relevant = result.Relevant || strings.Contains(text, phrase)
	}
	invalid := false
	for _, expr := range expressions {
		clause, prefix, suffix := publicationTimeClause(text, expr.start, expr.end)
		if !boundExpression && !isPublicationTimeClause(clause) {
			continue
		}
		result.Relevant = true
		if publicationTimeNegation.MatchString(prefix) || strings.Contains(suffix, "仅作背景") || strings.Contains(suffix, "只是背景") {
			continue
		}
		quote := strings.TrimSpace(text[expr.start:expr.end])
		number, _ := strconv.Atoi(text[expr.groups[2]:expr.groups[3]])
		years := research.PublicationYears{}
		relative := expr.kind != 2
		switch expr.kind {
		case 0:
			if number == 0 {
				number = map[string]int{"十": 10, "五": 5, "三": 3, "两": 2, "二": 2, "一": 1}[text[expr.groups[2]:expr.groups[3]]]
			}
			if number < 1 {
				invalid = true
				continue
			}
			years = research.PublicationYears{From: anchor.Year() - number, To: anchor.Year()}
		case 1:
			years = research.PublicationYears{From: number, To: anchor.Year()}
		case 2:
			end, _ := strconv.Atoi(text[expr.groups[4]:expr.groups[5]])
			years = research.PublicationYears{From: number, To: end}
		}
		if relative && anchor.IsZero() || years.Validate() != nil {
			invalid = true
			continue
		}
		if result.Resolved && result.Years != years {
			invalid = true
		}
		if !result.Resolved || relative && !result.Relative {
			result.Quote = quote
		}
		result.Resolved, result.Years = true, years
		result.Relative = result.Relative || relative
		result.Quotes = append(result.Quotes, quote)
	}
	if invalid {
		result.Resolved = false
	}
	return result
}

func publicationTimeClause(text string, start, end int) (clause, prefix, suffix string) {
	const separators = "\n\r，,；;。.!?！？"
	left := 0
	for index, char := range text[:start] {
		if strings.ContainsRune(separators, char) {
			left = index + len(string(char))
		}
	}
	right := len(text)
	if index := strings.IndexAny(text[end:], separators); index >= 0 {
		right = end + index
	}
	return text[left:right], text[left:start], text[end:right]
}

func isPublicationTimeClause(clause string) bool {
	for _, word := range []string{"随访", "病史", "年龄", "实验日期", "实验时间", "采集时间", "招募时间", "入学", "出生"} {
		if strings.Contains(clause, word) {
			return false
		}
	}
	lower := strings.ToLower(clause)
	for _, word := range []string{"follow-up", "medical history", "participant age", "birth date", "enrolment date", "experimental date", "recruitment date"} {
		if strings.Contains(lower, word) {
			return false
		}
	}
	for _, word := range []string{"文献", "论文", "发表", "检索", "只查", "改查", "研究", "年份", "时间：", "时间:"} {
		if strings.Contains(clause, word) {
			return true
		}
	}
	for _, word := range []string{"published", "publication", "literature", "papers", "articles", "studies"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func (r publicationTimeResolution) includesQuote(quote string) bool {
	for _, expression := range r.Quotes {
		if strings.Contains(quote, expression) {
			return true
		}
	}
	return false
}
