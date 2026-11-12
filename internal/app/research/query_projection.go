package research

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RankedQuery is a lossy retrieval aid, not an equivalent Boolean expression.
// More keywords do not guarantee broader retrieval at a ranking endpoint.
type RankedQuery struct {
	Query       string
	Limitations []string
}

// RankedBibliographicQuery accepts plain terms, quoted phrases, uppercase
// Boolean operators and bracketed field suffixes. Other native query dialects
// must be supplied to a matching native provider, not guessed or sent on error.
func RankedBibliographicQuery(query string) (RankedQuery, error) {
	tokens, err := rankedQueryTokens(query)
	if err != nil {
		return RankedQuery{}, err
	}
	p := rankedQueryParser{tokens: tokens, limitations: map[string]bool{}}
	node, err := p.expression(0)
	if err != nil {
		return RankedQuery{}, err
	}
	if p.index != len(tokens) {
		return RankedQuery{}, fmt.Errorf("unexpected ranked query token %q", tokens[p.index].value)
	}
	terms, seen := []string{}, map[string]bool{}
	var collect func(*rankedQueryNode, bool)
	collect = func(n *rankedQueryNode, negative bool) {
		negative = negative != n.negate
		if n.term != "" && !negative {
			key := strings.ToLower(n.term)
			if !seen[key] {
				terms, seen[key] = append(terms, n.term), true
			}
		}
		for _, child := range n.children {
			collect(child, negative)
		}
	}
	collect(node, false)
	if len(terms) == 0 {
		return RankedQuery{}, fmt.Errorf("ranked query requires a positive search term; exclusions alone cannot be projected")
	}
	result := RankedQuery{Query: strings.Join(terms, " ")}
	for _, code := range []string{"boolean_logic_not_enforced", "negation_not_enforced", "field_scope_not_enforced", "phrase_matching_not_enforced"} {
		if p.limitations[code] {
			result.Limitations = append(result.Limitations, code)
		}
	}
	return result, nil
}

type rankedQueryToken struct{ kind, value string }

func rankedQueryTokens(query string) ([]rankedQueryToken, error) {
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > MaxQueryRunes {
		return nil, fmt.Errorf("invalid ranked query encoding or size")
	}
	runes := []rune(query)
	for _, r := range runes {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return nil, fmt.Errorf("ranked query contains a control character")
		}
	}
	tokens := []rankedQueryToken{}
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		switch runes[i] {
		case '(', ')':
			tokens = append(tokens, rankedQueryToken{kind: string(runes[i]), value: string(runes[i])})
			i++
		case '"':
			i++
			var value strings.Builder
			for i < len(runes) && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
					i++
				}
				value.WriteRune(runes[i])
				i++
			}
			if i == len(runes) || strings.TrimSpace(value.String()) == "" {
				return nil, fmt.Errorf("ranked query has an unclosed or empty phrase")
			}
			if strings.ContainsAny(value.String(), "*?{}") {
				return nil, fmt.Errorf("ranked query cannot preserve wildcard or range syntax inside a phrase")
			}
			tokens = append(tokens, rankedQueryToken{kind: "phrase", value: value.String()})
			i++
		case '[':
			start := i + 1
			i++
			for i < len(runes) && runes[i] != ']' {
				if !unicode.IsLetter(runes[i]) && !unicode.IsDigit(runes[i]) && !strings.ContainsRune(" /_-", runes[i]) {
					return nil, fmt.Errorf("unsupported ranked query field suffix")
				}
				i++
			}
			if i == len(runes) || strings.TrimSpace(string(runes[start:i])) == "" {
				return nil, fmt.Errorf("ranked query has an unclosed or empty field suffix")
			}
			tokens = append(tokens, rankedQueryToken{kind: "field", value: string(runes[start:i])})
			i++
		case ']':
			return nil, fmt.Errorf("ranked query has an unmatched field suffix")
		default:
			start := i
			for i < len(runes) && !unicode.IsSpace(runes[i]) && !strings.ContainsRune("()[]\"", runes[i]) {
				i++
			}
			value := string(runes[start:i])
			if strings.HasPrefix(value, "'") || strings.HasSuffix(value, "'") || strings.ContainsAny(value, "“”‘") || strings.HasSuffix(value, "’") {
				return nil, fmt.Errorf("unsupported ranked query quote; use double-quoted phrases")
			}
			if strings.ContainsAny(value, "*?{}") {
				return nil, fmt.Errorf("ranked query cannot preserve wildcard or range syntax; use plain keywords or a native provider")
			}
			if prefix, rest, found := strings.Cut(value, ":"); found && !strings.Contains(value, "://") && prefix != "" {
				field := unicode.IsLetter([]rune(prefix)[0]) || strings.HasPrefix(prefix, "_")
				for _, r := range prefix {
					field = field && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-')
				}
				next := i
				for next < len(runes) && unicode.IsSpace(runes[next]) {
					next++
				}
				qualifiedGroup := next < len(runes) && strings.ContainsRune("(\"[", runes[next])
				if field && (rest != "" || rankedNativeField(prefix) || qualifiedGroup) {
					return nil, fmt.Errorf("ranked query cannot interpret field prefix %q; use plain keywords or a native provider", prefix)
				}
			}
			if i < len(runes) && runes[i] == '(' && rankedNativeField(value) {
				return nil, fmt.Errorf("ranked query cannot interpret field function %q; use plain keywords or a native provider", value)
			}
			kind := "term"
			if value == "AND" || value == "OR" || value == "NOT" || value == "ANDNOT" {
				kind = value
			}
			tokens = append(tokens, rankedQueryToken{kind: kind, value: value})
		}
	}
	return tokens, nil
}

