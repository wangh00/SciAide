package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/browserhttp"
	"github.com/wangh00/SciAide/internal/network"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
)

var Providers = []string{"baidu", "firecrawl", "brave", "tavily", "exa"}

type Secrets interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}
type Channel struct {
	Provider   string `json:"provider"`
	Enabled    bool   `json:"enabled"`
	Priority   int    `json:"priority"`
	Configured bool   `json:"configured"`
}
type SaveCommand struct {
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
	APIKey   string `json:"apiKey"`
}
type stored struct {
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
	Key      string `json:"key"`
}
type Item struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}
type Attempt struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
}
type Result struct {
	Query    string    `json:"query"`
	Provider string    `json:"provider"`
	Status   string    `json:"status"`
	Items    []Item    `json:"items"`
	Attempts []Attempt `json:"attempts"`
}
type cooling struct {
	until  time.Time
	reason string
}
type Service struct {
	secrets         Secrets
	client          *http.Client
	mu              sync.Mutex
	cooldown        map[string]cooling
	networkRevision uint64
	gate            chan struct{}
}

func New(secrets Secrets) *Service {
	base := http.DefaultTransport.(*http.Transport).Clone()
	return &Service{secrets: secrets, client: &http.Client{Transport: browserhttp.New(base), Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cooldown: map[string]cooling{}, gate: make(chan struct{}, 1)}
}
func validProvider(id string) bool {
	for _, p := range Providers {
		if id == p {
			return true
		}
	}
	return false
}
func (s *Service) read(ctx context.Context, id string) (stored, error) {
	b, err := s.secrets.Get(ctx, "websearch/"+id)
	if errors.Is(err, secretstore.ErrNotFound) {
		return stored{}, nil
	}
	if err != nil {
		return stored{}, fmt.Errorf("无法读取搜索服务凭据")
	}
	defer clear(b)
	var v stored
	if json.Unmarshal(b, &v) != nil {
		return v, fmt.Errorf("搜索配置损坏，请重新保存")
	}
	return v, nil
}
func (s *Service) List(ctx context.Context) ([]Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Channel{}
	for i, id := range Providers {
		v, err := s.read(ctx, id)
		if err != nil {
			return nil, err
		}
		if v.Priority == 0 {
			v.Priority = i + 1
		}
		out = append(out, Channel{id, v.Enabled, v.Priority, v.Key != ""})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	b, err := s.secrets.Get(ctx, "websearch/order")
	if err != nil && !errors.Is(err, secretstore.ErrNotFound) {
		return nil, fmt.Errorf("无法读取搜索顺序")
	}
	if err == nil {
		defer clear(b)
		var order []string
		if json.Unmarshal(b, &order) != nil || !validOrder(order) {
			return nil, fmt.Errorf("搜索顺序配置损坏")
		}
		ranks := map[string]int{}
		for i, id := range order {
			ranks[id] = i + 1
		}
		for i := range out {
			out[i].Priority = ranks[out[i].Provider]
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	}
	return out, nil
}

func validOrder(order []string) bool {
	if len(order) != len(Providers) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range order {
		if !validProvider(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *Service) SaveOrder(ctx context.Context, order []string) error {
	if !validOrder(order) {
		return fmt.Errorf("搜索顺序必须包含每个供应商且不能重复")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(order)
	if err != nil {
		return err
	}
	if err := s.secrets.Put(ctx, "websearch/order", b); err != nil {
		return fmt.Errorf("无法保存搜索顺序")
	}
	return nil
}
func (s *Service) Save(ctx context.Context, c SaveCommand) error {
	if !validProvider(c.Provider) || c.Priority < 1 || c.Priority > 100 {
		return fmt.Errorf("搜索来源或优先级无效")
	}
	if len(c.APIKey) > 1800 || strings.ContainsAny(c.APIKey, "\r\n") {
		return fmt.Errorf("搜索 API Key 无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.read(ctx, c.Provider)
	if err != nil {
		return err
	}
	if strings.TrimSpace(c.APIKey) != "" {
		v.Key = strings.TrimSpace(c.APIKey)
	}
	if c.Enabled && v.Key == "" {
		return fmt.Errorf("启用前请填写 API Key")
	}
	v.Enabled = c.Enabled
	v.Priority = c.Priority
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	defer clear(b)
	if err = s.secrets.Put(ctx, "websearch/"+c.Provider, b); err != nil {
		return fmt.Errorf("无法保存搜索凭据")
	}
	delete(s.cooldown, c.Provider)
	return nil
}
func (s *Service) Delete(ctx context.Context, id string) error {
	if !validProvider(id) {
		return fmt.Errorf("搜索来源无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.secrets.Delete(ctx, "websearch/"+id); err != nil && !errors.Is(err, secretstore.ErrNotFound) {
		return fmt.Errorf("无法删除搜索凭据")
	}
	delete(s.cooldown, id)
	return nil
}
func (s *Service) Search(ctx context.Context, query string, limit int) (Result, error) {
	_, revision := network.Resolve("search")
	s.mu.Lock()
	if revision != s.networkRevision {
		clear(s.cooldown)
		s.networkRevision = revision
	}
	s.mu.Unlock()
	query = strings.TrimSpace(query)
	r := Result{Query: query, Status: "unavailable", Items: []Item{}, Attempts: []Attempt{}}
	if query == "" || len([]rune(query)) > 500 || limit < 1 || limit > 10 {
		return r, fmt.Errorf("查询需为 1 至 500 字，结果数需为 1 至 10")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return r, ctx.Err()
	}
	channels, err := s.List(ctx)
	if err != nil {
		return r, err
	}
	channels = append(channels, Channel{Provider: "duckduckgo", Enabled: true, Configured: true})
	for _, ch := range channels {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		if !ch.Enabled || !ch.Configured {
			continue
		}
		s.mu.Lock()
		cool := s.cooldown[ch.Provider]
		s.mu.Unlock()
		if time.Now().Before(cool.until) {
			r.Attempts = append(r.Attempts, Attempt{ch.Provider, "cooldown:" + cool.reason})
			continue
		}
		key := ""
		if ch.Provider != "duckduckgo" {
			s.mu.Lock()
			v, e := s.read(ctx, ch.Provider)
			s.mu.Unlock()
			if e != nil {
				return r, e
			}
			if !v.Enabled || v.Key == "" {
				continue
			}
			key = v.Key
		}
		items, status, delay := s.request(ctx, ch.Provider, key, query, limit)
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		r.Attempts = append(r.Attempts, Attempt{ch.Provider, status})
		if status == "ok" || status == "empty" {
			r.Provider = ch.Provider
			r.Status = status
			r.Items = items
			return r, nil
		}
		if delay > 0 {
			s.mu.Lock()
			s.cooldown[ch.Provider] = cooling{time.Now().Add(delay), status}
			s.mu.Unlock()
		}
	}
	return r, nil
}
