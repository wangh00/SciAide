package research

import "strings"

// FilterCandidatePage is the legacy fallback. New query snapshots are paged in SQL.
func FilterCandidatePage(values []Candidate, c CandidateListCommand) CandidatePage {
	filtered := []Candidate{}
	for _, value := range values {
		if c.Status != "" && value.ReviewStatus != c.Status {
			continue
		}
		if text := normalizeTitle(c.Search); text != "" && !strings.Contains(normalizeTitle(value.Preferred.Title+" "+authorNames(value.Preferred.Authors)+" "+value.Preferred.Venue), text) {
			continue
		}
		filtered = append(filtered, value)
	}
	sortCandidates(filtered, c.Sort)
	page := CandidatePage{Items: []Candidate{}, Total: len(filtered), Offset: c.Offset, Limit: c.Limit}
	if c.Offset < len(filtered) {
		page.Items = filtered[c.Offset:min(c.Offset+c.Limit, len(filtered))]
	}
	return page
}
