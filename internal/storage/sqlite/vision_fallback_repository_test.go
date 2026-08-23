package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/multimodal"
	"github.com/wangh00/SciAide/internal/modelcap"
)

func TestVisionFallbackRepositoryPersistsOnlyNonSecretConfiguration(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "vision-fallback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewVisionFallbackRepository(store.DB())
	now := time.Now().UTC()
	value := multimodal.Channel{
		ID: "custom-channel", Name: "Custom Vision", BaseURL: "https://vision.example/v1",
		ModelID: "vision-model", APIFormat: string(modelcap.ProtocolOpenAIResponses),
		Enabled: true, Priority: 20, TimeoutSeconds: 90, MaxTokens: 2048,
		SecretRef: "sciaide/vision-fallback/custom-channel", CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.Save(ctx, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Get(ctx, value.ID)
	if err != nil || loaded.Name != value.Name || loaded.Protocol() != modelcap.ProtocolOpenAIResponses || loaded.SecretRef != value.SecretRef {
		t.Fatalf("loaded channel = %#v, %v", loaded, err)
	}
	var apiKeyColumns int
	rows, err := store.DB().QueryContext(ctx, `PRAGMA table_info(vision_fallback_channels)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "api_key" {
			apiKeyColumns++
		}
	}
	_ = rows.Close()
	if apiKeyColumns != 0 {
		t.Fatal("vision fallback table persisted an API key column")
	}
	if err := repository.Delete(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
}
