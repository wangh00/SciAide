package modelutil

import (
	"net/http"
	"testing"
)

func TestReasoningControlRejected(t *testing.T) {
	if !ReasoningControlRejected(http.StatusBadRequest, []byte(`{"error":{"message":"Unsupported parameter: reasoning_effort"}}`)) {
		t.Fatal("expected reasoning parameter rejection")
	}
	if !ReasoningControlRejected(http.StatusUnprocessableEntity, []byte(`{"message":"thinking is not supported by this model"}`)) {
		t.Fatal("expected thinking rejection")
	}
	if ReasoningControlRejected(http.StatusBadRequest, []byte(`{"error":{"message":"Invalid tools schema"}}`)) {
		t.Fatal("tool schema error must not trigger reasoning fallback")
	}
	if ReasoningControlRejected(http.StatusUnauthorized, []byte(`{"error":{"message":"reasoning_effort denied"}}`)) {
		t.Fatal("non-client capability error must not trigger fallback")
	}
}

func TestClassifyReasoningRejectionSeparatesValueAndControl(t *testing.T) {
	if got := ClassifyReasoningRejection(http.StatusUnprocessableEntity, []byte(`{"error":{"message":"Invalid value for reasoning_effort: max; supported values are low, medium, high"}}`)); got != ReasoningRejectionValue {
		t.Fatalf("value rejection = %v", got)
	}
	if got := ClassifyReasoningRejection(http.StatusBadRequest, []byte(`{"error":{"message":"Unknown parameter: reasoning_effort"}}`)); got != ReasoningRejectionControl {
		t.Fatalf("control rejection = %v", got)
	}
	if got := ClassifyReasoningRejection(http.StatusBadRequest, []byte(`{"error":{"message":"reasoning_effort max is not supported; use high"}}`)); got != ReasoningRejectionValue {
		t.Fatalf("named level rejection = %v", got)
	}
	for _, test := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"error":{"message":"Invalid reasoning_effort"}}`},
		{http.StatusTooManyRequests, `{"error":{"message":"reasoning_effort rate limited"}}`},
		{http.StatusBadRequest, `{"error":{"message":"Invalid tools schema"}}`},
		{http.StatusBadRequest, `{"error":{"message":"context length exceeded while using reasoning_effort"}}`},
	} {
		if got := ClassifyReasoningRejection(test.status, []byte(test.body)); got != ReasoningRejectionNone {
			t.Fatalf("unrelated rejection %d %s = %v", test.status, test.body, got)
		}
	}
}
