package research

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func identityFixture(id string) Candidate {
	return Candidate{ID: id, ProjectID: "p", ReviewStatus: ReviewPending, Records: []SourceRecord{{Work: Work{SourceID: "fixture", SourceRecordID: id, Title: "Automated writing feedback and undergraduate revision quality", Year: 2024, Authors: []Author{{Name: "Imelda Gozali"}, {Name: "Anita Lie"}}, Abstract: strings.Repeat("This study examined eighteen students revising essays using feedback. ", 8)}}}}
}
func TestRecordIdentityHintsRequireMultipleIndependentMetadataSignals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Work)
		want   string
	}{
		{name: "same_metadata", change: func(w *Work) { w.Authors = []Author{{Name: "Lie, Anita"}, {Name: "Gozali, Imelda"}} }, want: "possible_same_work"},
		{name: "preprint", change: func(w *Work) { w.WorkType = "preprint" }, want: "possible_version"},
		{name: "different_authors", change: func(w *Work) { w.Authors = []Author{{Name: "Unrelated Author"}} }},
		{name: "different_year", change: func(w *Work) { w.Year = 2025 }},
		{name: "missing_year", change: func(w *Work) { w.Year = 0 }},
		{name: "no_abstract", change: func(w *Work) { w.Abstract = "" }},
		{name: "short_abstract", change: func(w *Work) { w.Abstract = "Generic findings." }},
		{name: "no_authors", change: func(w *Work) { w.Authors = nil }},
		{name: "different_abstract", change: func(w *Work) {
			w.Abstract = strings.Repeat("Different unrelated protocols of molecular sequencing experimental cultures. ", 8)
		}},
		{name: "conflicting_doi", change: func(w *Work) { w.Identifiers.DOI = "10.1000/b" }, want: "identifier_conflict"},
		{name: "same_author_with_replacement_character", change: func(w *Work) { w.Authors = []Author{{Name: "Imelda Gozali\ufffd"}, {Name: "Anita Lie"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := []Candidate{identityFixture("a"), identityFixture("b")}
			values[0].Records[0].Work.Identifiers.DOI = "10.1000/a"
			tc.change(&values[1].Records[0].Work)
			before, _ := json.Marshal(values)
			AnnotateRecordIdentities(values)
			hints := values[0].RecordIdentity
			if tc.want == "" {
				if len(hints.Relations) != 0 {
					t.Fatal("unsupported relation", hints)
				}
			} else if len(hints.Relations) != 1 || hints.Relations[0].Kind != tc.want || hints.Relations[0].TargetCandidateID != "b" {
				t.Fatal(hints)
			}
			if len(values) != 2 || values[0].ReviewStatus != ReviewPending || values[1].ReviewStatus != ReviewPending {
				t.Fatal("hints mutated candidates")
			}
			values[0].RecordIdentity = nil
			values[1].RecordIdentity = nil
			after, _ := json.Marshal(values)
			if string(before) != string(after) {
				t.Fatal("source metadata changed")
			}
		})
	}
}
func TestRecordIdentityCompanionHintsDoNotInventPrimaryWorkOrExcludeMaterial(t *testing.T) {
	main := identityFixture("main")
	supp := identityFixture("supp")
	supp.Records[0].Work.Title = `Supplemental Files for "` + main.Records[0].Work.Title + `"`
	supp.Records[0].Work.Abstract = "Online supplementary materials"
	supp.Records[0].Work.WorkType = "project"
	values := []Candidate{supp}
	AnnotateRecordIdentities(values)
	if len(values[0].RecordIdentity.Roles) != 1 || len(values[0].RecordIdentity.Relations) != 0 || values[0].ReviewStatus != ReviewPending {
		t.Fatal(values)
	}
	values = []Candidate{main, supp}
	AnnotateRecordIdentities(values)
	if len(values[0].RecordIdentity.Relations) != 1 || values[0].RecordIdentity.Relations[0].Kind != "possible_companion" {
		t.Fatal(values[0].RecordIdentity)
	}
	supp.Records[0].Work.Title = "Supplementary material improves undergraduate writing outcomes"
	values = []Candidate{supp}
	AnnotateRecordIdentities(values)
	if len(values[0].RecordIdentity.Roles) != 0 {
		t.Fatal("generic topic interpreted as supplementary file")
	}
}
func TestRecordIdentityViewsHaveStableHashesDiscloseOmissionAndDoNotMutate(t *testing.T) {
	values := []Candidate{}
	for i := 0; i < 12; i++ {
		values = append(values, identityFixture(fmt.Sprint(i)))
	}
	AnnotateRecordIdentities(values)
	full := values[0].RecordIdentity
	b, _ := json.Marshal(full)
	view := PreviewRecordIdentity(full)
	if len(full.Relations) != identityWitnessPerKind || len(view.Relations) != 2 || !view.Preview.Truncated || view.Preview.RelationCount != 11 || view.Preview.SHA256 != RecordIdentityContentSHA256(full) {
		t.Fatal(view)
	}
	overview := ImportRecordIdentity(full)
	if len(overview.RelatedCandidateIDs) != 2 || !overview.Preview.Truncated || overview.Preview.SHA256 != view.Preview.SHA256 {
		t.Fatal(overview)
	}
	viewAgain := PreviewRecordIdentity(view)
	if viewAgain.Preview.SHA256 != view.Preview.SHA256 {
		t.Fatal("hashed preview instead of original")
	}
	merged := MergeRecordIdentities(full, full)
	m, _ := json.Marshal(merged)
	if string(m) != string(b) {
		t.Fatal("union duplicates or source order drift")
	}
	after, _ := json.Marshal(full)
	if string(after) != string(b) {
		t.Fatal("view mutated frozen full hints")
	}
	values[0].Records[0].Work.Abstract += "Changed source snapshot."
	AnnotateRecordIdentities(values)
	changed, _ := json.Marshal(values[0].RecordIdentity)
	if string(changed) == string(b) {
		t.Fatal("source change not bound")
	}
}
func TestRecordIdentityStrongConflictIsVisibleEvenWithoutAbstract(t *testing.T) {
	a, b := identityFixture("a"), identityFixture("b")
	a.Records[0].Work.Abstract = ""
	b.Records[0].Work.Abstract = ""
	a.Records[0].Work.Identifiers = Identifiers{DOI: "10.1000/a", PMID: "123"}
	b.Records[0].Work.Identifiers = Identifiers{DOI: "10.1000/b", PMID: "123"}
	values := []Candidate{a, b}
	AnnotateRecordIdentities(values)
	if len(values[0].RecordIdentity.Relations) != 1 || values[0].RecordIdentity.Relations[0].Kind != "identifier_conflict" {
		t.Fatal(values)
	}
}
func TestRecordIdentityLegacyAbsenceAndCaseSensitiveSourceKeys(t *testing.T) {
	if PreviewRecordIdentity(nil) != nil || ImportRecordIdentity(nil) != nil {
		t.Fatal("invented legacy hints")
	}
	a := Work{SourceID: "core", SourceRecordID: "Ab", Title: "Exactly the same scientific title", Year: 2025}
	b := a
	b.SourceRecordID = "ab"
	if CandidateKey(a) == CandidateKey(b) {
		t.Fatal("case-sensitive source identities collided")
	}
	if _, ok := RecordGroupingRulesFor("record-grouping-v1"); !ok {
		t.Fatal("old procedure unrecognized")
	}
	if _, ok := RecordGroupingRulesFor("future"); ok {
		t.Fatal("unknown procedure trusted")
	}
}

func TestRecordIdentityScopedInventoriesAreBoundedReconstructibleAndPortable(t *testing.T) {
	values := make([]Candidate, 400)
	for i := range values {
		values[i] = identityFixture(fmt.Sprintf("r%03d", i))
	}
	start := time.Now()
	if err := AnnotateRecordIdentitiesContext(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(values)
	if len(b) > 2400*1024 {
		t.Fatal("quadratic persisted hints", len(b))
	}
	for _, v := range values {
		h := v.RecordIdentity
		if len(h.Relations) != identityWitnessPerKind || len(h.Scopes) != 1 || !h.Scopes[0].Complete || h.Scopes[0].InputRecordCount != 400 || h.Scopes[0].RelationCount != 399 || h.Scopes[0].OmittedRelations != 397 {
			t.Fatal(h)
		}
	}
	t.Logf("400 records: saved=%d bytes, elapsed=%s, all 159600 directed hints counted, 800 witnesses retained", len(b), time.Since(start))
	// Local candidate IDs are references, not part of portable source content SHA.
	before := RecordIdentityContentSHA256(values[0].RecordIdentity)
	for i := range values[0].RecordIdentity.Relations {
		values[0].RecordIdentity.Relations[i].TargetCandidateID = "remapped-" + values[0].RecordIdentity.Relations[i].TargetCandidateID
	}
	if RecordIdentityContentSHA256(values[0].RecordIdentity) != before {
		t.Fatal("local ID remapping changed content hash")
	}
	// Reconstructing different query scopes is order-independent, but is NOT a
	// promise that counts were deduplicated across those scopes.
	q1 := []Candidate{identityFixture("a"), identityFixture("b")}
	q2 := []Candidate{identityFixture("a"), identityFixture("c")}
	AnnotateRecordIdentities(q1)
	AnnotateRecordIdentities(q2)
	ab := MergeRecordIdentities(q1[0].RecordIdentity, q2[0].RecordIdentity)
	ba := MergeRecordIdentities(q2[0].RecordIdentity, q1[0].RecordIdentity)
	if len(ab.Scopes) != 2 || RecordIdentityContentSHA256(ab) != RecordIdentityContentSHA256(ba) || len(ab.Relations) != 2 {
		t.Fatal("query scoped inventory drift")
	}
	bad := MergeRecordIdentities(ab, &RecordIdentity{Version: "future"})
	bad = MergeRecordIdentities(bad, ab)
	if bad.Version == RecordIdentityVersion {
		t.Fatal("unknown inventory silently became current again")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if AnnotateRecordIdentitiesContext(ctx, values) == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestCandidateAliasesRejectMalformedIdentityWithoutErasingSourceFields(t *testing.T) {
	for _, id := range []Identifiers{{DOI: "10.invalid"}, {DOI: "10.x/value"}, {DOI: "10.1000/"}, {PMID: "abc123"}, {ArXiv: "not-an-arxiv-id"}, {OpenAlex: "not-an-openalex-id"}} {
		w := Work{SourceID: "fixture", SourceRecordID: "r", Identifiers: id}
		if len(CandidateAliases(w)) != 0 || w.Identifiers != id {
			t.Fatal("malformed identity used or erased", id)
		}
	}
	for _, id := range []Identifiers{{DOI: "https://doi.org/10.1000/ABC"}, {PMID: "123"}, {ArXiv: "https://arxiv.org/abs/2401.01234v2"}, {ArXiv: "hep-th/9901001"}, {OpenAlex: "https://openalex.org/W123"}} {
		if len(CandidateAliases(Work{Identifiers: id})) != 1 {
			t.Fatal("valid identity shape rejected", id)
		}
	}
}
func TestNormalizeWorksPreservesCaseSensitiveSourceRecordIdentities(t *testing.T) {
	works := []Work{{SourceRecordID: "Ab", Title: "One"}, {SourceRecordID: "ab", Title: "Two"}, {SourceRecordID: "Ab", Title: "Duplicate same source"}}
	got := normalizeWorks("fixture", works, 50)
	if len(got) != 2 || got[0].SourceRecordID == got[1].SourceRecordID {
		t.Fatal(got)
	}
}

func TestRecordIdentityPriorityAndSubjectBindingSurviveAllViews(t *testing.T) {
	values := make([]Candidate, 7)
	for i := range values {
		values[i] = identityFixture(fmt.Sprintf("r%d", i))
	}
	values[0].Records[0].Work.Identifiers = Identifiers{DOI: "10.1000/a", PMID: "123"}
	values[6].Records[0].Work.Identifiers = Identifiers{DOI: "10.1000/b", PMID: "123"}
	AnnotateRecordIdentities(values)
	h := values[0].RecordIdentity
	preview := PreviewRecordIdentity(h)
	overview := ImportRecordIdentity(h)
	if len(h.Relations) != 3 || h.Scopes[0].RelationKinds["possible_same_work"] != 5 || h.Scopes[0].RelationKinds["identifier_conflict"] != 1 || h.Relations[0].Kind != "identifier_conflict" || preview.Relations[0].Kind != "identifier_conflict" || overview.RelationKinds[0] != "identifier_conflict" {
		t.Fatal("conflict hidden", h, preview, overview)
	}
	pair := []Candidate{identityFixture("a"), identityFixture("b")}
	AnnotateRecordIdentities(pair)
	if RecordIdentityContentSHA256(pair[0].RecordIdentity) == RecordIdentityContentSHA256(pair[1].RecordIdentity) {
		t.Fatal("two ends share a content identity")
	}
}
func TestRecordIdentityNonPrefixLongAbstractPressureAndCancellation(t *testing.T) {
	common := ""
	for i := 0; i < 2000; i++ {
		common += fmt.Sprintf("word%04d ", i)
	}
	values := make([]Candidate, 400)
	for i := range values {
		values[i] = identityFixture(fmt.Sprintf("r%03d", i))
		values[i].Records[0].Work.Abstract = fmt.Sprintf("unique%03d ", i) + common
	}
	start := time.Now()
	if err := AnnotateRecordIdentitiesContext(context.Background(), values); err != nil {
		t.Fatal(err)
	}
	if values[0].RecordIdentity.Scopes[0].RelationCount != 399 {
		t.Fatal("non-prefix overlap not compared")
	}
	t.Logf("400 non-prefix ~18K abstracts: elapsed=%s, all pairs scanned", time.Since(start))
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(10*time.Millisecond, cancel)
	defer timer.Stop()
	start = time.Now()
	if err := AnnotateRecordIdentitiesContext(ctx, values); err == nil {
		t.Fatal("mid-scan cancellation ignored")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation not bounded", time.Since(start))
	}
}
