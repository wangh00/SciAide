package modelutil

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestSSEDecoderBoundariesAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		want       []string
	}{
		{"multiline", ": heartbeat\r\nevent: delta\r\ndata: {\r\ndata:  \"text\":\"same value\"}\r\n\r\n", []string{"{\n \"text\":\"same value\"}"}},
		{"separate", "id: 2\ndata: one\n\ndata: two\n\n", []string{"one", "two"}},
		{"data without colon", "data\n\ndata:\n\n", []string{"", ""}},
		{"EOF compatibility", "data: complete", []string{"complete"}},
		{"empty stream", ":heartbeat\n\nevent: ping\n\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoder := NewSSEDecoder(strings.NewReader(tc.wire), 128)
			var got []string
			for decoder.Scan() {
				got = append(got, decoder.Text())
			}
			if decoder.Err() != nil || strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") || len(got) != len(tc.want) {
				t.Fatalf("got=%q err=%v want=%q", got, decoder.Err(), tc.want)
			}
		})
	}
	for _, wire := range []string{"data: " + strings.Repeat("x", 130), "data: " + strings.Repeat("x", 80) + "\ndata: " + strings.Repeat("y", 80) + "\n\n"} {
		decoder := NewSSEDecoder(strings.NewReader(wire), 128)
		if decoder.Scan() || decoder.Err() == nil || IsRetryable(ClassifySSEReadError(decoder.Err())) {
			t.Fatal("oversized physical line or combined event was accepted/retried")
		}
	}
}

func TestSSEJSONOnlyTruncatedSyntaxIsRetryable(t *testing.T) {
	for _, tc := range []struct {
		data      string
		retryable bool
	}{
		{`{"delta":"`, true},
		{"{\"delta\":\"中国", true},
		{`{"delta":`, true},
		{`{"delta":"partial"`, true},
		{`{"delta":broken}`, false},
		{`{"delta":"a` + "\n" + `b"}`, false},
		{`{"delta":"complete"} junk`, false},
	} {
		var value map[string]any
		err := json.Unmarshal([]byte(tc.data), &value)
		if err == nil {
			t.Fatal("invalid test case")
		}
		classified := ClassifySSEJSONError("fixture", "invalid fixture", []byte(tc.data), err)
		if IsRetryable(classified) != tc.retryable {
			t.Fatalf("data=%q classification=%v", tc.data, classified)
		}
		var appErr *apperr.Error
		if !errors.As(classified, &appErr) || !strings.Contains(appErr.Details, "Provider payload:") {
			t.Fatal("raw provider data not retained")
		}
	}
	if ClassifySSEReadError(context.Canceled) != context.Canceled || ClassifySSEReadError(context.DeadlineExceeded) != context.DeadlineExceeded || !IsRetryable(ClassifySSEReadError(io.ErrUnexpectedEOF)) {
		t.Fatal("cancellation/read failure classification changed")
	}
}
