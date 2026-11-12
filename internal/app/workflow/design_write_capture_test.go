package workflow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Opt-in, read-only replay of the two actual accepted design outputs. It does
// not open an application/database or rewrite old completed scientific results.
func TestCapturedDesignWriteScopeDrift(t *testing.T) {
	path := os.Getenv("SCIAIDE_DESIGN_WRITE_CAPTURE")
	if path == "" {
		t.Skip("no isolated task capture supplied")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Baseline, Candidate string
		SelectedIndices     []int
	}
	if err := json.Unmarshal(data, &c); err != nil || len(c.SelectedIndices) != 3 {
		t.Fatal("incomplete capture", err)
	}
	m := designWriteMask{baseline: raw(c.Baseline), markdowns: map[int]bool{}}
	for _, i := range c.SelectedIndices {
		m.markdowns[i] = true
	}
	err = m.validate(raw(c.Candidate))
	if err == nil || !strings.Contains(err.Error(), "/deliverables/0/markdown") || !strings.Contains(err.Error(), "/populationAndSampling/1") {
		t.Fatal("actual unauthorized changes escaped", err)
	}
	good := strings.ReplaceAll(strings.ReplaceAll(c.Baseline, "糀倒", "绊倒"), "即佾", "即使")
	if err := m.validate(raw(good)); err != nil {
		t.Fatal("only requested design replacements rejected", err)
	}
	t.Log("actual two unselected changes rejected; exact requested replacements accepted; original capture unchanged")
}
