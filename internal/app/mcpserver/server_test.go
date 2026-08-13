package mcpserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

func TestValidateSaveTransportBoundaries(t *testing.T) {
	base := SaveCommand{Name: "Papers", Namespace: "papers", Enabled: true, Trust: TrustUser, TimeoutSeconds: 30}
	stdio := base
	stdio.Transport, stdio.Command, stdio.Args = TransportStdio, `C:\tools\server.exe`, []string{"--stdio"}
	if err := validateSave(normalizeSave(stdio)); err != nil {
		t.Fatal(err)
	}
	http := base
	http.Transport, http.URL = TransportStreamableHTTP, "https://mcp.example.test/mcp"
	if err := validateSave(normalizeSave(http)); err != nil {
		t.Fatal(err)
	}
	local := base
	local.Transport, local.URL = TransportStreamableHTTP, "http://127.0.0.1:3000/mcp"
	if err := validateSave(normalizeSave(local)); err != nil {
		t.Fatal(err)
	}
	remoteHTTP := base
	remoteHTTP.Transport, remoteHTTP.URL = TransportStreamableHTTP, "http://mcp.example.test/mcp"
	if err := validateSave(normalizeSave(remoteHTTP)); err == nil {
		t.Fatal("remote plaintext HTTP was accepted")
	}
}

type memoryRepository struct{ values map[string]Server }

func (r *memoryRepository) Save(_ context.Context, value Server) error {
	r.values[value.ID] = value
	return nil
}
func (r *memoryRepository) Get(_ context.Context, id string) (Server, error) {
	value, ok := r.values[id]
	if !ok {
		return Server{}, fmt.Errorf("MCP server not found")
	}
	return value, nil
}
func (r *memoryRepository) List(context.Context) ([]Server, error) {
	values := make([]Server, 0, len(r.values))
	for _, value := range r.values {
		values = append(values, value)
	}
	return values, nil
}
func (r *memoryRepository) Delete(_ context.Context, id string) error {
	delete(r.values, id)
	return nil
}
func (r *memoryRepository) UpdateRuntime(_ context.Context, id string, status Status, protocolVersion, serverVersion string, toolCount, resourceCount, promptCount int, lastError string, connectedAt *time.Time, updatedAt time.Time) error {
	value := r.values[id]
	value.Status, value.ProtocolVersion, value.ServerVersion = status, protocolVersion, serverVersion
	value.ToolCount, value.ResourceCount, value.PromptCount = toolCount, resourceCount, promptCount
	value.LastError, value.LastConnectedAt, value.UpdatedAt = lastError, connectedAt, updatedAt
	r.values[id] = value
	return nil
}
func (r *memoryRepository) NamespaceOwner(_ context.Context, namespace string) (string, error) {
	for id, value := range r.values {
		if value.Namespace == namespace {
			return id, nil
		}
	}
	return "", nil
}

func TestServiceStoresSecretValuesAsReferencesAndResolvesThem(t *testing.T) {
	repository := &memoryRepository{values: map[string]Server{}}
	secrets := secretstore.NewMemory()
	service := NewService(repository, nil, secrets)
	value, err := service.Save(context.Background(), SaveCommand{Name: "Local", Namespace: "local", Transport: TransportStdio, Command: "server", SecretValues: map[string]string{"TOKEN": "private"}, Enabled: true, Trust: TrustUser, TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if value.SecretEnv["TOKEN"] == "" || value.SecretEnv["TOKEN"] == "private" {
		t.Fatalf("secret refs = %#v", value.SecretEnv)
	}
	resolved, err := service.ResolveSecretEnv(context.Background(), value)
	if err != nil || resolved["TOKEN"] != "private" {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	if err := service.Delete(context.Background(), value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(context.Background(), value.SecretEnv["TOKEN"]); err == nil {
		t.Fatal("secret remained after server deletion")
	}
}

func TestValidateSaveRejectsSensitiveHeadersAndShellLikeConfigurationMixing(t *testing.T) {
	value := SaveCommand{Name: "Remote", Namespace: "remote", Transport: TransportStreamableHTTP, URL: "https://mcp.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer secret"}, Trust: TrustUser, TimeoutSeconds: 30}
	if err := validateSave(normalizeSave(value)); err == nil {
		t.Fatal("sensitive header was accepted")
	}
	value.Headers = nil
	value.Command = "node"
	if err := validateSave(normalizeSave(value)); err == nil {
		t.Fatal("HTTP config accepted a local command")
	}
}

func TestNormalizeSaveDoesNotTreatArgumentsAsAShellString(t *testing.T) {
	value := normalizeSave(SaveCommand{Name: "Local", Namespace: "local", Transport: TransportStdio, Command: "node", Args: []string{"server.js; rm -rf x"}, Trust: TrustUser, TimeoutSeconds: 30})
	if len(value.Args) != 1 || value.Args[0] != "server.js; rm -rf x" {
		t.Fatalf("args were rewritten: %#v", value.Args)
	}
}

func TestRecoverRuntimeClearsStaleOnlineState(t *testing.T) {
	now := time.Now().UTC()
	repository := &memoryRepository{values: map[string]Server{
		"ready":    {ID: "ready", Status: StatusReady, Enabled: true, LastConnectedAt: &now},
		"disabled": {ID: "disabled", Status: StatusStarting, Enabled: false},
	}}
	service := NewService(repository, nil)
	service.now = func() time.Time { return now.Add(time.Minute) }
	recovered, err := service.RecoverRuntime(context.Background())
	if err != nil || recovered != 2 {
		t.Fatalf("RecoverRuntime() = %d, %v", recovered, err)
	}
	if repository.values["ready"].Status != StatusDisconnected || repository.values["disabled"].Status != StatusDisabled {
		t.Fatalf("values = %#v", repository.values)
	}
}