func rankedNativeField(value string) bool {
	switch strings.ToLower(strings.TrimSuffix(value, "=")) {
	case "title", "ti", "abstract", "abs", "ab", "author", "au", "title_abs", "title-abs-key", "ts", "mesh", "mh", "doi", "pmid", "all", "subject", "descriptor", "educationlevel", "year", "pubyear", "publicationdateyear":
		return true
	default:
		return false
	}
}

type rankedQueryNode struct {
	term     string
	children []*rankedQueryNode
	negate   bool
}

type rankedQueryParser struct {
	tokens      []rankedQueryToken
	index       int
	limitations map[string]bool
}

func (p *rankedQueryParser) kind() string {
	if p.index == len(p.tokens) {
		return ""
	}
	return p.tokens[p.index].kind
}

func (p *rankedQueryParser) expression(depth int) (*rankedQueryNode, error) {
	left, err := p.conjunction(depth)
	for err == nil && p.kind() == "OR" {
		p.limitations["boolean_logic_not_enforced"] = true
		p.index++
		var right *rankedQueryNode
		right, err = p.conjunction(depth)
		left = &rankedQueryNode{children: []*rankedQueryNode{left, right}}
	}
	return left, err
}

func (p *rankedQueryParser) conjunction(depth int) (*rankedQueryNode, error) {
	left, err := p.atom(depth)
	for err == nil && p.kind() != "" && p.kind() != ")" && p.kind() != "OR" {
		negate := p.kind() == "ANDNOT"
		if p.kind() == "AND" || negate {
			p.limitations["boolean_logic_not_enforced"] = true
			p.index++
		}
		var right *rankedQueryNode
		right, err = p.atom(depth)
		if err == nil && negate {
			right.negate = !right.negate
			p.limitations["negation_not_enforced"] = true
		}
		left = &rankedQueryNode{children: []*rankedQueryNode{left, right}}
	}
	return left, err
}

func (p *rankedQueryParser) atom(depth int) (*rankedQueryNode, error) {
	if depth > 32 || p.kind() == "" {
		return nil, fmt.Errorf("incomplete or deeply nested ranked query")
	}
	token := p.tokens[p.index]
	p.index++
	var node *rankedQueryNode
	switch token.kind {
	case "NOT":
		var err error
		node, err = p.atom(depth + 1)
		if err != nil {
			return nil, err
		}
		node.negate = !node.negate
		p.limitations["negation_not_enforced"] = true
		return node, nil
	case "(":
		var err error
		node, err = p.expression(depth + 1)
		if err != nil {
			return nil, err
		}
		if p.kind() != ")" {
			return nil, fmt.Errorf("ranked query has an unclosed group")
		}
		p.index++
	case "term", "phrase":
		node = &rankedQueryNode{term: token.value}
		if token.kind == "phrase" {
			p.limitations["phrase_matching_not_enforced"] = true
		}
	default:
		return nil, fmt.Errorf("unexpected ranked query token %q", token.value)
	}
	if p.kind() == "field" {
		p.limitations["field_scope_not_enforced"] = true
		p.index++
	}
	return node, nil
}
