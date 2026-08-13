package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/mcpserver"
)

func TestMCPServerRepositoryPersistsConfigurationWithoutSecretValues(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewMCPServerRepository(store.DB())
	now := time.Now().UTC()
	value := mcpserver.Server{ID: "server", Name: "Local", Namespace: "local", Transport: mcpserver.TransportStdio, Command: "node", Args: []string{"server.js"}, Headers: map[string]string{}, Env: map[string]string{"LANG": "C"}, SecretEnv: map[string]string{"TOKEN": "sciaide/mcp/server/token"}, Enabled: true, Trust: mcpserver.TrustUser, TimeoutSeconds: 30, Status: mcpserver.StatusDisconnected, CreatedAt: now, UpdatedAt: now}
	if err := repository.Save(ctx, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SecretEnv["TOKEN"] != "sciaide/mcp/server/token" || loaded.Env["LANG"] != "C" {
		t.Fatalf("loaded=%#v", loaded)
	}
	var persisted string
	if err := store.DB().QueryRowContext(ctx, `SELECT secret_env_json FROM mcp_servers WHERE id='server'`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted == "" {
		t.Fatal("secret reference was not persisted")
	}
}
