package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"sort"
	"strings"
	"unicode/utf8"
)

// Hints describe catalogue records, never verified study identities. They are
// computed only when a new query is frozen, not backfilled into old snapshots.
// Each scope saves a bounded witness inventory, detected totals and stream SHA,
// not an unbounded pair list. Multi-query views union these scoped inventories,
// NOT the set of all possible relations. SHA excludes local candidate IDs.
const RecordIdentityVersion = "record-identity-hints-v1"
const identityWitnessPerKind = 2
const identityWitnessLimit = 10
const RecordIdentityInstruction = "recordIdentity 是宿主按已保存题录生成的待核实线索，不是文献原文、同文确认、科学纳入或独立研究计数。possible_same_work/possible_version 不能自动合并；identifier_conflict 须核对原标识，不能被题名相似覆盖。possible_companion 只定位可能主文；补充材料、数据、方案、预印本和学位论文仍可有资料价值，不因提示自动排除，也不能直接各计一项独立研究。没有提示不证明独立；独立研究数仍需试验/样本/注册号等身份核验。关系目标可在本批外，仅用于导航，本批判断仍只覆盖本批候选。每个scope仅保留有限见证及全部派生线索计数/内容哈希，原始题录完整保留。多查询统计不跨scope去重，不是完整关系并集。视图SHA标识可由已发queryIDs重建的有界清单内容，排除本地候选ID。"

// Identity overview is an explicitly lossy handoff, not another source record.
// Full source records and saved scoped inventories are reconstructible from the
// issued queryIDs. Preview.SHA256 hashes their bounded content, not a query JSON.
type RecordIdentityOverview struct {
	Version             string                 `json:"version"`
	RoleKinds           []string               `json:"roleKinds,omitempty"`
	RelationKinds       []string               `json:"relationKinds,omitempty"`
	RelatedCandidateIDs []string               `json:"relatedCandidateIds,omitempty"`
	Preview             *RecordIdentityPreview `json:"preview"`
}

func ImportRecordIdentity(v *RecordIdentity) *RecordIdentityOverview {
	if v == nil {
		return nil
	}
	preview := PreviewRecordIdentity(v)
	out := &RecordIdentityOverview{Version: v.Version, Preview: preview.Preview}
	add := func(values []string, s string) []string {
		for _, v := range values {
			if v == s {
				return values
			}
		}
		if len(values) < 5 {
			return append(values, s)
		}
		return values
	}
	for _, r := range v.Roles {
		out.RoleKinds = add(out.RoleKinds, r.Kind)
	}
	for kind := range preview.Preview.RoleKinds {
		out.RoleKinds = add(out.RoleKinds, kind)
	}
	for _, r := range v.Relations {
		out.RelationKinds = add(out.RelationKinds, r.Kind)
		if len(out.RelatedCandidateIDs) < 2 {
			out.RelatedCandidateIDs = add(out.RelatedCandidateIDs, r.TargetCandidateID)
		}
	}
	for kind := range preview.Preview.RelationKinds {
		out.RelationKinds = add(out.RelationKinds, kind)
	}
	sort.Slice(out.RoleKinds, func(i, j int) bool {
		return IdentityHintPriority(out.RoleKinds[i]) < IdentityHintPriority(out.RoleKinds[j])
	})
	sort.Slice(out.RelationKinds, func(i, j int) bool {
		return IdentityHintPriority(out.RelationKinds[i]) < IdentityHintPriority(out.RelationKinds[j])
	})
	// Refs/bases are deliberately omitted even when every kind fits.
	if out.Preview != nil {
		p := *out.Preview
		p.RoleKinds = nil
		p.RelationKinds = nil
		p.Truncated = len(v.Roles) > 0 || len(v.Relations) > 0 || p.Truncated
		out.Preview = &p
	}
	return out
}

