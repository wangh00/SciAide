package embedding_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

type configRepository struct{ value embedding.Config }

func (r *configRepository) Get(context.Context) (embedding.Config, error) { return r.value, nil }
func (r *configRepository) Save(_ context.Context, value embedding.Config) error {
	r.value = value
	return nil
}

func TestHTTPClientOrdersAndNormalizesVectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request = %s, authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 1, "embedding": []float64{0, 2}},
			map[string]any{"index": 0, "embedding": []float64{3, 4}},
		}})
	}))
	defer server.Close()

	vectors, err := embedding.NewHTTPClient().Embed(context.Background(), embedding.Config{BaseURL: server.URL + "/v1", ModelID: "fixture", TimeoutSeconds: 5}, []byte("secret"), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 2 || vectors[0][0] < 0.59 || vectors[0][0] > 0.61 || vectors[1][1] != 1 {
		t.Fatalf("vectors = %#v", vectors)
	}
}

func TestServiceDefaultsOffAndVerifiesBeforeEnable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 0, 0}}}})
	}))
	defer server.Close()

	repository := &configRepository{value: embedding.Config{SecretRef: embedding.SecretRef, TimeoutSeconds: 30, UpdatedAt: time.Now().UTC()}}
	secrets := secretstore.NewMemory()
	service := embedding.NewService(repository, secrets, embedding.NewHTTPClient())
	if _, enabled, err := service.Current(context.Background()); err != nil || enabled {
		t.Fatalf("default current = %v, %v", enabled, err)
	}
	value, err := service.Save(context.Background(), embedding.SaveCommand{Enabled: true, BaseURL: server.URL, ModelID: "fixture", APIKey: "key-1234", TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !value.Enabled || value.Dimensions != 3 || !value.SecretConfigured || !strings.HasSuffix(value.SecretMasked, "1234") {
		t.Fatalf("saved config = %#v", value)
	}
	identity, enabled, err := service.Current(context.Background())
	if err != nil || !enabled || identity.Dimensions != 3 || identity.Fingerprint == "" {
		t.Fatalf("active identity = %#v, %v, %v", identity, enabled, err)
	}
}
