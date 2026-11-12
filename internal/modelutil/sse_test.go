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
		classified := ClassifySSEJSONError("fixture", "invalid fixture", []byte(tc.data), err, false)
		if IsRetryable(classified) != tc.retryable {
			t.Fatalf("data=%q classification=%v", tc.data, classified)
		}
		var appErr *apperr.Error
		if !errors.As(classified, &appErr) || !strings.Contains(appErr.Details, "Provider payload:") {
			t.Fatal("safe provider metadata not retained")
		}
	}
	if ClassifySSEReadError(context.Canceled) != context.Canceled || ClassifySSEReadError(context.DeadlineExceeded) != context.DeadlineExceeded || !IsRetryable(ClassifySSEReadError(io.ErrUnexpectedEOF)) {
		t.Fatal("cancellation/read failure classification changed")
	}
}

func TestSSEDecoderRetainsExplicitEventChannelWithoutBleeding(t *testing.T) {
	wire := "event: delta\ndata: first\nevent: error\ndata: second\n\n" +
		"data: ordinary\n\n" +
		"event: error\n\ndata: no-inheritance\n\n" +
		"event: error\nevent:\ndata: reset\n\n" +
		"event:  error\ndata: preserves-extra-space\n\n" +
		"event: terminal\ndata: eof"
	d := NewSSEDecoder(strings.NewReader(wire), 128)
	for _, want := range []struct {
		data, channel string
		complete      bool
	}{
		{"first\nsecond", "error", true},
		{"ordinary", "", true},
		{"no-inheritance", "", true},
		{"reset", "", true},
		{"preserves-extra-space", " error", true},
		{"eof", "terminal", false},
	} {
		if !d.Scan() || d.Text() != want.data || d.EventType() != want.channel || d.EventComplete() != want.complete {
			t.Fatalf("data=%q channel=%q complete=%v err=%v", d.Text(), d.EventType(), d.EventComplete(), d.Err())
		}
	}
	if d.Scan() || d.Err() != nil || d.EventType() != "" {
		t.Fatal("channel leaked past EOF")
	}
}

func TestSSECoalescedErrorMetadataPreservesOriginalDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, wire, prefix, prior, terminal string
		ok                                  bool
	}{
		{"explicit-transition", "event: response.output_item.done\ndata: prefix\nevent: error\ndata: terminal\n\n", "prefix", "response.output_item.done", "terminal", true},
		{"CRLF", "event: delta\r\ndata: prefix\r\nevent: error\r\ndata: terminal\r\n\r\n", "prefix", "delta", "terminal", true},
		{"ordinary-error", "event: error\ndata: terminal\n\n", "", "", "", false},
		{"error-label-before-both", "event: error\ndata: prefix\ndata: terminal\n\n", "", "", "", false},
		{"no-terminal-data", "event: delta\ndata: prefix\nevent: error\n\n", "", "", "", false},
		{"no-prior-channel", "data: prefix\nevent: error\ndata: terminal\n\n", "", "", "", false},
		{"separate-dispatches", "event: delta\ndata: prefix\n\nevent: error\ndata: terminal\n\n", "", "", "", false},
		{"EOF", "event: delta\ndata: prefix\nevent: error\ndata: terminal", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewSSEDecoder(strings.NewReader(tc.wire), 128)
			if !d.Scan() {
				t.Fatal(d.Err())
			}
			original := d.Text()
			prefix, prior, terminal, ok := d.CoalescedErrorFields()
			if ok != tc.ok || prefix != tc.prefix || prior != tc.prior || terminal != tc.terminal || d.Text() != original {
				t.Fatal("channel metadata changed SSE dispatch", ok)
			}
			if ok && original != prefix+"\n"+terminal {
				t.Fatal("lost original joined data")
			}
			if tc.name == "separate-dispatches" {
				if !d.Scan() {
					t.Fatal("lost ordinary terminal")
				}
				if _, _, _, ok = d.CoalescedErrorFields(); ok {
					t.Fatal("preceding block leaked")
				}
			}
		})
	}
}
