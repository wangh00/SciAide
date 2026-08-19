package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/embedding"
)

func TestEmbeddingRepositoryDefaultsOffAndPersistsIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "embedding.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewEmbeddingRepository(store.DB())
	value, err := repository.Get(ctx)
	if err != nil || value.Enabled || value.TimeoutSeconds != 30 || value.SecretRef != embedding.SecretRef {
		t.Fatalf("default config = %#v, %v", value, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	value.Enabled = true
	value.BaseURL = "http://127.0.0.1:8000/v1"
	value.ModelID = "fixture"
	value.Dimensions = 384
	value.Fingerprint = "fingerprint"
	value.LastTestedAt = &now
	value.UpdatedAt = now
	if err := repository.Save(ctx, value); err != nil {
		t.Fatal(err)
	}
	saved, err := repository.Get(ctx)
	if err != nil || !saved.Enabled || saved.ModelID != "fixture" || saved.Dimensions != 384 || saved.Fingerprint != "fingerprint" || saved.LastTestedAt == nil {
		t.Fatalf("saved config = %#v, %v", saved, err)
	}
}
