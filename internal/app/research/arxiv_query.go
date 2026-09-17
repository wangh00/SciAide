package research

import (
	"fmt"
	"strconv"
	"strings"
	"text/scanner"
	"unicode"
)

// ArXivQuery binds each search atom to an arXiv field. A single all: prefix
// on a multi-concept expression does not reliably scope subsequent atoms.
func ArXivQuery(query string) (string, error) {
	var scan scanner.Scanner
	scan.Init(strings.NewReader(query))
	scan.Mode = scanner.ScanStrings
	scan.Whitespace = 0
	var lexErr error
	scan.Error = func(_ *scanner.Scanner, message string) { lexErr = fmt.Errorf("invalid arXiv query: %s", message) }
	tokens := []string{}
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for token := scan.Scan(); token != scanner.EOF; token = scan.Scan() {
		switch {
		case unicode.IsSpace(token):
			flush()
		case token == '(' || token == ')' || token == scanner.String:
			flush()
			tokens = append(tokens, scan.TokenText())
		default:
			word.WriteString(scan.TokenText())
		}
	}
	flush()
	if lexErr != nil {
		return "", lexErr
	}
	if len(tokens) == 0 || len(tokens) > 512 {
		return "", fmt.Errorf("invalid arXiv query size")
	}
	p := arxivQueryParser{tokens: tokens}
	value, err := p.expression("all", 0)
	if err != nil {
		return "", err
	}
	if p.index != len(tokens) {
		return "", fmt.Errorf("unexpected arXiv query token %q", tokens[p.index])
	}
	return value, nil
}

type arxivQueryParser struct {
	tokens []string
	index  int
}

func (p *arxivQueryParser) expression(field string, depth int) (string, error) {
	left, err := p.conjunction(field, depth)
	for err == nil && p.index < len(p.tokens) && p.tokens[p.index] == "OR" {
		p.index++
		var right string
		right, err = p.conjunction(field, depth)
		left = "(" + left + " OR " + right + ")"
	}
	return left, err
}

func (p *arxivQueryParser) conjunction(field string, depth int) (string, error) {
	left, err := p.atom(field, depth)
	for err == nil && p.index < len(p.tokens) && p.tokens[p.index] != ")" && p.tokens[p.index] != "OR" {
		op := "AND"
		switch p.tokens[p.index] {
		case "AND":
			p.index++
			if p.index < len(p.tokens) && p.tokens[p.index] == "NOT" {
				op = "ANDNOT"
				p.index++
			}
		case "ANDNOT", "NOT":
			op = "ANDNOT"
			p.index++
		}
		right, nextErr := p.atom(field, depth)
		err = nextErr
		left = "(" + left + " " + op + " " + right + ")"
	}
	return left, err
}

func (p *arxivQueryParser) atom(field string, depth int) (string, error) {
	if depth > 64 || p.index >= len(p.tokens) {
		return "", fmt.Errorf("incomplete or deeply nested arXiv expression")
	}
	token := p.tokens[p.index]
	p.index++
	if token == "(" {
		value, err := p.expression(field, depth+1)
		if err != nil {
			return "", err
		}
		if p.index >= len(p.tokens) || p.tokens[p.index] != ")" {
			return "", fmt.Errorf("unclosed arXiv query group")
		}
		p.index++
		return value, nil
	}
	if token == ")" || token == "AND" || token == "OR" || token == "NOT" || token == "ANDNOT" {
		return "", fmt.Errorf("arXiv query expected a term, got %q", token)
	}
	if prefix, rest, ok := strings.Cut(token, ":"); ok {
		switch strings.ToLower(prefix) {
		case "ti", "au", "abs", "co", "jr", "cat", "rn", "id", "all", "submitteddate", "lastupdateddate":
			if rest == "" {
				return p.atom(prefix, depth+1)
			}
			if strings.HasPrefix(rest, "[") {
				parts := []string{rest}
				for !strings.HasSuffix(parts[len(parts)-1], "]") && p.index < len(p.tokens) {
					parts = append(parts, p.tokens[p.index])
					p.index++
				}
				if len(parts) != 3 || parts[1] != "TO" || !strings.HasSuffix(parts[2], "]") {
					return "", fmt.Errorf("invalid arXiv field range")
				}
				return prefix + ":" + strings.Join(parts, " "), nil
			}
			return prefix + ":" + rest, nil
		}
	}
	if strings.HasPrefix(token, "\"") {
		value, err := strconv.Unquote(token)
		if err != nil || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("invalid arXiv quoted term")
		}
		return field + ":" + token, nil
	}
	if strings.Contains(token, ":") {
		token = strconv.Quote(token)
	}
	return field + ":" + token, nil
}
