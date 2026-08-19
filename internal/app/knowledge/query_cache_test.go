package knowledge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
)

func TestQueryEmbeddingCacheIsBoundedAndVersionScoped(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	privateRoot := filepath.Join(workspace, project.PrivateDirectoryName)
	if err := os.MkdirAll(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privateRoot, "project.json"), []byte(`{"version":1,"projectId":"project"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	version := IndexVersion{
		ID: "version", ProjectID: "project", VersionNumber: 1, SchemaVersion: IndexSchemaVersion,
		ParserSchemaVersion: document.SchemaVersion, ChunkingVersion: ChunkingVersion, SearchKind: SearchKind,
		RetrievalEngine: RetrievalEngine, EmbeddingModel: "fixture", EmbeddingDimensions: 2,
		EmbeddingFingerprint: "fingerprint", HybridStrategy: HybridRRF,
		StorageRelativePath: filepath.ToSlash(filepath.Join("cache", "knowledge", "index-v1.db")), Status: IndexReady,
	}
	index, err := openProjectIndex(ctx, project.Project{ID: "project", WorkspacePath: workspace}, version)
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	base := time.Now().UTC()
	for value := 0; value <= queryEmbeddingCacheLimit; value++ {
		if err := index.StoreQueryVector(ctx, fmt.Sprintf("query-%d", value), []float32{1, 0}, base.Add(time.Duration(value)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := index.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_embedding_cache`).Scan(&count); err != nil || count != queryEmbeddingCacheLimit {
		t.Fatalf("cache count = %d, %v", count, err)
	}
	if _, found, err := index.CachedQueryVector(ctx, "query-0", base.Add(1_000*time.Second)); err != nil || found {
		t.Fatalf("oldest cache entry = %v, %v", found, err)
	}
	vector, found, err := index.CachedQueryVector(ctx, "query-512", base.Add(1_001*time.Second))
	if err != nil || !found || len(vector) != 2 {
		t.Fatalf("newest cache entry = %#v, %v, %v", vector, found, err)
	}
	version.EmbeddingFingerprint = "different"
	otherHash := queryEmbeddingHash(version.EmbeddingFingerprint, "query-512")
	var existing int
	if err := index.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM query_embedding_cache WHERE query_hash=?)`, otherHash).Scan(&existing); err != nil || existing != 0 {
		t.Fatalf("different version fingerprint reused cache = %d, %v", existing, err)
	}
}
