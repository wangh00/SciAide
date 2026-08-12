package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactingHandlerMasksKeysAndKnownValues(t *testing.T) {
	const secret = "sk-test-secret"
	var output bytes.Buffer
	base := slog.NewJSONHandler(&output, nil)
	logger := slog.New(NewRedactingHandler(base, []string{secret}))
	logger.InfoContext(context.Background(), "request "+secret,
		"api_key", secret,
		"detail", "provider rejected "+secret,
	)
	text := output.String()
	if strings.Contains(text, secret) {
		t.Fatalf("secret leaked in log: %s", text)
	}
	if !strings.Contains(text, masked) {
		t.Fatalf("expected redaction marker in log: %s", text)
	}
}
