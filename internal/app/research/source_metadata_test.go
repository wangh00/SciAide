package research

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetadataViewRetainsKeyFieldsAndFullSourceIdentity(t *testing.T) {
	yes := true
	original := &SourceMetadata{Subjects: []string{}, EducationLevels: []string{"Higher Education"}, PublicationTypes: []string{"Journal Articles"}, PeerReviewed: &yes, SourceCitation: "v1 n2"}
	for i := 0; i < 100; i++ {
		original.Subjects = append(original.Subjects, strings.Repeat("题", 300))
	}
	full, _ := json.Marshal(original)
	view, info := PreviewSourceMetadata(original)
	encoded, _ := json.Marshal(view)
	if len(encoded) > 5000 || !info.Truncated || info.SubjectCount != 100 || info.SHA256 == "" || view.EducationLevels[0] != "Higher Education" || view.PeerReviewed == nil || !*view.PeerReviewed {
		t.Fatal(view, info, len(encoded))
	}
	after, _ := json.Marshal(original)
	if string(full) != string(after) {
		t.Fatal("projection mutated full source")
	}
	changed := *original
	changed.Subjects = append([]string(nil), original.Subjects...)
	changed.Subjects[99] = "new omitted value"
	_, other := PreviewSourceMetadata(&changed)
	if other.SHA256 == info.SHA256 {
		t.Fatal("omitted data not bound by hash")
	}
	records := []SourceRecord{}
	for i := 0; i < 20; i++ {
		records = append(records, SourceRecord{Work: Work{SourceID: "eric", SourceRecordID: "EJ1234567", SourceMetadata: original}})
	}
	refs := SourceMetadataRecords(records)
	if len(refs) != 1 {
		t.Fatal("exact duplicate source inventory grew", len(refs))
	}
	views, total, hash := PreviewSourceMetadataRecords(refs)
	if len(views) != 1 || total != 1 || hash == "" {
		t.Fatal(views, total, hash)
	}
}
