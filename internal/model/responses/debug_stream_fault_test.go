package responses

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func TestStreamFaultObservationKeepsWireAndOriginalClassification(t *testing.T) {
	for _, tc := range []struct {
		name, data, wire, code string
		complete, valid        bool
	}{
		{"single-line", `{"type":"error","message":"bad " quote"}`, "", "MODEL_STREAM_INVALID", true, false},
		{"multiline", "{\n\"type\":broken}", "data: {\r\ndata: \"type\":broken}\r\n\r\n", "MODEL_STREAM_INVALID", true, false},
		{"EOF", `{"delta":"prefix`, "data: {\"delta\":\"prefix", "MODEL_STREAM_INTERRUPTED", false, false},
		{"shape", `{"type":"response.output_text.delta","delta":{}}`, "", "MODEL_STREAM_INVALID", true, true},
		{"percent-byte-fidelity", `{"type":"error","message":"https://x/%2f/%aB"} junk`, "", "MODEL_STREAM_INVALID", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.wire
			if wire == "" {
				wire = "data: " + tc.data + "\n\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, wire)
			}))
			defer server.Close()
			for _, enabled := range []bool{false, true} {
				client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
				var captured []DebugStreamFault
				if enabled {
					client.SetDebugStreamFaultObserver(func(v DebugStreamFault) { captured = append(captured, v) })
				}
				s, err := client.Stream(context.Background(), model.ChatRequest{})
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.Recv()
				_ = s.Close()
				var appErr *apperr.Error
				if !errors.As(err, &appErr) || appErr.Code != tc.code || appErr.Retryable != !tc.complete {
					t.Fatal("observation changed classification", err)
				}
				if !enabled {
					if client.debugStreamObserver != nil || len(captured) != 0 {
						t.Fatal("wire retention enabled by default")
					}
					continue
				}
				if len(captured) != 1 {
					t.Fatal("missing or duplicate fault observation")
				}
				f := captured[0]
				data, _ := base64.StdEncoding.DecodeString(f.DataBase64)
				raw, _ := base64.StdEncoding.DecodeString(f.WireTailBase64)
				if string(data) != tc.data || string(raw) != wire || f.EventComplete != tc.complete || f.JSONValid != tc.valid || f.DataSHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) || f.WireTailSHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) || f.DataTruncated || f.DataRedacted || f.WireTailPartial || f.WireTailRedacted {
					t.Fatal("wire/data evidence changed")
				}
				if strings.Contains(appErr.Details, "bad ") || strings.Contains(appErr.Details, f.DataBase64) {
					t.Fatal("private evidence entered public diagnostics")
				}
			}
		})
	}
}

func TestStreamFaultEvidenceRedactionBoundsAndBinaryFidelity(t *testing.T) {
	var f DebugStreamFault
	secret := "KEY_SENTINEL"
	data := []byte(strings.Repeat("a", streamFaultCaptureBytes-5) + secret + strings.Repeat("b", streamFaultCaptureBytes))
	wire := append([]byte{0xff, 0xfe}, []byte("data: "+string(data)+"\r\n\r\n")...)
	trace := &streamWireTrace{ReadCloser: io.NopCloser(strings.NewReader(string(wire)))}
	if _, err := io.Copy(io.Discard, trace); err != nil {
		t.Fatal(err)
	}
	debugStreamFaultObserver(func(v DebugStreamFault) { f = v }, []byte(secret), nil)(data, true, trace)
	clean, _ := base64.StdEncoding.DecodeString(f.DataBase64)
	tail, _ := base64.StdEncoding.DecodeString(f.WireTailBase64)
	if len(clean) > streamFaultCaptureBytes || len(tail) > streamFaultCaptureBytes || !f.DataTruncated || !f.WireTailPartial || !f.DataRedacted || !f.WireTailRedacted || strings.Contains(string(clean), secret) || strings.Contains(string(tail), secret) {
		t.Fatal("capture bound or pre-truncation redaction failed")
	}
	binary := []byte{'{', 0xff, 0xfe, '}'}
	trace = &streamWireTrace{ReadCloser: io.NopCloser(strings.NewReader(string(binary)))}
	_, _ = io.Copy(io.Discard, trace)
	debugStreamFaultObserver(func(v DebugStreamFault) { f = v }, nil, nil)(binary, true, trace)
	clean, _ = base64.StdEncoding.DecodeString(f.DataBase64)
	if string(clean) != string(binary) || f.DataRedacted {
		t.Fatal("invalid UTF-8 evidence was re-encoded")
	}
	large := strings.Repeat("x", streamFaultWireWindow*3)
	trace = &streamWireTrace{ReadCloser: io.NopCloser(strings.NewReader(large))}
	_, _ = io.Copy(io.Discard, trace)
	if window, read := trace.snapshot(); len(window) != streamFaultWireWindow || read != int64(len(large)) {
		t.Fatal("wire ring is not bounded")
	}
}

func TestValidStreamDoesNotEmitPrivateFaultCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"okay\"}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil)
	count := 0
	client.SetDebugStreamFaultObserver(func(DebugStreamFault) { count++ })
	s, err := client.Stream(context.Background(), model.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for {
		if _, err := s.Recv(); err != nil {
			break
		}
	}
	if count != 0 {
		t.Fatal("valid response captured")
	}
}
