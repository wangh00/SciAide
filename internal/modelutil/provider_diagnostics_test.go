package modelutil

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

func TestMalformedProviderDiagnosticsDoNotRetainUntrustedContent(t *testing.T) {
	for _, body := range []string{
		`{"api_key":"PRIVATE_SENTINEL","response":}`,
		`{"authorization":"PRIVATE_SENTINEL`,
		`{"reasoning":"PRIVATE_SENTINEL","response":}`,
		`{"thinking":{"text":"PRIVATE_SENTINEL"},"output":`,
		`{"encrypted_content":"PRIVATE_SENTINEL","signature":"PRIVATE_SENTINEL"`,
		`{"api\u005fkey":"PRIVATE_SENTINEL","response":}`,
		`{"api_key":"PRIVATE_SENTINEL\\\"escaped","response":}`,
		"<html>PRIVATE_SENTINEL</html>",
		"{\"reasoning\":\"私密PRIVATE_SENTINEL\ntext\"}",
	} {
		t.Run(fmt.Sprintf("%x", sha256.Sum256([]byte(body)))[:12], func(t *testing.T) {
			detail := ProviderErrorDetails("fixture", 502, []byte(body))
			if strings.Contains(detail, "PRIVATE_SENTINEL") || !strings.Contains(detail, fmt.Sprintf("%x", sha256.Sum256([]byte(body)))) || !strings.Contains(detail, "502") || !strings.Contains(detail, "payloadBytes") {
				t.Fatal("unparseable provider content exposed or safe fingerprint lost", detail)
			}
		})
	}
}

func TestStreamDiagnosticsOmitNativeContentAndUntrustedMessages(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.output_item.done","item":{"type":"reasoning","summary":[{"text":"PRIVATE_SENTINEL"}],"content":[{"text":"PRIVATE_SENTINEL"}]}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","arguments":"PRIVATE_SENTINEL"},"id":"PRIVATE_SENTINEL"}`,
		`{"type":"error","code":"PRIVATE_SENTINEL","message":"PRIVATE_SENTINEL"}`,
		`{"type":"error","error":{"type":"upstream_error","code":"stream_read_error","message":"PRIVATE_SENTINEL"}}`,
	} {
		detail := StreamEventErrorDetails("fixture", []byte(body))
		for _, input := range []string{detail, body, "Source: HTTP response\nProvider payload:\n" + body} {
			got := SanitizeProviderDiagnostic(input)
			if strings.Contains(got, "PRIVATE_SENTINEL") || !strings.Contains(got, fmt.Sprintf("%x", sha256.Sum256([]byte(body)))) {
				t.Fatal("native content leaked or original fingerprint lost", got)
			}
		}
		err := ClassifyStreamError("upstream_error", "stream_read_error", "PRIVATE_SENTINEL", detail)
		public := apperr.Public(err)
		if strings.Contains(public.Message+public.Details+err.Error(), "PRIVATE_SENTINEL") || !public.Retryable {
			t.Fatal("upstream message escaped classification", public)
		}
	}
	malicious := `Source: PRIVATE_SENTINEL
Provider payload:
{"payloadOmitted":true,"payloadBytes":12,"payloadSha256":"PRIVATE_SENTINEL","jsonError":"PRIVATE_SENTINEL","jsonByteOffset":"PRIVATE_SENTINEL","eventBoundary":"PRIVATE_SENTINEL","providerCode":"PRIVATE_SENTINEL","PRIVATE_SENTINEL":"PRIVATE_SENTINEL"}`
	if got := SanitizeProviderDiagnostic(malicious); strings.Contains(got, "PRIVATE_SENTINEL") {
		t.Fatal("forged metadata leaked", got)
	}
}

func TestProtocolDecodeDiagnosticPreservesBoundaryOffsetWithoutCauseLeak(t *testing.T) {
	data := []byte(`{"PRIVATE_SENTINEL":[],"output_index":"PRIVATE_SENTINEL"}`)
	var event struct {
		OutputIndex int `json:"output_index"`
	}
	err := json.Unmarshal(data, &event)
	if err == nil {
		t.Fatal("expected type mismatch")
	}
	classified := ClassifySSEJSONError("fixture", "协议错误。", data, err, true)
	public := apperr.Public(classified)
	for _, detail := range []string{public.Details, SanitizeProviderDiagnostic(public.Details)} {
		if !strings.Contains(detail, `"jsonError": "type_mismatch"`) || !strings.Contains(detail, `"eventBoundary": "complete"`) || !strings.Contains(detail, "jsonByteOffset") || !strings.Contains(detail, fmt.Sprintf("%x", sha256.Sum256(data))) || strings.Contains(detail, "PRIVATE_SENTINEL") {
			t.Fatal("unsafe/incomplete decode diagnostic", detail)
		}
	}
	if strings.Contains(classified.Error(), "PRIVATE_SENTINEL") || public.Retryable {
		t.Fatal("raw parse error escaped or type error was retried", classified)
	}
}
