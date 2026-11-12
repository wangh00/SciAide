package modelutil

import (
	"errors"
	"github.com/wangh00/SciAide/internal/apperr"
	"strings"
	"testing"
)

func TestExplicitPromptPolicyFailureIsPermanentAndKeepsRawDiagnostic(t *testing.T) {
	message := "Invalid prompt: your prompt was flagged as potentially violating our usage policy. Please try again with a different prompt"
	for _, e := range []error{ClassifyStreamError("invalid_request_error", "invalid_prompt", message, "raw provider payload"), ClassifyStatus(400, []byte(`{"error":{"code":"invalid_prompt","message":"`+message+`"}}`))} {
		var a *apperr.Error
		if !errors.As(e, &a) || a.Code != "MODEL_PROMPT_REJECTED" || a.Retryable || !strings.Contains(a.UserMessage, "不等于研究题目违规") || a.Details == "" {
			t.Fatal(e)
		}
	}
	if StreamErrorRetryable("server_error", "invalid_prompt") {
		t.Fatal("policy error retried")
	}
	var a *apperr.Error
	e := ClassifyStreamError("invalid_request_error", "invalid_prompt", "bad parameter", "raw")
	if !errors.As(e, &a) || a.Code != "MODEL_REQUEST_REJECTED" {
		t.Fatal("generic invalid prompt mislabelled", e)
	}
}
