package workflow

import (
	research "github.com/wangh00/SciAide/internal/app/research"
	"sort"
)

// Opt-in for newly captured identity-aware records only. Old source hashes
// retain their original byte contract. Local IDs are navigation, not content.
const LiteratureCandidateDigestVersion = "literature-candidate-content-v1"

func literatureCandidateSHA256(c literatureCandidate) string {
	if c.SourceSHA256Version != LiteratureCandidateDigestVersion {
		return hashJSON(mustJSON(c))
	}
	c.ID = ""
	c.SourceSHA256 = ""
	if c.RecordIdentity != nil {
		h := *c.RecordIdentity
		h.Relations = append([]research.RecordRelationHint(nil), h.Relations...)
		for i := range h.Relations {
			h.Relations[i].TargetCandidateID = ""
		}
		c.RecordIdentity = &h
	}
	return hashJSON(mustJSON(c))
}

// Shared across every synthesis level. Bounded navigation is not evidence,
// and does not expand the current batch's permitted assessment identities.
func literatureIdentityNavigation(records []literatureEvidenceRecord) any {
	type entry struct {
		CandidateID string                           `json:"candidateId"`
		Identity    *research.RecordIdentityOverview `json:"identity"`
	}
	items := []entry{}
	for _, r := range records {
		if r.RecordIdentity != nil && (len(r.RecordIdentity.Roles) > 0 || len(r.RecordIdentity.Relations) > 0) {
			items = append(items, entry{r.CandidateID, research.ImportRecordIdentity(r.RecordIdentity)})
		}
	}
	if len(items) == 0 {
		return nil
	}
	priority := func(e entry) int {
		p := 99
		for _, k := range e.Identity.RelationKinds {
			if n := research.IdentityHintPriority(k); n < p {
				p = n
			}
		}
		for _, k := range e.Identity.RoleKinds {
			if n := research.IdentityHintPriority(k); n < p {
				p = n
			}
		}
		return p
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := priority(items[i]), priority(items[j])
		if a != b {
			return a < b
		}
		return items[i].CandidateID < items[j].CandidateID
	})
	total := len(items)
	if len(items) > 16 {
		items = items[:16]
	}
	return map[string]any{"items": items, "totalCandidatesWithHints": total, "retainedCandidates": len(items), "truncated": total > len(items), "interpretation": "跨分组待核实导航，不是原文或独立研究数，不扩展本批assessment允许的候选。统计按保存scope，不跨查询去重；有界清单内容哈希可用本运行已发queryIDs重建，原始题录保留。"}
}
