// Package reviewcontract owns the persisted independent-review/gate exchange.
// It knows neither research disciplines nor workflow node names. Content
// judgments belong to the reviewer; identities and projections belong here.
package reviewcontract

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/reportview"
)

type Core struct {
	Approved            bool     `json:"approved"`
	ReviewedInputSHA256 string   `json:"reviewedInputSha256"`
	VerifiedClaims      []string `json:"verifiedClaims"`
	UnsupportedClaims   []string `json:"unsupportedClaims"`
	CitationIssues      []string `json:"citationIssues"`
	NumericIssues       []string `json:"numericIssues"`
	MethodIssues        []string `json:"methodIssues"`
	RequiredCorrections []string `json:"requiredCorrections"`
	Confidence          string   `json:"confidence"`
	Limitations         []string `json:"limitations"`
}

type Receipt struct {
	Approved            bool     `json:"approved"`
	ReviewedInputSHA256 string   `json:"reviewedInputSha256"`
	ReviewSHA256        string   `json:"reviewSha256"`
	Confidence          string   `json:"confidence"`
	VerifiedClaimCount  int      `json:"verifiedClaimCount"`
	Limitations         []string `json:"limitations"`
}

var ErrBinding = errors.New("independent review snapshot binding failed")

type Rejected struct{ Issues, Corrections int }

func (e *Rejected) Error() string {
	return fmt.Sprintf("独立审查未通过：审查记录列出 %d 条分类意见，并归纳为 %d 条必须修正项；当前结果不会交付", e.Issues, e.Corrections)
}

func Digest(value []byte) string { return fmt.Sprintf("%x", sha256.Sum256(value)) }

// This is semantic equality, NOT the hash of the persisted input bytes.
// Keep JSON numbers exact; decoding through float64 loses large identifiers.
func Equal(left, right json.RawMessage) bool {
	l, le := Key(left)
	r, re := Key(right)
	return le == nil && re == nil && l == r
}

func Key(value json.RawMessage) (string, error) {
	if !json.Valid(value) {
		return "", fmt.Errorf("invalid JSON")
	}
	d := json.NewDecoder(strings.NewReader(string(value)))
	d.UseNumber()
	var object any
	if err := d.Decode(&object); err != nil {
		return "", err
	}
	b, err := json.Marshal(object)
	return string(b), err
}

// Strip only the established extended-review fields. Unknown fields remain
// visible to the gate's closed schema instead of being silently discarded.
func ProjectCore(output json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(output, &fields) != nil || fields == nil {
		return nil, fmt.Errorf("独立审查输出不是对象")
	}
	for _, name := range []string{"acceptanceChecks", "revisionPlan", "reviewFindings", "suggestions"} {
		delete(fields, name)
	}
	return json.Marshal(fields)
}

// Older direct gate callers supplied just the result. Only that exact one-key
// envelope is accepted; never drop additional evidence or contract fields.
func SubjectMatchesInput(subject, input json.RawMessage) bool {
	if Equal(subject, input) {
		return true
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(input, &fields) == nil && len(fields) == 1 && Equal(fields["context"], subject)
}

func SubjectInput(subject json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(subject, &fields) != nil || fields == nil {
		return nil, fmt.Errorf("%w: subject is not an object", ErrBinding)
	}
	if _, has := fields["context"]; has {
		return subject, nil
	}
	return json.Marshal(map[string]json.RawMessage{"context": subject})
}

// Result restores the same complete result used by review dependency checks.
// The original subject is immutable; this is a derived publication view.
func Result(subject json.RawMessage) (json.RawMessage, error) {
	assembled, err := reportview.AssembleInput(subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBinding, err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(assembled, &fields) != nil || fields == nil {
		return nil, fmt.Errorf("%w: result is not an object", ErrBinding)
	}
	if context, ok := fields["context"]; ok {
		return context, nil
	}
	return assembled, nil
}

// Evaluate is the single gate decision implementation. It does not interpret
// confidence or evidence limitations as failure, and never invents approval.
func Evaluate(subject, review json.RawMessage) (Receipt, error) {
	input, err := SubjectInput(subject)
	if err != nil {
		return Receipt{}, err
	}
	var core Core
	if json.Unmarshal(review, &core) != nil || core.ReviewedInputSHA256 != Digest(input) {
		return Receipt{}, fmt.Errorf("%w: reviewed input digest differs", ErrBinding)
	}
	issues := len(core.UnsupportedClaims) + len(core.CitationIssues) + len(core.NumericIssues) + len(core.MethodIssues)
	if !core.Approved || issues > 0 || len(core.RequiredCorrections) > 0 {
		return Receipt{}, &Rejected{issues, len(core.RequiredCorrections)}
	}
	encoded, _ := json.Marshal(core) // Keep persisted review-digest field order.
	limits := core.Limitations
	if limits == nil {
		limits = []string{}
	}
	return Receipt{true, Digest(input), Digest(encoded), core.Confidence, len(core.VerifiedClaims), limits}, nil
}
