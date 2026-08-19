package document

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const textChunkRunes = 32_000

func parseText(ctx context.Context, path string, format Format) (Parsed, error) {
	file, err := os.Open(path)
	if err != nil {
		return Parsed{}, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	collect := collector{}
	var chunk strings.Builder
	startLine, line := 1, 0
	flush := func(endLine int) bool {
		if chunk.Len() == 0 {
			return true
		}
		kind := "text"
		locator := fmt.Sprintf("lines:%d-%d", startLine, endLine)
		if format == FormatCSV {
			kind = "rows"
			locator = fmt.Sprintf("rows:%d-%d", startLine, endLine)
		}
		ok := collect.add(kind, locator, "", chunk.String())
		chunk.Reset()
		startLine = endLine + 1
		return ok
	}
	for {
		if err := ctx.Err(); err != nil {
			return Parsed{}, err
		}
		value, readErr := reader.ReadString('\n')
		if value != "" {
			line++
			if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
				return Parsed{}, fmt.Errorf("document is not valid UTF-8 text")
			}
			chunk.WriteString(value)
			if len([]rune(chunk.String())) >= textChunkRunes && !flush(line) {
				break
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return Parsed{}, readErr
			}
			break
		}
	}
	if line == 0 {
		line = 1
	}
	flush(line)
	return Parsed{Units: collect.units, Truncated: collect.truncated, ExtractedRunes: collect.runes}, nil
}
