package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

func newImportService() (*Service, *memoryRepository) {
	repository := &memoryRepository{values: map[string]Server{}}
	return NewService(repository, nil, secretstore.NewMemory()), repository
}

func TestImportCompatibleChromeDevToolsConfiguration(t *testing.T) {
	service, _ := newImportService()
	result, err := service.Import(context.Background(), ImportCommand{JSON: `{
		"mcpServers": {
			"chrome-devtools": {
				"command": "npx",
				"args": ["-y", "chrome-devtools-mcp@latest"]
			}
		}
	}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 || len(result.Imported) != 1 {
		t.Fatalf("result = %#v", result)
	}
	server := result.Imported[0]
	if server.Name != "chrome-devtools" || server.Namespace != "chrome-devtools" || server.Transport != TransportStdio || server.Command != "npx" {
		t.Fatalf("server = %#v", server)
	}
	if len(server.Args) != 2 || server.Args[0] != "-y" || server.Args[1] != "chrome-devtools-mcp@latest" {
		t.Fatalf("args = %#v", server.Args)
	}
	if !server.Enabled || server.AutoStart || server.Trust != TrustUntrusted || server.Status != StatusDisconnected {
		t.Fatalf("unsafe import state = %#v", server)
	}
}

func TestImportMultipleServersAndHTTPAliases(t *testing.T) {
	service, _ := newImportService()
	result, err := service.Import(context.Background(), ImportCommand{JSON: `{
		"mcpServers": {
			"local": {"type":"stdio", "command":"node", "args":["server.js"], "disabled":true},
			"remote": {"transport":"streamable-http", "url":"https://mcp.example.test/mcp", "headers":{"X-Tenant":"lab"}}
		}
	}`})
	if err != nil || len(result.Errors) != 0 || len(result.Imported) != 2 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	byName := map[string]Server{}
	for _, server := range result.Imported {
		byName[server.Name] = server
	}
	if byName["local"].Enabled || byName["local"].Status != StatusDisabled {
		t.Fatalf("disabled local = %#v", byName["local"])
	}
	if byName["remote"].Transport != TransportStreamableHTTP || byName["remote"].Headers["X-Tenant"] != "lab" {
		t.Fatalf("remote = %#v", byName["remote"])
	}
}

func TestImportMovesSensitiveEnvironmentValuesIntoSecretStore(t *testing.T) {
	repository := &memoryRepository{values: map[string]Server{}}
	secrets := secretstore.NewMemory()
	service := NewService(repository, nil, secrets)
	result, err := service.Import(context.Background(), ImportCommand{JSON: `{
		"mcpServers": {"zotero": {"command":"npx", "env":{"LANG":"C", "ZOTERO_API_KEY":"private"}}}
	}`})
	if err != nil || len(result.Errors) != 0 || len(result.Imported) != 1 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	server := result.Imported[0]
	if server.Env["LANG"] != "C" {
		t.Fatalf("env = %#v", server.Env)
	}
	if _, exposed := server.Env["ZOTERO_API_KEY"]; exposed || server.SecretEnv["ZOTERO_API_KEY"] == "" || server.SecretEnv["ZOTERO_API_KEY"] == "private" {
		t.Fatalf("secret classification = env %#v refs %#v", server.Env, server.SecretEnv)
	}
	resolved, err := service.ResolveSecretEnv(context.Background(), server)
	if err != nil || resolved["ZOTERO_API_KEY"] != "private" {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
}

func TestImportReportsInvalidEntriesWithoutLeakingSecretOrBlockingValidEntries(t *testing.T) {
	service, _ := newImportService()
	secret := "do-not-leak-this-value"
	result, err := service.Import(context.Background(), ImportCommand{JSON: `{
		"mcpServers": {
			"bad": {"command":"node", "url":"https://mcp.example.test/mcp", "env":{"TOKEN":"` + secret + `"}},
			"good": {"command":"node", "args":["server.js"]}
		}
	}`})
	if err != nil || len(result.Imported) != 1 || result.Imported[0].Name != "good" || len(result.Errors) != 1 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if strings.Contains(result.Errors[0].Message, secret) {
		t.Fatalf("secret leaked in error: %q", result.Errors[0].Message)
	}
}

func TestImportRejectsMissingRootAndInvalidArgumentTypes(t *testing.T) {
	service, _ := newImportService()
	if _, err := service.Import(context.Background(), ImportCommand{JSON: `{}`}); err == nil {
		t.Fatal("missing mcpServers was accepted")
	}
	result, err := service.Import(context.Background(), ImportCommand{JSON: `{"mcpServers":{"bad":{"command":"node","args":"--stdio"}}}`})
	if err != nil || len(result.Imported) != 0 || len(result.Errors) != 1 {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestImportDoesNotOverwriteExistingNameAndUsesStableNamespaceForCollision(t *testing.T) {
	service, repository := newImportService()
	repository.values["existing"] = Server{ID: "existing", Name: "Existing", Namespace: "same-name"}
	result, err := service.Import(context.Background(), ImportCommand{JSON: `{
		"mcpServers": {
			"existing": {"command":"ignored"},
			"same name": {"command":"node"}
		}
	}`})
	if err != nil || len(result.Imported) != 1 || len(result.Errors) != 1 {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if result.Imported[0].Namespace != "same-name-f03c3f37" {
		t.Fatalf("namespace = %q", result.Imported[0].Namespace)
	}
}

func TestAvailableImportNamespaceHandlesRepeatedOccupiedHashCandidates(t *testing.T) {
	occupied := map[string]struct{}{
		"same-name":            {},
		"same-name-f03c3f37":   {},
		"same-name-f03c3f37-1": {},
	}
	if got := availableImportNamespace("same name", occupied); got != "same-name-f03c3f37-2" {
		t.Fatalf("namespace = %q", got)
	}
}

func TestImportRejectsConflictingTransportSettings(t *testing.T) {
	service, _ := newImportService()
	result, err := service.Import(context.Background(), ImportCommand{JSON: `{
		"mcpServers": {
			"both": {"command":"node", "url":"https://mcp.example.test/mcp"},
			"mismatch": {"type":"http", "command":"node"}
		}
	}`})
	if err != nil || len(result.Imported) != 0 || len(result.Errors) != 2 {
		t.Fatalf("result = %#v, %v", result, err)
	}
}
