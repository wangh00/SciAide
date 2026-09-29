package connectors

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	appresearch "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/researchtext"
)

var yearPattern = regexp.MustCompile(`(?:^|[^0-9])((?:18|19|20|21)[0-9]{2})(?:[^0-9]|$)`)

func rawSnapshot(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > appresearch.MaxRawSnapshotBytes {
		return nil
	}
	return encoded
}

func plainText(value string) string {
	return researchtext.Plain(value)
}

func yearFrom(value string) int {
	match := yearPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 2 {
		return 0
	}
	year, _ := strconv.Atoi(match[1])
	return year
}

func dateString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006 Jan 02", "2006 Jan", "2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			if layout == "2006" {
				return parsed.Format("2006")
			}
			return parsed.Format("2006-01-02")
		}
	}
	return value
}

func invertedAbstract(index map[string][]int) string {
	if len(index) == 0 {
		return ""
	}
	maximum := -1
	for _, positions := range index {
		for _, position := range positions {
			if position > maximum && position < 100_000 {
				maximum = position
			}
		}
	}
	if maximum < 0 {
		return ""
	}
	words := make([]string, maximum+1)
	keys := make([]string, 0, len(index))
	for word := range index {
		keys = append(keys, word)
	}
	sort.Strings(keys)
	for _, word := range keys {
		for _, position := range index[word] {
			if position >= 0 && position < len(words) && words[position] == "" {
				words[position] = word
			}
		}
	}
	return strings.Join(strings.Fields(strings.Join(words, " ")), " ")
}

func splitAuthorString(value string) []appresearch.Author {
	value = strings.TrimSpace(value)
	if value == "" {
		return []appresearch.Author{}
	}
	parts := strings.FieldsFunc(value, func(character rune) bool { return character == ';' })
	if len(parts) == 1 && strings.Count(value, ",") > 1 {
		parts = strings.Split(value, ",")
	}
	result := make([]appresearch.Author, 0, len(parts))
	for _, part := range parts {
		if name := strings.TrimSpace(part); name != "" {
			result = append(result, appresearch.Author{Name: name})
		}
	}
	return result
}

func endpointHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
