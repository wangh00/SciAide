package bootstrap

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
)

// Only the current provider-visible input/results are inspected. No database,
// stored source or hidden fixture oracle supplies source text to this adapter.
func citationFixtureSourceSelections(t *testing.T, prompt string, result map[string]any) json.RawMessage {
	t.Helper()
	const open, close = "<citation_submission_sources>", "</citation_submission_sources>"
	at := strings.LastIndex(prompt, open)
	if at < 0 {
		t.Fatal("missing citation navigation")
	}
	tail := prompt[at+len(open):]
	end := strings.Index(tail, close)
	if end < 0 {
		t.Fatal("missing navigation end")
	}
	var menu struct {
		Sources []struct {
			Reference, QuoteSHA256 string
			QuoteCharacters        int
			Segments               []struct {
				ID         string
				Start, End int
			}
		}
	}
	if err := json.Unmarshal([]byte(tail[:end]), &menu); err != nil {
		t.Fatal(err)
	}
	sources := map[string]struct {
		quote    string
		segments []struct {
			ID         string
			Start, End int
		}
	}{}
	input := workflowAITestStageInput(prompt)
	// Collect JSON string values in the ACTUAL request (e.g. reader result.text).
	stringsInView := []string{}
	for i := 0; i < len(prompt); i++ {
		if prompt[i] != '"' {
			continue
		}
		var value string
		d := json.NewDecoder(strings.NewReader(prompt[i:]))
		if d.Decode(&value) == nil {
			stringsInView = append(stringsInView, value)
			i += int(d.InputOffset()) - 1
		}
	}
	for _, row := range menu.Sources {
		quote := ""
		if candidates, ok := input["candidates"].([]any); ok {
			for _, v := range candidates {
				c, _ := v.(map[string]any)
				if c["reference"] == row.Reference {
					quote, _ = c["quote"].(string)
				}
			}
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(quote))) != row.QuoteSHA256 {
			quote = ""
			for _, text := range stringsInView {
				pos := strings.Index(text, row.Reference)
				if pos < 0 {
					continue
				}
				line := strings.Index(text[pos:], "\n")
				if line < 0 {
					continue
				}
				runes := []rune(text[pos+line+1:])
				if len(runes) < row.QuoteCharacters {
					continue
				}
				candidate := string(runes[:row.QuoteCharacters])
				if fmt.Sprintf("%x", sha256.Sum256([]byte(candidate))) == row.QuoteSHA256 {
					quote = candidate
					break
				}
			}
		}
		if quote != "" {
			sources[row.Reference] = struct {
				quote    string
				segments []struct {
					ID         string
					Start, End int
				}
			}{quote, row.Segments}
		}
	}
	rows, _ := result["citationAssessments"].([]any)
	for _, value := range rows {
		row := value.(map[string]any)
		reference, _ := row["reference"].(string)
		wanted, _ := row["supportingQuote"].(string)
		ids := []string{}
		selection := map[string]any{"sourceSegmentIds": ids}
		if row["decision"] != "exclude" || wanted != "" {
			source, ok := sources[reference]
			if !ok {
				t.Fatalf("source text not visible to provider for %s", reference)
			}
			at := strings.Index(source.quote, wanted)
			if wanted == "" || at < 0 {
				t.Fatal("scripted supportingQuote not in current source")
			}
			a, b := utf8.RuneCountInString(source.quote[:at]), utf8.RuneCountInString(source.quote[:at])+utf8.RuneCountInString(wanted)
			for _, segment := range source.segments {
				if segment.Start < b && segment.End > a {
					ids = append(ids, segment.ID)
				}
			}
			if len(ids) == 0 {
				t.Fatal("no continuous source range")
			}
			selection = map[string]any{"sourceSegmentIds": ids, "startOffset": a, "endOffset": b}
		}
		delete(row, "supportingQuote")
		row["quoteSelection"] = selection
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// The legacy indexed fixture needs roles/results; normalize only this test's
// wire without changing production adapters or other fixture expectations.
func citationFixtureCanonicalRequest(t *testing.T, body []byte, protocol modelprofile.APIProtocol) []byte {
	t.Helper()
	if protocol == modelprofile.ProtocolOpenAIChat {
		return body
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		t.Fatal("bad actual request")
	}
	var canonical map[string]any
	_ = json.Unmarshal(contextPipelineCanonicalRequest(t, body), &canonical)
	messages := []any{}
	text := func(v json.RawMessage) string {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
		var parts []struct{ Text string }
		_ = json.Unmarshal(v, &parts)
		for _, p := range parts {
			s += p.Text
		}
		return s
	}
	if protocol == modelprofile.ProtocolOpenAIResponses {
		messages = append(messages, map[string]any{"role": "system", "content": text(raw["instructions"])})
		var input []struct {
			Type, Role      string
			Content, Output json.RawMessage
		}
		_ = json.Unmarshal(raw["input"], &input)
		for _, v := range input {
			if v.Type == "function_call_output" {
				messages = append(messages, map[string]any{"role": "tool", "content": text(v.Output)})
			} else if len(v.Content) > 0 {
				messages = append(messages, map[string]any{"role": v.Role, "content": text(v.Content)})
			}
		}
	} else {
		messages = append(messages, map[string]any{"role": "system", "content": text(raw["system"])})
		var input []struct {
			Role    string
			Content json.RawMessage
		}
		_ = json.Unmarshal(raw["messages"], &input)
		for _, v := range input {
			var plain string
			if json.Unmarshal(v.Content, &plain) == nil {
				messages = append(messages, map[string]any{"role": v.Role, "content": plain})
				continue
			}
			var parts []struct {
				Type, Text string
				Content    json.RawMessage
			}
			_ = json.Unmarshal(v.Content, &parts)
			for _, p := range parts {
				if p.Type == "tool_result" {
					messages = append(messages, map[string]any{"role": "tool", "content": text(p.Content)})
				} else if p.Type == "text" {
					messages = append(messages, map[string]any{"role": v.Role, "content": p.Text})
				}
			}
		}
	}
	canonical["messages"] = messages
	encoded, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
