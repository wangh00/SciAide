// Package network owns application network policy. It never mutates OS settings.
package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wangh00/SciAide/internal/platform/secretstore"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

type Proxy struct {
	Mode string `json:"mode"`
	URL  string `json:"url"`
}
type Config struct {
	Global   Proxy            `json:"global"`
	Modules  map[string]Proxy `json:"modules"`
	Revision uint64           `json:"revision"`
}
type Secrets interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
}
type Service struct {
	mu      sync.Mutex
	secrets Secrets
	config  Config
}

var active atomic.Pointer[Service]
var Modules = []string{"model", "search", "research", "dependencies", "mcp", "skills", "browser"}

func New(ctx context.Context, secrets Secrets) (*Service, error) {
	s := &Service{secrets: secrets, config: Config{Global: Proxy{Mode: "direct"}, Modules: map[string]Proxy{}}}
	b, e := secrets.Get(ctx, "network/config")
	if errors.Is(e, secretstore.ErrNotFound) {
		return s, nil
	}
	if e != nil {
		return nil, fmt.Errorf("无法读取应用网络配置")
	}
	defer clear(b)
	if json.Unmarshal(b, &s.config) != nil {
		return nil, fmt.Errorf("应用网络配置损坏")
	}
	if e = validate(s.config); e != nil {
		return nil, e
	}
	return s, nil
}
func Activate(s *Service) { active.Store(s) }
func Current() *Service   { return active.Load() }
func ValidateProxy(p Proxy, global bool) error {
	if p.Mode == "inherit" && !global {
		return nil
	}
	if p.Mode == "direct" {
		return nil
	}
	if p.Mode != "custom" {
		return fmt.Errorf("未知代理模式")
	}
	u, e := url.Parse(strings.TrimSpace(p.URL))
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(p.URL) > 1024 {
		return fmt.Errorf("代理地址无效，暂不支持内嵌账号密码")
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return fmt.Errorf("代理协议需为 HTTP、HTTPS 或 SOCKS5")
	}
	return nil
}
func validate(c Config) error {
	if e := ValidateProxy(c.Global, true); e != nil {
		return e
	}
	for k, v := range c.Modules {
		ok := false
		for _, m := range Modules {
			if k == m {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("未知网络模块 %s", k)
		}
		if e := ValidateProxy(v, false); e != nil {
			return e
		}
	}
	return nil
}
func clone(c Config) Config {
	m := map[string]Proxy{}
	for k, v := range c.Modules {
		m[k] = v
	}
	c.Modules = m
	return c
}
func (s *Service) Get() Config { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.config) }
func (s *Service) Save(ctx context.Context, c Config) error {
	if e := validate(c); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Revision != s.config.Revision {
		return fmt.Errorf("网络配置已变化，请重新加载后保存")
	}
	c = clone(c)
	c.Global.URL = strings.TrimSpace(c.Global.URL)
	for k, v := range c.Modules {
		v.URL = strings.TrimSpace(v.URL)
		c.Modules[k] = v
	}
	c.Revision++
	b, _ := json.Marshal(c)
	if len(b) > 2500 {
		return fmt.Errorf("网络配置过长，请缩短代理地址")
	}
	defer clear(b)
	if e := s.secrets.Put(ctx, "network/config", b); e != nil {
		return fmt.Errorf("无法保存应用网络配置")
	}
	s.config = c
	return nil
}
func (s *Service) Resolve(module string) (Proxy, uint64) {
	c := s.Get()
	p := c.Global
	if v, ok := c.Modules[module]; ok && v.Mode != "inherit" {
		p = v
	}
	if p.Mode != "custom" {
		p = Proxy{Mode: "direct"}
	}
	return p, c.Revision
}
func Resolve(module string) (Proxy, uint64) {
	if s := Current(); s != nil {
		return s.Resolve(module)
	}
	return Proxy{Mode: "direct"}, 0
}
func URL(module string) (*url.URL, error) {
	p, _ := Resolve(module)
	if p.Mode != "custom" {
		return nil, nil
	}
	return url.Parse(p.URL)
}
func IsLoopback(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	ip := net.ParseIP(h)
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || (ip != nil && ip.IsLoopback())
}
func ProxyFunc(module string) func(*http.Request) (*url.URL, error) {
	return func(r *http.Request) (*url.URL, error) {
		if IsLoopback(r.URL.Hostname()) {
			return nil, nil
		}
		return URL(module)
	}
}

// Environment does not inherit system proxy variables and never falls back on error.
func Environment(env []string, module string) []string {
	p, _ := Resolve(module)
	return PinnedEnvironment(env, p)
}
func PinnedEnvironment(env []string, p Proxy) []string {
	out := []string{}
	for _, v := range env {
		k := strings.ToUpper(strings.SplitN(v, "=", 2)[0])
		switch k {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		out = append(out, v)
	}
	value := ""
	if p.Mode == "custom" {
		value = p.URL
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		out = append(out, k+"="+value)
	}
	bypass := "localhost,127.0.0.1,::1"
	if value == "" {
		bypass = "*"
	}
	return append(out, "NO_PROXY="+bypass, "no_proxy="+bypass)
}
