package websearch

import (
	"context"
	"errors"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"testing"
)

type failingOrderStore struct {
	Secrets
	fail bool
}

func (s *failingOrderStore) Put(ctx context.Context, key string, b []byte) error {
	if key == "websearch/order" && s.fail {
		return errors.New("test failure")
	}
	return s.Secrets.Put(ctx, key, b)
}
func TestOrderPersistenceAndFailure(t *testing.T) {
	ctx := context.Background()
	store := &failingOrderStore{Secrets: secretstore.NewMemory()}
	s := New(store)
	_ = s.Save(ctx, SaveCommand{"baidu", true, 1, "test-secret"})
	order := []string{"exa", "tavily", "brave", "firecrawl", "baidu"}
	if err := s.SaveOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		channels, err := New(store).List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i, c := range channels {
			if c.Provider != order[i] || c.Priority != i+1 {
				t.Fatalf("bad order: %+v", channels)
			}
		}
	}
	check()
	v, _ := s.read(ctx, "baidu")
	if v.Key != "test-secret" || !v.Enabled {
		t.Fatal("reorder changed credentials")
	}
	for _, invalid := range [][]string{{"baidu"}, {"baidu", "baidu", "brave", "tavily", "exa"}, {"duckduckgo", "firecrawl", "brave", "tavily", "exa"}} {
		if s.SaveOrder(ctx, invalid) == nil {
			t.Fatal("invalid order accepted")
		}
	}
	store.fail = true
	if s.SaveOrder(ctx, Providers) == nil {
		t.Fatal("save failure ignored")
	}
	check()
	if err := s.Delete(ctx, "baidu"); err != nil {
		t.Fatal(err)
	}
	check()
}
