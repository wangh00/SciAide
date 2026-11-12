package connectors

import (
	"context"
	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/researchsources"
	"os"
	"testing"
	"time"
)

func TestCORELiveAnonymousMetadataContract(t *testing.T) {
	if os.Getenv("SCIAIDE_LITERATURE_SOURCE_LIVE") != "1" {
		t.Skip("public network source probe is opt-in")
	}
	source, _ := researchsources.Lookup("core")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	works, err := newCORE(NewClient(), source.Endpoint, source.MinSpacing).Search(ctx, research.SearchOptions{Query: "ChatGPT AND writing", Limit: 2, Years: research.PublicationYears{From: 2025, To: 2025}})
	if err != nil || len(works) == 0 {
		t.Fatal("live CORE metadata", err, len(works))
	}
	for _, w := range works {
		if w.SourceRecordID == "" || w.Year != 2025 || w.PDFURL != "" || w.OpenAccess || w.SourceMetadata == nil {
			t.Fatal("live CORE scope/year mismatch", w.SourceRecordID, w.Year)
		}
	}
	t.Logf("live CORE anonymous metadata records=%d firstRecord=%s year=2025 fullTextDisabled=true; modelRequests=0", len(works), works[0].SourceRecordID)
}
