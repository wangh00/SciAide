package reviewcontract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCapturedGateReceiptRetainsPersistedIdentity(t *testing.T) {
	dir := os.Getenv("SCIAIDE_GATE_AUDIT_DIR")
	if dir == "" {
		t.Skip("no isolated captured gate supplied")
	}
	read := func(name string, target any) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, target); err != nil {
			t.Fatal(err)
		}
	}
	var in struct{ Subject, Review json.RawMessage }
	var out struct{ Structured json.RawMessage }
	// The readable input capture is pretty-printed. Restore the exact saved
	// argument bytes from the call journal before checking a byte digest.
	var calls []struct {
		Name      string `json:"tool_name"`
		Status    string `json:"status"`
		Arguments string `json:"arguments_json"`
	}
	read("calls", &calls)
	for _, call := range calls {
		if call.Name == "builtin.research.workflow.review.gate" && call.Status == "completed" {
			if err := json.Unmarshal([]byte(call.Arguments), &in); err != nil {
				t.Fatal(err)
			}
		}
	}
	read("delivery_gate-output_json", &out)
	receipt, err := Evaluate(in.Subject, in.Review)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(receipt)
	if !Equal(encoded, out.Structured) {
		t.Fatal("new shared contract changed the stored approval identity")
	}
	t.Log("captured gate result remains identical; no model or formal task modified")
}

func TestSharedGateContractPreservesDecisionAndDigest(t *testing.T) {
	input := json.RawMessage(`{"context":{"markdown":"中文正文"},"evidence":{"limited":true}}`)
	core := Core{Approved: true, ReviewedInputSHA256: Digest(input), VerifiedClaims: []string{"limited claim"}, UnsupportedClaims: []string{}, CitationIssues: []string{}, NumericIssues: []string{}, MethodIssues: []string{}, RequiredCorrections: []string{}, Confidence: "low", Limitations: []string{"no full text"}}
	full, _ := json.Marshal(core)
	r, err := Evaluate(input, full)
	if err != nil || !r.Approved || r.ReviewSHA256 != Digest(full) || r.Confidence != "low" {
		t.Fatal("contract drift", r, err)
	}
	core.Approved = false
	full, _ = json.Marshal(core)
	_, err = Evaluate(input, full)
	var rejected *Rejected
	if !errors.As(err, &rejected) {
		t.Fatal("scientific rejection became technical", err)
	}
	_, err = Evaluate(json.RawMessage(`{"context":"changed"}`), full)
	if !errors.Is(err, ErrBinding) {
		t.Fatal("stale input passed", err)
	}
	if SubjectMatchesInput(json.RawMessage(`{"markdown":"中文正文"}`), input) {
		t.Fatal("ignored evidence input")
	}
	if Equal(json.RawMessage(`{"id":9007199254740992}`), json.RawMessage(`{"id":9007199254740993}`)) {
		t.Fatal("large identifiers rounded")
	}
}