type IdentityRecordRef struct {
	SourceID       string `json:"sourceId"`
	SourceRecordID string `json:"sourceRecordId"`
	WorkSHA256     string `json:"workSha256"`
}
type RecordRoleHint struct {
	ScopeSHA256 string            `json:"scopeSha256"`
	Record      IdentityRecordRef `json:"record"`
	Kind        string            `json:"kind"`
	Field       string            `json:"field"`
}
type RecordRelationHint struct {
	ScopeSHA256       string              `json:"scopeSha256"`
	Kind              string              `json:"kind"`
	TargetCandidateID string              `json:"targetCandidateId"`
	Records           []IdentityRecordRef `json:"records"`
	Basis             []string            `json:"basis"`
}
type RecordIdentityScope struct {
	SubjectRecordCount int            `json:"subjectRecordCount"`
	SubjectSHA256      string         `json:"subjectSha256"`
	RoleKinds          map[string]int `json:"roleKinds,omitempty"`
	RelationKinds      map[string]int `json:"relationKinds,omitempty"`
	Complete           bool           `json:"complete"`
	InputRecordCount   int            `json:"inputRecordCount"`
	InputSHA256        string         `json:"inputSha256"`
	ComparedPairs      int            `json:"comparedPairs"`
	RoleCount          int            `json:"roleCount"`
	RelationCount      int            `json:"relationCount"`
	RoleSHA256         string         `json:"roleSha256"`
	RelationSHA256     string         `json:"relationSha256"`
	OmittedRoles       int            `json:"omittedRoles"`
	OmittedRelations   int            `json:"omittedRelations"`
}
type RecordIdentityPreview struct {
	RoleKinds     map[string]int `json:"roleKinds,omitempty"`
	RelationKinds map[string]int `json:"relationKinds,omitempty"`
	ScopeCount    int            `json:"scopeCount"`
	SHA256        string         `json:"sha256"`
	RoleCount     int            `json:"roleCount"`
	RelationCount int            `json:"relationCount"`
	Truncated     bool           `json:"truncated"`
}
type RecordIdentity struct {
	Version   string                 `json:"version"`
	Scopes    []RecordIdentityScope  `json:"scopes,omitempty"`
	Roles     []RecordRoleHint       `json:"roles,omitempty"`
	Relations []RecordRelationHint   `json:"relations,omitempty"`
	Preview   *RecordIdentityPreview `json:"preview,omitempty"`
}

// Content SHA is portable across archive ID remapping. It hashes the complete
// saved bounded inventories, not the omitted relations or any single query JSON.
func RecordIdentityContentSHA256(v *RecordIdentity) string {
	if v == nil {
		return ""
	}
	out := *v
	out.Preview = nil
	out.Relations = append([]RecordRelationHint(nil), v.Relations...)
	for i := range out.Relations {
		out.Relations[i].TargetCandidateID = ""
	}
	b, _ := json.Marshal(out)
	return hashText(string(b))
}

// Lower is higher priority; counts for every detected kind remain visible
// even when only two provenance witnesses fit a downstream preview.
func IdentityHintPriority(kind string) int {
	switch kind {
	case "identifier_conflict":
		return 0
	case "possible_companion", "companion_material":
		return 1
	case "possible_version", "preprint_or_working_paper":
		return 2
	case "shared_identifier", "dataset":
		return 3
	case "possible_same_work", "protocol":
		return 4
	case "thesis":
		return 5
	default:
		return 6
	}
}
func PreviewRecordIdentity(v *RecordIdentity) *RecordIdentity {
	if v == nil || v.Preview != nil {
		return v
	}
	out := *v
	info := &RecordIdentityPreview{SHA256: RecordIdentityContentSHA256(v), ScopeCount: len(v.Scopes), RoleKinds: map[string]int{}, RelationKinds: map[string]int{}}
	for _, scope := range v.Scopes {
		for k, n := range scope.RoleKinds {
			info.RoleKinds[k] += n
		}
		for k, n := range scope.RelationKinds {
			info.RelationKinds[k] += n
		}
		info.RoleCount += scope.RoleCount
		info.RelationCount += scope.RelationCount
		info.Truncated = info.Truncated || scope.OmittedRoles > 0 || scope.OmittedRelations > 0
	}
	if len(v.Scopes) == 0 {
		info.RoleCount = len(v.Roles)
		info.RelationCount = len(v.Relations)
		for _, h := range v.Roles {
			info.RoleKinds[h.Kind]++
		}
		for _, h := range v.Relations {
			info.RelationKinds[h.Kind]++
		}
	}
	info.Truncated = info.Truncated || len(v.Roles) > 2 || len(v.Relations) > 2 || len(v.Scopes) > 2
	if len(out.Roles) > 2 {
		out.Roles = out.Roles[:2]
	}
	if len(out.Relations) > 2 {
		out.Relations = out.Relations[:2]
	}
	if len(out.Scopes) > 2 {
		out.Scopes = out.Scopes[:2]
	}
	out.Preview = info
	return &out
}

