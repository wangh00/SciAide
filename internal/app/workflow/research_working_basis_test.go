package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResearchWorkingStateExplicitBasisContract(t *testing.T) {
	raw := json.RawMessage(`{"expectedVersion":1,"rationale":"两份摘要结局不一致，先核对任务条件。","basis":{"rationale":["read-one","read-two"]}}`)
	value, err := ParseResearchWorkingStateUpdate(raw)
	if err != nil {
		t.Fatal("original working-note patch cannot explicitly associate successful read receipts", err)
	}
	encoded, _ := json.Marshal(value)
	if !strings.Contains(string(encoded), `"basis":{"rationale":["read-one","read-two"]}`) {
		t.Fatal("selected read identities were dropped")
	}
	if _, err := ParseResearchWorkingStateUpdate(json.RawMessage(`{"expectedVersion":1,"currentFocus":"focus","rationale":"why","nextStep":"inspect","unknowns":[],"basis":{"currentFocus":["r1"],"rationale":["r2"],"nextStep":["r3"],"unknowns":["r4"]}}`)); err != nil {
		t.Fatal("bounded four-field associations rejected", err)
	}
	for name, raw := range map[string]string{
		"omitted field":   `{"expectedVersion":1,"nextStep":"inspect","basis":{"rationale":["read"]}}`,
		"unknown field":   `{"expectedVersion":1,"rationale":"why","basis":{"claim":["read"]}}`,
		"empty map":       `{"expectedVersion":1,"rationale":"why","basis":{}}`,
		"empty list":      `{"expectedVersion":1,"rationale":"why","basis":{"rationale":[]}}`,
		"null map":        `{"expectedVersion":1,"rationale":"why","basis":null}`,
		"null list":       `{"expectedVersion":1,"rationale":"why","basis":{"rationale":null}}`,
		"repeated id":     `{"expectedVersion":1,"rationale":"why","basis":{"rationale":["read","read"]}}`,
		"space id":        `{"expectedVersion":1,"rationale":"why","basis":{"rationale":[" read"]}}`,
		"empty id":        `{"expectedVersion":1,"rationale":"why","basis":{"rationale":[""]}}`,
		"long id":         `{"expectedVersion":1,"rationale":"why","basis":{"rationale":["` + strings.Repeat("a", 101) + `"]}}`,
		"three per field": `{"expectedVersion":1,"rationale":"why","basis":{"rationale":["r1","r2","r3"]}}`,
		"five per patch":  `{"expectedVersion":1,"currentFocus":"focus","rationale":"why","nextStep":"inspect","basis":{"currentFocus":["r1","r2"],"rationale":["r3","r4"],"nextStep":["r5"]}}`,
		"duplicate map":   `{"expectedVersion":1,"rationale":"why","basis":{"rationale":["r1"],"rationale":["r2"]}}`,
		"over wire cap":   `{"expectedVersion":1,"rationale":"` + strings.Repeat("界", 690) + `","basis":{"rationale":["read"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResearchWorkingStateUpdate(json.RawMessage(raw)); err == nil {
				t.Fatal("invalid relation accepted", raw)
			}
		})
	}
}

func TestResearchWorkingAbstractPageContract(t *testing.T) {
	page := ResearchWorkingAbstractPage{Status: "unverified_discovery_field", SnapshotView: "preferred_record", QueryID: "q", CandidateID: "c", Field: "abstract", SHA256: strings.Repeat("a", 64), Available: true, Offset: 2, NextOffset: 4, TotalRunes: 6, Content: "文🧪"}
	raw, _ := json.Marshal(page)
	if _, err := ParseResearchWorkingAbstractPage(raw); err != nil {
		t.Fatal(err)
	}
	if err := ValidateResearchWorkingAbstractSelector(json.RawMessage(`{"queryId":"q","candidateId":"c","offset":2,"limit":2}`), page); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ResearchWorkingAbstractPage){
		"title":      func(p *ResearchWorkingAbstractPage) { p.Field = "title" },
		"index":      func(p *ResearchWorkingAbstractPage) { p.Status = "unverified_discovery_index" },
		"missing":    func(p *ResearchWorkingAbstractPage) { p.Available = false },
		"empty":      func(p *ResearchWorkingAbstractPage) { p.Content = "" },
		"bad cursor": func(p *ResearchWorkingAbstractPage) { p.NextOffset = 5 },
		"outside":    func(p *ResearchWorkingAbstractPage) { p.Offset = 7 },
	} {
		t.Run(name, func(t *testing.T) {
			p := page
			mutate(&p)
			raw, _ := json.Marshal(p)
			if _, err := ParseResearchWorkingAbstractPage(raw); err == nil {
				t.Fatal("non-page accepted")
			}
		})
	}
	for _, args := range []string{`{"queryId":"other","candidateId":"c","offset":2}`, `{"queryId":"q","candidateId":"c","offset":0}`, `{"queryId":"q","candidateId":"c","offset":2,"field":"title"}`, `{"queryId":"q","candidateId":"c","offset":2,"limit":1}`, `{"queryId":"q","candidateId":"c","offset":2,"unknown":1}`, `{"queryId":"q","queryId":"q","candidateId":"c","offset":2}`} {
		if err := ValidateResearchWorkingAbstractSelector(json.RawMessage(args), page); err == nil {
			t.Fatal("different selector accepted", args)
		}
	}
}

func TestResearchWorkingAbstractReceiptRejectsAmbiguousOrIncompletePages(t *testing.T) {
	p := ResearchWorkingAbstractPage{Status: "unverified_discovery_field", SnapshotView: "preferred_record", QueryID: "q", CandidateID: "c", Field: "abstract", Available: true, TotalRunes: 2, NextOffset: -1, Content: "文🧪"}
	raw, _ := json.Marshal(p)
	good := `{"status":"success","toolCallId":"read","truncated":false,"structured":` + string(raw) + `}`
	id, provided, err := ParseResearchWorkingAbstractReceipt(json.RawMessage(good))
	if err != nil || id != "read" || provided != p {
		t.Fatal("host receipt rejected", id, provided, err)
	}
	for _, bad := range []string{`{}`, strings.Replace(good, `"success"`, `"error"`, 1), strings.Replace(good, `"truncated":false`, `"truncated":true`, 1), strings.Replace(good, `"toolCallId":"read"`, `"toolCallId":"other","toolCallId":"read"`, 1), strings.Replace(good, `"field":"abstract"`, `"field":"title"`, 1)} {
		if _, _, err := ParseResearchWorkingAbstractReceipt(json.RawMessage(bad)); err == nil {
			t.Fatal("ambiguous/incomplete receipt accepted", bad)
		}
	}
}
