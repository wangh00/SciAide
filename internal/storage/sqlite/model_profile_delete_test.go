package sqlite

import (
	"context"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"path/filepath"
	"testing"
	"time"
)

func TestDeleteModelProfilePreservesHistoricalIdentityButCannotBeUsed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := NewModelProfileRepository(store.DB())
	now := time.Now().UTC()
	value := modelprofile.Profile{ID: "profile", Name: "source", ProviderType: modelprofile.ProviderOpenAICompatible, BaseURL: "https://example.test/v1", ModelID: "model", SecretRef: "secret", TimeoutSeconds: 60, CustomHeaders: map[string]string{}, Enabled: true, IsDefault: true, CreatedAt: now, UpdatedAt: now}
	if err := repo.Save(ctx, value); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `CREATE TABLE deletion_history(profile_id TEXT REFERENCES model_profiles(id) ON DELETE RESTRICT); INSERT INTO deletion_history VALUES ('profile')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, value.ID); err == nil {
		t.Fatal("deleted profile still usable")
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("visible deleted profile: %v %v", list, err)
	}
	if err := repo.Save(ctx, value); err == nil {
		t.Fatal("stale save resurrected profile")
	}
	var name string
	if err := store.DB().QueryRowContext(ctx, `SELECT p.name FROM deletion_history h JOIN model_profiles p ON p.id=h.profile_id`).Scan(&name); err != nil || name != "source" {
		t.Fatalf("history lost: %s %v", name, err)
	}
}
