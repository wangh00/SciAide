package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

func TestCapturedReviewSameSourceSelection(t *testing.T) {
	dir := os.Getenv("SCIAIDE_REVIEW_CELL_CAPTURE")
	if dir == "" {
		t.Skip("no isolated capture")
	}
	read := func(name string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var diagnostic workflow.ReviewBasisErrors
	if err := json.Unmarshal(read("derived/basis-diagnostic.json"), &diagnostic); err != nil {
		t.Fatal(err)
	}
	var turns []struct {
		Index int    `json:"turn_index"`
		Text  string `json:"draft_text"`
	}
	_ = json.Unmarshal(read("model_turn_journal.json"), &turns)
	base := ""
	for _, turn := range turns {
		if turn.Index == 4 {
			base = turn.Text
		}
	}
	r := newReviewRepair(base, read("schema.json"), &diagnostic)
	if r == nil || r.hash != "d8dee3fbe18895aabd0203039be3c77bf24c9ce6390e54b600e858f907a6ed7f" {
		t.Fatal("original base changed")
	}
	for _, turn := range turns {
		if turn.Index < 5 {
			continue
		}
		merged, err := r.merge(turn.Text)
		if err != nil {
			t.Fatal("real same-source correction rejected", turn.Index, err)
		}
		var before, after map[string]json.RawMessage
		_ = json.Unmarshal(r.base, &before)
		_ = json.Unmarshal([]byte(merged), &after)
		var a, b []map[string]json.RawMessage
		_ = json.Unmarshal(before["reviewFindings"], &a)
		_ = json.Unmarshal(after["reviewFindings"], &b)
		for i := range a {
			var ba, bb []map[string]json.RawMessage
			_ = json.Unmarshal(a[i]["basis"], &ba)
			_ = json.Unmarshal(b[i]["basis"], &bb)
			for j := range ba {
				if (i == 2 && j == 0) || (i == 4 && (j == 0 || j == 1)) {
					ba[j]["quote"] = bb[j]["quote"]
				}
			}
			a[i]["basis"], _ = json.Marshal(ba)
		}
		before["reviewFindings"], _ = json.Marshal(a)
		left, _ := json.Marshal(before)
		right, _ := json.Marshal(after)
		var lc, rc bytes.Buffer
		_ = json.Compact(&lc, left)
		_ = json.Compact(&rc, right)
		if !bytes.Equal(lc.Bytes(), rc.Bytes()) {
			t.Fatal("non-quote scientific fields changed")
		}
		if err := os.WriteFile(filepath.Join(dir, "derived", fmt.Sprintf("merged-turn-%d.json", turn.Index)), []byte(merged), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("both original patches select already-offered current-field text; only diagnosed quote cells changed, verdict and all scientific judgments preserved")
}
