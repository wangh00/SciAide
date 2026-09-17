package research

import (
	"fmt"
	"github.com/wangh00/SciAide/internal/document"
	"strings"
	"testing"
	"time"
)

func TestFullTextCacheBoundedScopedAndExpires(t *testing.T) {
	c := fullTextCache{}
	p := document.Parsed{Units: []document.Unit{{Content: "weight result", Locator: "page:2"}}}
	c.put("task-a/candidate/snapshot", p, "sha")
	if _, _, ok := c.get("task-b/candidate/snapshot"); ok {
		t.Fatal("cross task reuse")
	}
	if _, _, ok := c.get("task-a/candidate/changed"); ok {
		t.Fatal("stale snapshot reuse")
	}
	got, sha, ok := c.get("task-a/candidate/snapshot")
	if !ok || sha != "sha" || got.Units[0].Locator != "page:2" {
		t.Fatal("cache lost source")
	}
	v := c.entries["task-a/candidate/snapshot"]
	v.at = time.Now().Add(-time.Hour)
	c.entries["task-a/candidate/snapshot"] = v
	if _, _, ok := c.get("task-a/candidate/snapshot"); ok {
		t.Fatal("expired cache")
	}
	for i := 0; i < 40; i++ {
		c.put(fmt.Sprint(i), p, "sha")
	}
	if len(c.entries) > 16 || c.bytes > 8<<20 {
		t.Fatal("unbounded cache")
	}
	c.put("large", document.Parsed{Units: []document.Unit{{Content: strings.Repeat("x", 3<<20)}}}, "sha")
	if _, _, ok := c.get("large"); ok {
		t.Fatal("oversize admitted")
	}
}
