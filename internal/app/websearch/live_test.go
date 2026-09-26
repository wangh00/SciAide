package websearch

import (
	"context"
	"errors"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"os"
	"testing"
)

func TestLivePublicWeb(t *testing.T) {
	if os.Getenv("SCIAIDE_TEST_PUBLIC_WEB") != "1" {
		t.Skip("explicit public-network test only")
	}
	s := New(secretstore.NewMemory())
	r, e := s.Search(context.Background(), "Python scipy ttest_rel documentation", 3)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("DDG status=%s results=%d attempts=%+v", r.Status, len(r.Items), r.Attempts)
	p, e := Open(context.Background(), "https://docs.python.org/3/library/csv.html", "DictReader")
	if e != nil {
		if errors.Is(e, errNonPublicAddress) {
			t.Skipf("Public page not verified: %v", e)
		}
		t.Fatal(e)
	}
	if len(p.Text) == 0 {
		t.Fatal("empty page")
	}
	t.Logf("page=%s chars=%d links=%d truncated=%v", p.URL, len([]rune(p.Text)), len(p.Links), p.Truncated)
}