// Merge issued full snapshots, retaining distinct source versions and all
// their bounded, explicitly scoped inventories (not a complete pair union).
// Never infer a missing historic relation from today's metadata.
func MergeRecordIdentities(a, b *RecordIdentity) *RecordIdentity {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if a.Version != RecordIdentityVersion || b.Version != RecordIdentityVersion || a.Preview != nil || b.Preview != nil {
		return &RecordIdentity{Version: "unsupported-identity-inventory"}
	}
	return canonicalRecordIdentity(&RecordIdentity{Scopes: append(append([]RecordIdentityScope(nil), a.Scopes...), b.Scopes...), Version: RecordIdentityVersion, Roles: append(append([]RecordRoleHint(nil), a.Roles...), b.Roles...), Relations: append(append([]RecordRelationHint(nil), a.Relations...), b.Relations...)})
}
func canonicalRecordIdentity(v *RecordIdentity) *RecordIdentity {
	// Sorted JSON keys also make unions order-independent and reproducible.
	roles, relations := map[string]RecordRoleHint{}, map[string]RecordRelationHint{}
	scopes := map[string]RecordIdentityScope{}
	for _, scope := range v.Scopes {
		b, _ := json.Marshal(scope)
		scopes[string(b)] = scope
	}
	v.Scopes = nil
	scopeKeys := make([]string, 0, len(scopes))
	for k := range scopes {
		scopeKeys = append(scopeKeys, k)
	}
	sort.Strings(scopeKeys)
	for _, k := range scopeKeys {
		v.Scopes = append(v.Scopes, scopes[k])
	}
	for _, h := range v.Roles {
		b, _ := json.Marshal(h)
		roles[string(b)] = h
	}
	for _, h := range v.Relations {
		key := h
		key.TargetCandidateID = ""
		b, _ := json.Marshal(key)
		relations[string(b)] = h
	}
	v.Roles = nil
	v.Relations = nil
	keys := make([]string, 0, len(roles))
	for k := range roles {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := IdentityHintPriority(roles[keys[i]].Kind), IdentityHintPriority(roles[keys[j]].Kind)
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		v.Roles = append(v.Roles, roles[k])
	}
	keys = keys[:0]
	for k := range relations {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := IdentityHintPriority(relations[keys[i]].Kind), IdentityHintPriority(relations[keys[j]].Kind)
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		v.Relations = append(v.Relations, relations[k])
	}
	return v
}

type identityFeatures struct {
	aliases                             []CandidateAlias
	candidate                           int
	ref                                 IdentityRecordRef
	work                                Work
	title, abstract, authors, companion string
	titleWords, abstractShingles        map[string]bool
	preprint                            bool
}

