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
