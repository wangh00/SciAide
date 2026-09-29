package research

import (
	"encoding/json"
	"testing"

	"github.com/wangh00/SciAide/internal/researchtext"
)

func TestRecoverAbstractRequiresExactLegacyContentAndIdentity(t *testing.T) {
	source := `<h4>Results</h4>p<.005), benefit 0.72 points.<h4>Conclusion</h4>Association.`
	raw, _ := json.Marshal(map[string]string{"id": "123", "source": "MED", "abstractText": source})
	w := Work{SourceID: "europepmc", SourceRecordID: "MED/123", Abstract: researchtext.Legacy(source), RawSnapshot: raw}
	c := Candidate{Preferred: w, Records: []SourceRecord{{Work: w}}}
	got, changed := RecoverCandidateAbstracts(c)
	if !changed || got.Preferred.Abstract != researchtext.Plain(source) || got.Records[0].Work.Abstract != got.Preferred.Abstract {
		t.Fatalf("%+v %v", got, changed)
	}
	if c.Records[0].Work.Abstract != w.Abstract {
		t.Fatal("mutated original source record")
	}
	if _, changed = RecoverCandidateAbstracts(got); changed {
		t.Fatal("recovery is not idempotent")
	}
	for _, mutate := range []func(*Work){
		func(w *Work) { w.SourceRecordID = "MED/other" },
		func(w *Work) { w.Abstract = "manually corrected source" },
		func(w *Work) { w.RawSnapshot = nil },
		func(w *Work) { w.SourceID = "pubmed" },
	} {
		value := w
		mutate(&value)
		if _, changed = RecoverCandidateAbstracts(Candidate{Preferred: value, Records: []SourceRecord{{Work: value}}}); changed {
			t.Fatal("unverified source was recovered")
		}
	}
}
