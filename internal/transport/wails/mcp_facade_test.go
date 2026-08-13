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

func TestPublicMCPBatchResultDoesNotExposeSecretReferences(t *testing.T) {
	public := publicMCPBatchResult(mcpserver.BatchResult{Items: []mcpserver.BatchItemResult{{
		Server: mcpserver.Server{SecretEnv: map[string]string{"TOKEN": "sciaide/mcp/server/TOKEN"}},
	}}})
	if len(public.Items[0].Server.SecretEnv) != 0 || !public.Items[0].Server.SecretConfigured["TOKEN"] {
		t.Fatalf("batch result exposed secret references: %#v", public.Items[0].Server)
	}
}
