// Package researchtext normalizes source markup without treating statistical
// inequalities as tags. It is not an HTML sanitizer for rendered output.
package researchtext

import (
	"html"
	"regexp"
	"strings"
)

var tags = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:._-]*(?:\s+[A-Za-z_:][A-Za-z0-9:._-]*(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>` + "`" + `]+))?)*\s*/?>`)
var comments = regexp.MustCompile(`(?s)<!--.*?-->`)
var legacyTags = regexp.MustCompile(`<[^>]+>`)

func Plain(value string) string {
	value = comments.ReplaceAllString(value, " ")
	return collapse(tags.ReplaceAllString(value, " "))
}

// Legacy exists only to recognize damaged persisted abstracts exactly. Never
// use it for new source content or infer missing text without its raw snapshot.
func Legacy(value string) string { return collapse(legacyTags.ReplaceAllString(value, " ")) }

func collapse(value string) string {
	return strings.Join(strings.Fields(html.UnescapeString(value)), " ")
}
