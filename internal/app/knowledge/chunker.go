package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"github.com/wangh00/SciAide/internal/document"
)

const (
	targetChunkRunes  = 1_200
	maximumChunkRunes = 1_600
	minimumChunkRunes = 600
	chunkOverlapRunes = 80
)

type textSpan struct {
	Content string
	Start   int
	End     int
}

func buildChunks(documentValue Document, parsed document.Parsed) ([]Chunk, error) {
	if parsed.SchemaVersion != documentValue.ParserSchemaVersion {
		return nil, fmt.Errorf("parsed document schema does not match the active knowledge index")
	}
	chunks := make([]Chunk, 0, len(parsed.Units))
	for _, unit := range parsed.Units {
		if unit.Index <= 0 || strings.TrimSpace(unit.Locator) == "" || strings.TrimSpace(unit.Content) == "" {
			return nil, fmt.Errorf("parsed document contains an invalid source unit")
		}
		for _, span := range splitUnitContent(unit.Content) {
			contentHash := sha256.Sum256([]byte(span.Content))
			contentDigest := hex.EncodeToString(contentHash[:])
			identity := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00%s", documentValue.ID, documentValue.ChunkingVersion, unit.Index, unit.Locator, span.Start, span.End, contentDigest)))
			chunks = append(chunks, Chunk{
				ID: hex.EncodeToString(identity[:]), DocumentID: documentValue.ID, AttachmentID: documentValue.AttachmentID,
				Ordinal: len(chunks) + 1, UnitIndex: unit.Index, Kind: unit.Kind, Locator: unit.Locator,
				Title: strings.TrimSpace(unit.Title), Content: span.Content, ContentSHA256: contentDigest,
				SourceStart: span.Start, SourceEnd: span.End,
			})
		}
	}
	return chunks, nil
}

func splitUnitContent(value string) []textSpan {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= maximumChunkRunes {
		return []textSpan{{Content: string(runes), Start: 0, End: len(runes)}}
	}
	result := make([]textSpan, 0, (len(runes)/targetChunkRunes)+1)
	start := 0
	for start < len(runes) {
		hardEnd := min(len(runes), start+maximumChunkRunes)
		end := hardEnd
		if hardEnd < len(runes) {
			target := min(hardEnd, start+targetChunkRunes)
			end = chooseChunkEnd(runes, start, target, hardEnd)
		}
		contentStart, contentEnd := trimSpan(runes, start, end)
		if contentEnd > contentStart {
			result = append(result, textSpan{Content: string(runes[contentStart:contentEnd]), Start: contentStart, End: contentEnd})
		}
		if end >= len(runes) {
			break
		}
		next := max(start+1, end-chunkOverlapRunes)
		for next < end && unicode.IsSpace(runes[next]) {
			next++
		}
		start = next
	}
	return result
}

func chooseChunkEnd(value []rune, start, target, hardEnd int) int {
	for index := target; index < hardEnd; index++ {
		if strongBoundary(value[index]) {
			return index + 1
		}
	}
	minimum := min(target, start+minimumChunkRunes)
	for index := target - 1; index >= minimum; index-- {
		if strongBoundary(value[index]) {
			return index + 1
		}
	}
	for index := target; index < hardEnd; index++ {
		if unicode.IsSpace(value[index]) {
			return index
		}
	}
	for index := target - 1; index >= minimum; index-- {
		if unicode.IsSpace(value[index]) {
			return index
		}
	}
	return hardEnd
}

func strongBoundary(value rune) bool {
	switch value {
	case '\n', '\r', '.', '!', '?', ';', '。', '！', '？', '；':
		return true
	default:
		return false
	}
}

func trimSpan(value []rune, start, end int) (int, int) {
	for start < end && unicode.IsSpace(value[start]) {
		start++
	}
	for end > start && unicode.IsSpace(value[end-1]) {
		end--
	}
	return start, end
}

func normalizedTerms(value string) []string {
	runes := []rune(value)
	result := make([]string, 0, len(runes)/2)
	for index := 0; index < len(runes); {
		if unicode.Is(unicode.Han, runes[index]) {
			end := index + 1
			for end < len(runes) && unicode.Is(unicode.Han, runes[end]) {
				end++
			}
			sequence := runes[index:end]
			if len(sequence) == 1 {
				result = append(result, string(sequence))
			} else {
				for offset := 0; offset+1 < len(sequence); offset++ {
					result = append(result, string(sequence[offset:offset+2]))
				}
			}
			index = end
			continue
		}
		if unicode.IsLetter(runes[index]) || unicode.IsDigit(runes[index]) {
			end := index + 1
			for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end])) && !unicode.Is(unicode.Han, runes[end]) {
				end++
			}
			result = append(result, strings.ToLower(string(runes[index:end])))
			index = end
			continue
		}
		index++
	}
	return result
}

func uniqueQueryTerms(value string) []string {
	all := normalizedTerms(value)
	result := make([]string, 0, min(len(all), 64))
	seen := make(map[string]struct{}, len(all))
	for _, term := range all {
		if term == "" {
			continue
		}
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		result = append(result, term)
		if len(result) == 64 {
			break
		}
	}
	return result
}

func ftsQuery(terms []string) string {
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}
