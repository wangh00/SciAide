package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func validReviewGateArguments(t *testing.T) json.RawMessage {
	t.Helper()
	subject := json.RawMessage(`{"summary":"frozen result"}`)
	envelope, _ := json.Marshal(map[string]json.RawMessage{"context": subject})
	digest := sha256.Sum256(envelope)
	value, _ := json.Marshal(map[string]any{
		"subject": subject,
		"review": map[string]any{
			"approved": true, "reviewedInputSha256": hex.EncodeToString(digest[:]), "verifiedClaims": []string{"claim"},
			"unsupportedClaims": []string{}, "citationIssues": []string{}, "numericIssues": []string{}, "methodIssues": []string{},
			"requiredCorrections": []string{}, "confidence": "high", "limitations": []string{},
		},
	})
	return value
}

func TestResearchWorkflowReviewGateAcceptsExactReviewedSnapshot(t *testing.T) {
	result, err := NewResearchWorkflowReviewGate().Invoke(context.Background(), tool.Invocation{Arguments: validReviewGateArguments(t)})
	if err != nil || result.Status != tool.ResultSuccess || len(result.Structured) == 0 {
		t.Fatalf("review gate = %#v, %v", result, err)
	}
}

func TestResearchWorkflowReviewGateAcceptsCompleteStageSnapshot(t *testing.T) {
	subject := json.RawMessage(`{"context":{"summary":"frozen result"},"evidenceScreening":{"strength":"limited"}}`)
	digest := sha256.Sum256(subject)
	value, _ := json.Marshal(map[string]any{
		"subject": subject,
		"review": map[string]any{
			"approved": true, "reviewedInputSha256": hex.EncodeToString(digest[:]), "verifiedClaims": []string{"claim"},
			"unsupportedClaims": []string{}, "citationIssues": []string{}, "numericIssues": []string{}, "methodIssues": []string{},
			"requiredCorrections": []string{}, "confidence": "high", "limitations": []string{},
		},
	})
	result, err := NewResearchWorkflowReviewGate().Invoke(context.Background(), tool.Invocation{Arguments: value})
	if err != nil || result.Status != tool.ResultSuccess {
		t.Fatalf("review gate with complete stage snapshot = %#v, %v", result, err)
	}
}

func TestResearchWorkflowReviewGateRejectsWrongHashAndUnresolvedIssues(t *testing.T) {
	var arguments map[string]any
	_ = json.Unmarshal(validReviewGateArguments(t), &arguments)
	review := arguments["review"].(map[string]any)
	review["reviewedInputSha256"] = strings.Repeat("0", 64)
	wrongHash, _ := json.Marshal(arguments)
	if _, err := NewResearchWorkflowReviewGate().Invoke(context.Background(), tool.Invocation{Arguments: wrongHash}); err == nil {
		t.Fatal("review gate accepted a review for another snapshot")
	}
	_ = json.Unmarshal(validReviewGateArguments(t), &arguments)
	review = arguments["review"].(map[string]any)
	review["unsupportedClaims"] = []string{"not verified"}
	unresolved, _ := json.Marshal(arguments)
	if _, err := NewResearchWorkflowReviewGate().Invoke(context.Background(), tool.Invocation{Arguments: unresolved}); err == nil {
		t.Fatal("review gate accepted unresolved claims")
	} else if !strings.Contains(err.Error(), "1 条分类意见，并归纳为 0 条必须修正项") {
		t.Fatalf("review gate issue count = %v", err)
	}

	_ = json.Unmarshal(validReviewGateArguments(t), &arguments)
	review = arguments["review"].(map[string]any)
	review["methodIssues"] = []string{"robust sensitivity analysis is missing"}
	review["requiredCorrections"] = []string{"add HC3 analysis"}
	unresolved, _ = json.Marshal(arguments)
	if _, err := NewResearchWorkflowReviewGate().Invoke(context.Background(), tool.Invocation{Arguments: unresolved}); err == nil {
		t.Fatal("review gate accepted unresolved method issue")
	} else if !strings.Contains(err.Error(), "1 条分类意见，并归纳为 1 条必须修正项") {
		t.Fatalf("review gate method issue count = %v", err)
	}
}
