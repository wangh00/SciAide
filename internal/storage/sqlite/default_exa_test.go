package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDefaultExaIsOneTimeAndUserOwned(t *testing.T) {
	ctx := context.Background()
	for _, action := range []string{"delete", "disable", "edit", "restart"} {
		t.Run(action, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.db")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			repo := NewMCPServerRepository(s.DB())
			v, err := repo.Get(ctx, "builtin-exa-search")
			if err != nil || !v.AutoStart || !v.Enabled || v.Trust != "user_trusted" || v.URL != "https://mcp.exa.ai/mcp?tools=web_search_exa" || v.TimeoutSeconds != 30 {
				t.Fatal(v, err)
			}
			switch action {
			case "delete":
				err = repo.Delete(ctx, v.ID)
			case "disable":
				v.Enabled, v.AutoStart = false, false
				err = repo.Save(ctx, v)
			case "edit":
				v.Name, v.URL = "Mine", "https://example.test/mcp"
				err = repo.Save(ctx, v)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			values, err := NewMCPServerRepository(s.DB()).List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if action == "delete" {
				if len(values) != 0 {
					t.Fatal("deleted default resurrected")
				}
				return
			}
			if len(values) != 1 || values[0].Enabled != v.Enabled || values[0].AutoStart != v.AutoStart || values[0].URL != v.URL || values[0].Name != v.Name {
				t.Fatal(values)
			}
		})
	}
}

func TestDefaultExaMigrationPreservesExistingConfigurations(t *testing.T) {
	ctx := context.Background()
	for _, existing := range []struct{ ns, url string }{{"exa", "https://other.test/mcp"}, {"my_exa", "https://mcp.exa.ai/mcp"}, {"my_exa", "https://mcp.exa.ai/mcp?tools=web_search_exa"}} {
		db, err := openDatabase(ctx, filepath.Join(t.TempDir(), "old.db"), false)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrateToVersion(ctx, db, 100); err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`INSERT INTO mcp_servers(id,name,namespace,transport,url,created_at,updated_at) VALUES('user','User',?,'streamable_http',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, existing.ns, existing.url)
		if err != nil {
			t.Fatal(err)
		}
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
		values, err := NewMCPServerRepository(db).List(ctx)
		if err != nil || len(values) != 1 || values[0].ID != "user" || values[0].AutoStart || values[0].Trust != "untrusted" {
			t.Fatal(values, err)
		}
		db.Close()
	}
}
