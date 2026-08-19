package document

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	headingPrefixPattern = regexp.MustCompile(`(?i)^(?:第[一二三四五六七八九十百0-9]+[章节部分篇]|[一二三四五六七八九十]+[、.．]|[（(][一二三四五六七八九十0-9]+[）)]|(?:\d+(?:\.\d+)+|\d{1,2}[、.．)]|\d{1,2}\s+)\s*|(?:abstract|introduction|background|methods?|materials?\s+and\s+methods?|results?|discussion|conclusions?|references?|acknowledg(?:e)?ments?)\s*[:：]?$|(?:摘要|关键词|引言|绪论|结论|参考文献|致谢)\s*[:：]?$)`)
	headingOnlyPrefix    = regexp.MustCompile(`^(?:第[一二三四五六七八九十百0-9]+[章节部分篇]|[一二三四五六七八九十]+[、.．]|[（(][一二三四五六七八九十0-9]+[）)]|\d+(?:\.\d+){0,3}[、.．)])$`)
	pageNumberPattern    = regexp.MustCompile(`(?i)^(?:第\s*)?(?:\d{1,6}|[ivxlcdm]{1,8})(?:\s*(?:/|of)\s*\d{1,6})?(?:\s*页)?$`)
)

func normalizeLine(value string) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\x00':
			return -1
		case '\u00a0', '\u2007', '\u202f':
			return ' '
		default:
			return r
		}
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func normalizeExplicitText(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = normalizeLine(line)
		if line == "" {
			if len(result) > 0 {
				blank = true
			}
			continue
		}
		if blank && len(result) > 0 && result[len(result)-1] != "" {
			result = append(result, "")
		}
		result = append(result, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func normalizedSourceLines(value string) []string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	result := make([]string, 0, strings.Count(value, "\n")+1)
	for _, line := range strings.Split(value, "\n") {
		if line = normalizeLine(line); line != "" {
			result = append(result, line)
		}
	}
	return coalesceStructuralFragments(result)
}

func coalesceStructuralFragments(lines []string) []string {
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(result) > 0 && punctuationFragment(line) {
			result[len(result)-1] += line
			continue
		}
		if len(result) > 0 && headingOnlyPrefix.MatchString(result[len(result)-1]) {
			result[len(result)-1] += line
			continue
		}
		result = append(result, line)
	}
	return result
}

func punctuationFragment(value string) bool {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 || len(runes) > 3 {
		return false
	}
	for _, r := range runes {
		if !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

func looksLikeHeading(value string) bool {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) == 0 || len(runes) > 120 || endsSentence(value) {
		return false
	}
	return headingPrefixPattern.MatchString(value)
}

func endsSentence(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	switch []rune(value)[len([]rune(value))-1] {
	case '.', '!', '?', ';', '。', '！', '？', '；':
		return true
	default:
		return false
	}
}

func joinWrappedLines(lines []string) string {
	var result strings.Builder
	previous := ""
	for _, line := range lines {
		line = normalizeLine(line)
		if line == "" {
			continue
		}
		if result.Len() > 0 {
			if dehyphenateBoundary(previous, line) {
				text := result.String()
				result.Reset()
				result.WriteString(strings.TrimSuffix(text, "-"))
			} else if needsJoinSpace(previous, line) {
				result.WriteByte(' ')
			}
		}
		result.WriteString(line)
		previous = line
	}
	return strings.TrimSpace(result.String())
}

func dehyphenateBoundary(left, right string) bool {
	leftRunes, rightRunes := []rune(strings.TrimSpace(left)), []rune(strings.TrimSpace(right))
	return len(leftRunes) > 1 && leftRunes[len(leftRunes)-1] == '-' && unicode.IsLetter(leftRunes[len(leftRunes)-2]) && len(rightRunes) > 0 && unicode.IsLower(rightRunes[0])
}

func needsJoinSpace(left, right string) bool {
	leftRunes, rightRunes := []rune(strings.TrimSpace(left)), []rune(strings.TrimSpace(right))
	if len(leftRunes) == 0 || len(rightRunes) == 0 {
		return false
	}
	last, first := leftRunes[len(leftRunes)-1], rightRunes[0]
	if unicode.Is(unicode.Han, last) || unicode.Is(unicode.Han, first) || unicode.IsPunct(first) {
		return false
	}
	return unicode.IsLetter(last) || unicode.IsDigit(last) || unicode.IsLetter(first) || unicode.IsDigit(first)
}

func sectionPath(headings []string) string {
	parts := make([]string, 0, len(headings))
	for _, heading := range headings {
		if heading = strings.TrimSpace(heading); heading != "" {
			parts = append(parts, heading)
		}
	}
	return strings.Join(parts, " > ")
}
