package research

import (
	"strconv"
	"strings"
	"text/scanner"
)

// Ranked bibliographic APIs do not implement PubMed Boolean syntax. This
// projection broadens retrieval; inclusion criteria are still applied by the
// screening stage, never inferred from the provider's ranking.
func RankedBibliographicQuery(query string) string {
	var s scanner.Scanner
	s.Init(strings.NewReader(query))
	s.Mode = scanner.ScanIdents | scanner.ScanStrings | scanner.ScanInts
	s.Error = func(*scanner.Scanner, string) {}
	terms := []string{}
	seen := map[string]bool{}
	skipNext, negativeDepth := false, 0
	for token := s.Scan(); token != scanner.EOF; token = s.Scan() {
		value := s.TokenText()
		if negativeDepth > 0 {
			if token == '(' {
				negativeDepth++
			}
			if token == ')' {
				negativeDepth--
			}
			continue
		}
		if skipNext {
			if token == '(' {
				negativeDepth = 1
				skipNext = false
				continue
			}
			if token == scanner.String || token == scanner.Ident || token == scanner.Int {
				skipNext = false
				continue
			}
		}
		if token == scanner.Ident && value == "NOT" {
			skipNext = true
			continue
		}
		if token == scanner.String {
			if decoded, err := strconv.Unquote(value); err == nil {
				value = decoded
			}
		}
		if token != scanner.String && token != scanner.Ident && token != scanner.Int {
			continue
		}
		if token == scanner.Ident && (value == "AND" || value == "OR") {
			continue
		}
		key := strings.ToLower(value)
		if !seen[key] {
			terms = append(terms, value)
			seen[key] = true
		}
	}
	if len(terms) == 0 {
		return query
	}
	return strings.Join(terms, " ")
}
