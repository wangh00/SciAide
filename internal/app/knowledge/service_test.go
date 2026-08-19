package knowledge_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/document"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

type semanticProvider struct {
	mu    sync.Mutex
	fail  bool
	calls int
}

func (*semanticProvider) Current(context.Context) (embedding.Identity, bool, error) {
	return embedding.Identity{ModelID: "fixture-embedding", Dimensions: 2, Fingerprint: "fixture-fingerprint"}, true, nil
}

func (p *semanticProvider) Embed(_ context.Context, _ embedding.Identity, inputs []string) ([][]float32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return nil, fmt.Errorf("fixture endpoint unavailable")
	}
	result := make([][]float32, len(inputs))
	for index, input := range inputs {
		lowered := strings.ToLower(input)
		if strings.Contains(lowered, "glucose") || strings.Contains(input, "血糖") {
			result[index] = []float32{1, 0}
		} else {
			result[index] = []float32{0, 1}
		}
	}
	return result, nil
}

func (p *semanticProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *semanticProvider) setFail(value bool) {
	p.mu.Lock()
	p.fail = value
	p.mu.Unlock()
}

func TestProjectKnowledgeSearchSpansDocumentsAndRebuildsDeletedCache(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	projectA, err := projects.Create(ctx, "Project A", "")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := projects.Create(ctx, "Project B", "")
	if err != nil {
		t.Fatal(err)
	}
	attachments := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projects)
	repository := sqlite.NewKnowledgeRepository(store.DB())
	service := knowledge.NewService(repository, projects, attachments)
	if _, err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	sources := filepath.Join(root, "sources")
	if err := os.MkdirAll(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(sources, "paper-a.txt"),
		filepath.Join(sources, "paper-b.txt"),
		filepath.Join(sources, "chat-only.txt"),
		filepath.Join(sources, "other-project.txt"),
	}
	contents := []string{
		"Alpha kinase treatment improved the primary outcome in cohort A. 蛋白质表达出现显著变化。\n",
		"Independent replication found that alpha kinase treatment also improved cohort B.\n",
		"Alpha kinase treatment appears here only as a temporary chat attachment.\n",
		"Alpha kinase treatment from another project must never be returned.\n",
	}
	for index := range paths {
		if err := os.WriteFile(paths[index], []byte(contents[index]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := attachments.ImportPaths(ctx, projectA.ID, paths[:2])
	if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 2 {
		t.Fatalf("project A import = %#v, %v", batch, err)
	}
	chatOnly, err := attachments.ImportPaths(ctx, projectA.ID, paths[2:3])
	if err != nil || len(chatOnly.Errors) != 0 || len(chatOnly.Attachments) != 1 {
		t.Fatalf("chat-only import = %#v, %v", chatOnly, err)
	}
	if documents, err := service.ListDocuments(ctx, projectA.ID); err != nil || len(documents) != 0 {
		t.Fatalf("ordinary attachments entered knowledge base = %#v, %v", documents, err)
	}
	for _, value := range batch.Attachments {
		if err := service.Enqueue(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	other, err := attachments.ImportPaths(ctx, projectB.ID, paths[3:])
	if err != nil || len(other.Errors) != 0 || len(other.Attachments) != 1 {
		t.Fatalf("project B import = %#v, %v", other, err)
	}

	result, err := service.Search(ctx, projectA.ID, "alpha kinase treatment", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || result.TotalMatches != 2 || result.Status.Ready != 2 {
		t.Fatalf("project A search = %#v", result)
	}
	seen := map[string]bool{}
	for _, match := range result.Matches {
		seen[match.Name] = true
		if match.Locator != "lines:1-1" || match.AttachmentID == other.Attachments[0].ID {
			t.Fatalf("cross-project or imprecise match = %#v", match)
		}
	}
	if !seen["paper-a.txt"] || !seen["paper-b.txt"] || seen["other-project.txt"] {
		t.Fatalf("matched documents = %#v", seen)
	}
	chinese, err := service.Search(ctx, projectA.ID, "蛋白表达", 5)
	if err != nil || len(chinese.Matches) != 1 || chinese.Matches[0].Name != "paper-a.txt" {
		t.Fatalf("Chinese FTS search = %#v, %v", chinese, err)
	}
	singleCharacter, err := service.Search(ctx, projectA.ID, "蛋", 5)
	if err != nil || len(singleCharacter.Matches) != 1 || singleCharacter.Matches[0].Name != "paper-a.txt" {
		t.Fatalf("short Chinese fallback = %#v, %v", singleCharacter, err)
	}
	var schemaVersion int
	var engine string
	if err := store.DB().QueryRowContext(ctx, `SELECT schema_version,retrieval_engine FROM knowledge_index_versions WHERE project_id=? AND status='ready'`, projectA.ID).Scan(&schemaVersion, &engine); err != nil || schemaVersion != knowledge.IndexSchemaVersion || engine != knowledge.RetrievalEngine {
		t.Fatalf("active FTS version = %d, %q, %v", schemaVersion, engine, err)
	}
	version, found, err := repository.ReadyVersion(ctx, projectA.ID)
	if err != nil || !found {
		t.Fatalf("ready version = %#v, %v, %v", version, found, err)
	}
	if _, queued, err := repository.Enqueue(ctx, batch.Attachments[0], version, true, time.Now().UTC()); err != nil || !queued {
		t.Fatalf("forced reindex = %v, %v", queued, err)
	}
	reindexed, err := service.Search(ctx, projectA.ID, "alpha kinase treatment", 10)
	if err != nil || reindexed.TotalMatches != 2 {
		t.Fatalf("contentless FTS replacement = %#v, %v", reindexed, err)
	}
	indexPath := filepath.Join(projectA.WorkspacePath, project.PrivateDirectoryName, "cache", "knowledge", "index-v1.db")
	if info, err := os.Stat(indexPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("project-local index = %v, %v", info, err)
	}
	if err := os.RemoveAll(filepath.Dir(indexPath)); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := service.Search(ctx, projectA.ID, "cohort B", 10)
	if err != nil || len(rebuilt.Matches) < 1 || rebuilt.Matches[0].Name != "paper-b.txt" {
		t.Fatalf("rebuilt search = %#v, %v", rebuilt, err)
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("knowledge cache was not rebuilt: %v", err)
	}
	documents, err := service.ListDocuments(ctx, projectA.ID)
	if err != nil || len(documents) != 2 {
		t.Fatalf("knowledge documents = %#v, %v", documents, err)
	}
	var removeID string
	for _, value := range documents {
		if value.Title == "paper-a.txt" {
			removeID = value.ID
		}
	}
	if removeID == "" {
		t.Fatal("paper-a knowledge document was not listed")
	}
	if _, err := service.RemoveDocument(ctx, projectA.ID, removeID); err != nil {
		t.Fatal(err)
	}
	removed, err := service.Search(ctx, projectA.ID, "蛋白表达", 5)
	if err != nil || len(removed.Matches) != 0 {
		t.Fatalf("removed knowledge document remained searchable = %#v, %v", removed, err)
	}
	projectAttachments, err := attachments.List(ctx, projectA.ID)
	if err != nil || len(projectAttachments) != 3 {
		t.Fatalf("removing knowledge membership deleted attachment = %#v, %v", projectAttachments, err)
	}
}

func TestHybridSearchFindsSemanticEvidenceAndFallsBackToBM25(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "sciaide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	selectedProject, err := projects.Create(ctx, "Hybrid", "")
	if err != nil {
		t.Fatal(err)
	}
	attachments := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projects)
	provider := &semanticProvider{}
	service := knowledge.NewService(sqlite.NewKnowledgeRepository(store.DB()), projects, attachments)
	if err := service.SetEmbeddingProvider(provider); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	source := filepath.Join(root, "metabolism.txt")
	if err := os.WriteFile(source, []byte("The intervention improved glucose metabolism without changing body weight."), 0o600); err != nil {
		t.Fatal(err)
	}
	batch, err := attachments.ImportPaths(ctx, selectedProject.ID, []string{source})
	if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != 1 {
		t.Fatalf("import = %#v, %v", batch, err)
	}
	if err := service.Enqueue(ctx, batch.Attachments[0]); err != nil {
		t.Fatal(err)
	}
	semantic, err := service.SearchWithOptions(ctx, selectedProject.ID, knowledge.SearchOptions{Query: "降低血糖", Limit: 5})
	if err != nil || len(semantic.Matches) != 1 || semantic.RetrievalMode != knowledge.HybridRRF || semantic.Matches[0].SemanticRank == 0 {
		documents, _ := service.ListDocuments(ctx, selectedProject.ID)
		t.Fatalf("semantic search = %#v, %v; documents=%#v", semantic, err, documents)
	}
	filtered, err := service.SearchWithOptions(ctx, selectedProject.ID, knowledge.SearchOptions{Query: "降低血糖", Limit: 5, Formats: []document.Format{document.FormatPDF}})
	if err != nil || len(filtered.Matches) != 0 {
		t.Fatalf("format filter = %#v, %v", filtered, err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("Embedding calls after indexing and first query = %d, want 2", provider.callCount())
	}
	provider.setFail(true)
	cached, err := service.Search(ctx, selectedProject.ID, "降低血糖", 5)
	if err != nil || len(cached.Matches) != 1 || cached.RetrievalMode != knowledge.HybridRRF || cached.EmbeddingWarning != "" || provider.callCount() != 2 {
		t.Fatalf("cached semantic search = %#v, calls=%d, %v", cached, provider.callCount(), err)
	}
	fallback, err := service.Search(ctx, selectedProject.ID, "glucose metabolism", 5)
	if err != nil || len(fallback.Matches) != 1 || fallback.RetrievalMode != knowledge.HybridBM25Only || !strings.Contains(fallback.EmbeddingWarning, "FTS5/BM25") {
		t.Fatalf("fallback search = %#v, %v", fallback, err)
	}
	var model string
	var dimensions int
	if err := store.DB().QueryRowContext(ctx, `SELECT embedding_model,embedding_dimensions FROM knowledge_index_versions WHERE project_id=? AND status='ready'`, selectedProject.ID).Scan(&model, &dimensions); err != nil || model != "fixture-embedding" || dimensions != 2 {
		t.Fatalf("version identity = %q, %d, %v", model, dimensions, err)
	}
	version, found, err := sqlite.NewKnowledgeRepository(store.DB()).ReadyVersion(ctx, selectedProject.ID)
	if err != nil || !found {
		t.Fatalf("ready version = %#v, %v, %v", version, found, err)
	}
	indexPath := filepath.Join(selectedProject.WorkspacePath, project.PrivateDirectoryName, filepath.FromSlash(version.StorageRelativePath))
	indexDB, err := sql.Open("sqlite", indexPath)
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	var cacheEntries, cacheHits, hashLength, plaintextMatches int
	if err := indexDB.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(hit_count),0),COALESCE(MAX(length(query_hash)),0),COALESCE(SUM(instr(query_hash,'降低')),0) FROM query_embedding_cache`).Scan(&cacheEntries, &cacheHits, &hashLength, &plaintextMatches); err != nil {
		t.Fatal(err)
	}
	if cacheEntries != 1 || cacheHits != 2 || hashLength != 64 || plaintextMatches != 0 {
		t.Fatalf("query cache = entries %d, hits %d, hash length %d, plaintext matches %d", cacheEntries, cacheHits, hashLength, plaintextMatches)
	}
}
