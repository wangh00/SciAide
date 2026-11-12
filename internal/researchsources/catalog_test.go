package researchsources

import "testing"

func TestCatalogCopiesAndCOREIsOptInWithoutMaterialAuthority(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Definitions() {
		if seen[s.ID] {
			t.Fatal("duplicate source", s.ID)
		}
		seen[s.ID] = true
		if len(s.FullTextHosts) > 0 {
			s.FullTextHosts[0] = "evil.test"
			original, _ := Lookup(s.ID)
			if original.FullTextHosts[0] == "evil.test" {
				t.Fatal("mutable authority")
			}
		}
	}
	core, ok := Lookup("core")
	if !ok || !core.ExplicitSelection || len(core.FullTextHosts) != 0 {
		t.Fatal(core)
	}
	eric, ok := Lookup("eric")
	if !ok || !eric.PublicationYears || len(eric.FullTextHosts) != 1 || eric.FullTextHosts[0] != "files.eric.ed.gov" {
		t.Fatal(eric)
	}
}
