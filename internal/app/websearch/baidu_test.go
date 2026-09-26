package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

func TestBaiduRequestAndDefaultPriority(t *testing.T) {
	s := New(secretstore.NewMemory())
	ctx := context.Background()
	channels, err := s.List(ctx)
	if err != nil || channels[0].Provider != "baidu" || channels[0].Priority != 1 {
		t.Fatalf("%+v %v", channels, err)
	}
	if err := s.Save(ctx, SaveCommand{"baidu", true, 1, "test-key"}); err != nil {
		t.Fatal(err)
	}
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://qianfan.baidubce.com/v2/ai_search/web_search" || r.Method != "POST" || r.Header.Get("X-Appbuilder-Authorization") != "Bearer test-key" {
			t.Fatal("incorrect Baidu request")
		}
		var body struct {
			Messages     []struct{ Role, Content string }
			SearchSource string `json:"search_source"`
			Resources    []struct {
				Type string
				TopK int `json:"top_k"`
			} `json:"resource_type_filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 1 || body.Messages[0].Content != "研究方法" || body.Messages[0].Role != "user" || body.SearchSource != "baidu_search_v2" || len(body.Resources) != 1 || body.Resources[0].TopK != 3 {
			t.Fatalf("bad body: %+v", body)
		}
		return reply(200, `{"references":[{"title":"研究","url":"https://example.org/paper","snippet":"摘要","content":"全文片段"}]}`), nil
	})
	result, err := s.Search(ctx, "研究方法", 3)
	if err != nil || result.Provider != "baidu" || len(result.Items) != 1 || result.Items[0].Snippet != "摘要" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestBaiduLongQueryFallbackWithoutCooldown(t *testing.T) {
	s := New(secretstore.NewMemory())
	_ = s.Save(context.Background(), SaveCommand{"baidu", true, 1, "test-key"})
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != "html.duckduckgo.com" {
			t.Fatal("long query sent to Baidu")
		}
		return reply(200, `<a class="result__a" href="https://example.org">Result</a>`), nil
	})
	result, err := s.Search(context.Background(), strings.Repeat("研", 37), 3)
	if err != nil || result.Provider != "duckduckgo" || result.Attempts[0].Status != "query_too_long" || len(s.cooldown) != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	for _, body := range []string{`{"code":216003,"references":[]}`, `{"code":"216003"}`, `{}`} {
		if _, err := parseAPI("baidu", []byte(body)); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	items, err := parseAPI("baidu", []byte(`{"references":[]}`))
	if err != nil || len(items) != 0 {
		t.Fatal("valid empty result rejected")
	}
}
