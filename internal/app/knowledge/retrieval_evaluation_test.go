package knowledge_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/embedding"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

type retrievalFixture struct {
	Documents []struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"documents"`
	Cases []knowledge.EvaluationCase `json:"cases"`
}

type baselineEmbeddingProvider struct{}

func (baselineEmbeddingProvider) Current(context.Context) (embedding.Identity, bool, error) {
	return embedding.Identity{ModelID: "baseline-embedding", Dimensions: 4, Fingerprint: "fixed-corpus-v1"}, true, nil
}

func (baselineEmbeddingProvider) Embed(_ context.Context, _ embedding.Identity, inputs []string) ([][]float32, error) {
	result := make([][]float32, len(inputs))
	for index, input := range inputs {
		value := strings.ToLower(input)
		switch {
		case strings.Contains(value, "brca1") || strings.Contains(value, "protein") || strings.Contains(value, "kinase"):
			result[index] = []float32{1, 0, 0, 0}
		case strings.Contains(value, "ocean") || strings.Contains(value, "heatwave") || strings.Contains(value, "temperature"):
			result[index] = []float32{0, 1, 0, 0}
		case strings.Contains(value, "randomized") || strings.Contains(value, "placebo") || strings.Contains(value, "trial"):
			result[index] = []float32{0, 0, 1, 0}
		case strings.Contains(value, "贝叶斯") || strings.Contains(value, "后验") || strings.Contains(value, "先验"):
			result[index] = []float32{0, 0, 0, 1}
		default:
			result[index] = []float32{0.5, 0.5, 0.5, 0.5}
		}
	}
	return result, nil
}

func TestFixedCorpusBM25RetrievalBaseline(t *testing.T) {
	metrics := runFixedCorpusBaseline(t, nil)
	assertRetrievalBaseline(t, metrics)
}

func TestFixedCorpusHybridRetrievalBaseline(t *testing.T) {
	provider := baselineEmbeddingProvider{}
	metrics := runFixedCorpusBaseline(t, provider)
	assertRetrievalBaseline(t, metrics)
}

func runFixedCorpusBaseline(t *testing.T, provider knowledge.EmbeddingProvider) knowledge.RetrievalMetrics {
	t.Helper()
	var fixture retrievalFixture
	contents, err := os.ReadFile(filepath.Join("testdata", "retrieval_baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &fixture); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(root, "evaluation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects := project.NewService(sqlite.NewProjectRepository(store.DB()), filepath.Join(root, "workspaces"), filepath.Join(root, "trash"))
	selectedProject, err := projects.Create(ctx, "Retrieval baseline", "")
	if err != nil {
		t.Fatal(err)
	}
	attachments := attachment.NewService(sqlite.NewAttachmentRepository(store.DB()), projects)
	service := knowledge.NewService(sqlite.NewKnowledgeRepository(store.DB()), projects, attachments)
	if provider != nil {
		if err := service.SetEmbeddingProvider(provider); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	sourceRoot := filepath.Join(root, "corpus")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(fixture.Documents))
	for index, item := range fixture.Documents {
		paths[index] = filepath.Join(sourceRoot, item.Name)
		if err := os.WriteFile(paths[index], []byte(item.Content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := attachments.ImportPaths(ctx, selectedProject.ID, paths)
	if err != nil || len(batch.Errors) != 0 || len(batch.Attachments) != len(paths) {
		t.Fatalf("baseline import = %#v, %v", batch, err)
	}
	for _, item := range batch.Attachments {
		if err := service.Enqueue(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	rankings := make([]knowledge.EvaluationRanking, 0, len(fixture.Cases))
	for _, item := range fixture.Cases {
		result, err := service.Search(ctx, selectedProject.ID, item.Query, 5)
		if err != nil {
			t.Fatalf("query %q: %v", item.Query, err)
		}
		rankings = append(rankings, knowledge.EvaluationRanking{Case: item, Matches: result.Matches})
	}
	metrics, err := knowledge.EvaluateRankings(rankings, 5)
	if err != nil {
		t.Fatal(err)
	}
	return metrics
}

func assertRetrievalBaseline(t *testing.T, metrics knowledge.RetrievalMetrics) {
	t.Helper()
	if metrics.HitRate < 1 || metrics.MeanRecall < 1 || metrics.MeanReciprocal < 0.95 || metrics.LocatableRate < 1 {
		t.Fatalf("retrieval baseline regressed: %#v", metrics)
	}
}
