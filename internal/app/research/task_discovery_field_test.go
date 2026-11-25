package research

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSavedDiscoveryFieldPagesPreserveUnicodeAndEscaping(t *testing.T) {
	for _, size := range []int{0, 1, 400, 50000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			pattern := []rune("A🙂中\"\n\x00<&é")
			value := make([]rune, size)
			for i := range value {
				value[i] = pattern[i%len(pattern)]
			}
			text := string(value)
			candidate := Candidate{ID: "candidate", Preferred: Work{SourceID: "eric", SourceRecordID: "EJ1", Title: text, Abstract: text}}
			for _, field := range []string{"title", "abstract"} {
				offset := 0
				var copied strings.Builder
				for pageCount := 0; ; pageCount++ {
					if pageCount > size+1 {
						t.Fatal("saved field page did not advance")
					}
					raw, err := discoveryFieldPage(candidate, DiscoveryFieldCommand{QueryID: "q", CandidateID: candidate.ID, Field: field, Offset: offset, Limit: 400})
					if err != nil || len(raw) > MaxDiscoveryPageBytes || !json.Valid(raw) {
						t.Fatal("invalid or oversized field page", len(raw), err)
					}
					var page struct {
						Status, Content, SHA256        string
						Offset, NextOffset, TotalRunes int
						Available                      bool
					}
					if json.Unmarshal(raw, &page) != nil || page.Status != "unverified_discovery_field" || page.Offset != offset || page.TotalRunes != size || page.SHA256 != hashText(text) || page.Available != (size > 0) {
						t.Fatal("page changed source identity", string(raw))
					}
					copied.WriteString(page.Content)
					if page.NextOffset < 0 {
						break
					}
					if page.NextOffset != offset+len([]rune(page.Content)) || page.NextOffset <= offset || !strings.HasPrefix(text, copied.String()) {
						t.Fatal("page skipped unseen source characters")
					}
					offset = page.NextOffset
				}
				if copied.String() != text {
					t.Fatal("saved field did not reconstruct original bytes")
				}
			}
		})
	}
	candidate := Candidate{Preferred: Work{Abstract: "abc"}}
	if _, err := discoveryFieldPage(candidate, DiscoveryFieldCommand{Field: "abstract", Offset: 4, Limit: 1}); err == nil {
		t.Fatal("out-of-range field offset accepted")
	}
	candidate.Preferred.SourceRecordID = strings.Repeat("<", 500)
	if _, err := discoveryFieldPage(candidate, DiscoveryFieldCommand{Field: "abstract", Limit: 400}); err == nil {
		t.Fatal("oversized identity was silently clipped")
	}
}