// AnnotateRecordIdentities operates on a complete newly captured query, before
// pagination. No candidate ID, source membership or review decision is changed.
func AnnotateRecordIdentities(values []Candidate) {
	_ = AnnotateRecordIdentitiesContext(context.Background(), values)
}
func AnnotateRecordIdentitiesContext(ctx context.Context, values []Candidate) error {
	features := []identityFeatures{}
	for i := range values {
		if err := ctx.Err(); err != nil {
			return err
		}
		values[i].RecordIdentity = &RecordIdentity{Version: RecordIdentityVersion}
		seen := map[string]bool{}
		for _, r := range values[i].Records {
			if err := ctx.Err(); err != nil {
				return err
			}
			w := r.Work
			w.RawSnapshot = nil
			w.SourceMetadataPreview = nil
			b, _ := json.Marshal(w)
			ref := IdentityRecordRef{SourceID: w.SourceID, SourceRecordID: w.SourceRecordID, WorkSHA256: hashText(string(b))}
			key := ref.SourceID + "\x00" + ref.SourceRecordID + "\x00" + ref.WorkSHA256
			if seen[key] {
				continue
			}
			seen[key] = true
			f := identityFeatures{candidate: i, ref: ref, work: w, title: normalizeTitle(w.Title), abstract: normalizeTitle(w.Abstract), authors: identityAuthors(w.Authors)}
			f.aliases = CandidateAliases(w)
			f.titleWords = identityWords(f.title, 1)
			f.abstractShingles = identityWords(f.abstract, 5)
			typ := normalizeTitle(w.WorkType)
			kind := ""
			switch typ {
			case "preprint", "pre print", "working paper":
				kind = "preprint_or_working_paper"
				f.preprint = true
			case "thesis", "dissertation", "doctoral thesis", "master thesis", "masters thesis":
				kind = "thesis"
			case "dataset", "data set":
				kind = "dataset"
			case "protocol", "study protocol":
				kind = "protocol"
			case "supplement", "supplementary material", "supplemental material":
				kind = "companion_material"
			}
			if kind != "" {
				values[i].RecordIdentity.Roles = append(values[i].RecordIdentity.Roles, RecordRoleHint{Record: ref, Kind: kind, Field: "workType"})
			}
			for _, prefix := range []string{"supplemental files for ", "supplementary files for ", "supplementary materials for ", "supplementary material for ", "supplemental material for ", "supplemental materials for ", "dataset for ", "data for "} {
				if strings.HasPrefix(f.title, prefix) && utf8.RuneCountInString(strings.TrimSpace(strings.TrimPrefix(f.title, prefix))) >= 16 {
					f.companion = strings.TrimSpace(strings.TrimPrefix(f.title, prefix))
					values[i].RecordIdentity.Roles = append(values[i].RecordIdentity.Roles, RecordRoleHint{Record: ref, Kind: "companion_material", Field: "title"})
					break
				}
			}
			features = append(features, f)
		}
	}
	sort.Slice(features, func(i, j int) bool { return identityRefKey(features[i].ref) < identityRefKey(features[j].ref) })
	groups := make([]string, 0, len(values))
	for i := range values {
		refs := []string{}
		for _, f := range features {
			if f.candidate == i {
				refs = append(refs, identityRefKey(f.ref))
			}
		}
		groups = append(groups, strings.Join(refs, "\n"))
	}
	sort.Strings(groups)
	scopeSHA := hashText(strings.Join(groups, "\n\n"))
	digests := make([]hash.Hash, len(values))
	roleDigests := make([]hash.Hash, len(values))
	for i := range values {
		h := values[i].RecordIdentity
		for j := range h.Roles {
			h.Roles[j].ScopeSHA256 = scopeSHA
		}
		canonicalRecordIdentity(h)
		roleDigest := sha256.New()
		for _, r := range h.Roles {
			b, _ := json.Marshal(r)
			roleDigest.Write(append(b, '\n'))
		}
		count := len(h.Roles)
		roleKinds := map[string]int{}
		retained := []RecordRoleHint{}
		for _, r := range h.Roles {
			roleKinds[r.Kind]++
			if roleKinds[r.Kind] <= identityWitnessPerKind {
				retained = append(retained, r)
			}
		}
		h.Roles = retained
		refs := []string{}
		for _, f := range features {
			if f.candidate == i {
				refs = append(refs, identityRefKey(f.ref))
			}
		}
		h.Scopes = []RecordIdentityScope{{InputSHA256: scopeSHA, SubjectRecordCount: len(refs), SubjectSHA256: hashText(strings.Join(refs, "\n")), RoleCount: count, OmittedRoles: count - len(h.Roles), RoleKinds: roleKinds, RelationKinds: map[string]int{}}}
		roleDigests[i] = roleDigest
		digests[i] = sha256.New()
	}
	emit := func(index int, hint RecordRelationHint) {
		h := values[index].RecordIdentity
		witness := hint
		witness.TargetCandidateID = ""
		b, _ := json.Marshal(witness)
		digests[index].Write(append(b, '\n'))
		h.Scopes[0].RelationCount++
		h.Scopes[0].RelationKinds[hint.Kind]++
		if h.Scopes[0].RelationKinds[hint.Kind] <= identityWitnessPerKind {
			h.Relations = append(h.Relations, hint)
		}
	}
	for i := range features {
		if err := ctx.Err(); err != nil {
			return err
		}
		for j := i + 1; j < len(features); j++ {
			if j%32 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			a, b := features[i], features[j]
			if values[a.candidate].ProjectID != values[b.candidate].ProjectID {
				continue
			}
			values[a.candidate].RecordIdentity.Scopes[0].ComparedPairs++
			if a.candidate != b.candidate {
				values[b.candidate].RecordIdentity.Scopes[0].ComparedPairs++
			}
			shared, conflict := identityIdentifierAliases(a.aliases, b.aliases)
			kind := ""
			basis := []string{}
			if conflict && (shared || a.candidate == b.candidate) {
				kind = "identifier_conflict"
				basis = []string{"conflicting_strong_identifiers"}
				if shared {
					basis = append(basis, "shared_strong_identifier")
				}
			}
			sameAuthors := a.authors != "" && a.authors == b.authors
			if kind == "" && a.candidate != b.candidate && sameAuthors && a.work.Year >= 1000 && b.work.Year >= 1000 {
				delta := a.work.Year - b.work.Year
				if delta < 0 {
					delta = -delta
				}
				if delta <= 1 && (a.companion == b.title || b.companion == a.title) {
					kind = "possible_companion"
					basis = []string{"explicit_companion_title", "same_full_authors", "compatible_years"}
				} else if a.work.Year == b.work.Year && a.companion == "" && b.companion == "" && utf8.RuneCountInString(a.title) >= 16 && utf8.RuneCountInString(b.title) >= 16 && identityOverlap(a.titleWords, b.titleWords, 1) >= 0.65 && identityAbstractOverlap(a, b) {
					kind = "possible_same_work"
					basis = []string{"same_year", "same_full_authors", "title_token_overlap_at_least_0.65", "abstract_overlap_at_least_0.85_or_long_prefix"}
					if a.preprint != b.preprint {
						kind = "possible_version"
					}
					if conflict {
						kind = "identifier_conflict"
						basis = append(basis, "conflicting_strong_identifiers")
					}
				}
			}
			if kind == "" && shared && a.candidate != b.candidate {
				kind = "shared_identifier"
				basis = []string{"shared_strong_identifier"}
			}
			if kind != "" {
				refs := []IdentityRecordRef{a.ref, b.ref}
				if identityRefKey(refs[1]) < identityRefKey(refs[0]) {
					refs[0], refs[1] = refs[1], refs[0]
				}
				emit(a.candidate, RecordRelationHint{ScopeSHA256: scopeSHA, Kind: kind, TargetCandidateID: values[b.candidate].ID, Records: refs, Basis: basis})
				if a.candidate != b.candidate {
					emit(b.candidate, RecordRelationHint{ScopeSHA256: scopeSHA, Kind: kind, TargetCandidateID: values[a.candidate].ID, Records: refs, Basis: basis})
				}
			}
		}
	}
	for i := range values {
		h := values[i].RecordIdentity
		scope := &h.Scopes[0]
		scope.Complete = true
		scope.InputRecordCount = len(features)
		scope.RoleSHA256 = hex.EncodeToString(roleDigests[i].Sum(nil))
		scope.RelationSHA256 = hex.EncodeToString(digests[i].Sum(nil))
		scope.OmittedRelations = scope.RelationCount - len(h.Relations)
		canonicalRecordIdentity(h)
	}
	return ctx.Err()
}
func identityRefKey(r IdentityRecordRef) string {
	return r.SourceID + "\x00" + r.SourceRecordID + "\x00" + r.WorkSHA256
}
func identityAuthors(authors []Author) string {
	if len(authors) == 0 {
		return ""
	}
	names := make([]string, 0, len(authors))
	for _, a := range authors {
		n := normalizeTitle(a.Name)
		if strings.ContainsRune(a.Name, '\ufffd') || n == "unknown" || n == "anonymous" || n == "et al" {
			return ""
		}
		if utf8.RuneCountInString(n) < 3 {
			return ""
		}
		parts := strings.Fields(n)
		sort.Strings(parts)
		names = append(names, strings.Join(parts, " "))
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}
func identityWords(s string, n int) map[string]bool {
	words := strings.Fields(s)
	out := map[string]bool{}
	for i := 0; i+n <= len(words); i++ {
		out[strings.Join(words[i:i+n], " ")] = true
	}
	return out
}
func identityOverlap(a, b map[string]bool, min int) float64 {
	if len(a) < min || len(b) < min {
		return 0
	}
	hits := 0
	for k := range a {
		if b[k] {
			hits++
		}
	}
	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	return float64(hits) / float64(smaller)
}
func identityAbstractOverlap(a, b identityFeatures) bool {
	if utf8.RuneCountInString(a.abstract) < 160 || utf8.RuneCountInString(b.abstract) < 160 {
		return false
	}
	return strings.HasPrefix(a.abstract, b.abstract) || strings.HasPrefix(b.abstract, a.abstract) || identityOverlap(a.abstractShingles, b.abstractShingles, 20) >= 0.85
}
func identityIdentifierAliases(aa, bb []CandidateAlias) (shared, conflict bool) {
	for _, x := range aa {
		if x.Kind == "title_year" {
			continue
		}
		for _, y := range bb {
			if x.Kind == y.Kind {
				if x.Value == y.Value {
					shared = true
				} else {
					conflict = true
				}
			}
		}
	}
	return
}
