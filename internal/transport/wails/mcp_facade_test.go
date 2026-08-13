package wails

import (
	"testing"

	"github.com/wangh00/SciAide/internal/app/mcpserver"
)

func TestPublicMCPServerDoesNotExposeSecretReferences(t *testing.T) {
	public := publicMCPServer(mcpserver.Server{
		SecretEnv: map[string]string{"API_KEY": "sciaide/mcp/server/API_KEY"},
	})
	if len(public.SecretEnv) != 0 {
		t.Fatalf("secret references exposed: %#v", public.SecretEnv)
	}
	if !public.SecretConfigured["API_KEY"] {
		t.Fatalf("configured secret marker missing: %#v", public.SecretConfigured)
	}
}
