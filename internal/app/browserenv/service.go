package browserenv

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/cloudflare"
	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
	"github.com/wangh00/SciAide/internal/browserhttp"
	"github.com/wangh00/SciAide/internal/network"
	"github.com/wangh00/SciAide/internal/platform/localexec"
	"golang.org/x/net/publicsuffix"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed bridge.py
var bridge string

type Projects interface {
	Get(context.Context, string) (project.Project, error)
}
type Service struct {
	projects Projects
	python   *pythonenv.Service
	runner   *localexec.Runner
	gate     chan struct{}
	mu       sync.Mutex
	cancel   map[string]context.CancelFunc
}
type Status struct {
	Ready      bool   `json:"ready"`
	Root       string `json:"root"`
	BinaryPath string `json:"binaryPath"`
	Message    string `json:"message"`
}
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure"`
	HTTPOnly bool    `json:"httpOnly"`
	Expires  float64 `json:"expires"`
}
type Clearance struct {
	Solved          bool          `json:"solved"`
	ChallengeSolved bool          `json:"challenge_solved"`
	Cookies         []Cookie      `json:"cookies"`
	UA              string        `json:"user_agent"`
	EgressIP        string        `json:"egressIP"`
	EgressStable    bool          `json:"egressStable"`
	Error           string        `json:"error"`
	FinalURL        string        `json:"final_url"`
	Proxy           network.Proxy `json:"-"`
	Revision        uint64        `json:"-"`
}

func New(p Projects, py *pythonenv.Service, r *localexec.Runner) *Service {
	return &Service{projects: p, python: py, runner: r, gate: make(chan struct{}, 1), cancel: map[string]context.CancelFunc{}}
}
func (s *Service) paths(ctx context.Context, id string) (project.Project, pythonenv.Environment, string, error) {
	p, e := s.projects.Get(ctx, id)
	if e != nil {
		return p, pythonenv.Environment{}, "", e
	}
	if e = project.VerifyPrivateDataLayout(p); e != nil {
		return p, pythonenv.Environment{}, "", e
	}
	env, e := s.python.Get(ctx, id)
	if e != nil {
		return p, env, "", e
	}
	if env.State != pythonenv.StateReady || env.Kind != pythonenv.KindWorkspaceManaged {
		return p, env, "", fmt.Errorf("浏览器仅支持当前项目的 Workspace 托管 Python 环境，请先创建项目环境")
	}
	root := filepath.Join(p.WorkspacePath, ".sciaide", "browser")
	if e = os.MkdirAll(root, 0700); e != nil {
		return p, env, root, e
	}
	resolved, e := filepath.EvalSymlinks(root)
	if e != nil {
		return p, env, root, e
	}
	base, e := filepath.EvalSymlinks(p.WorkspacePath)
	if e != nil {
		return p, env, root, e
	}
	rel, e := filepath.Rel(base, resolved)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p, env, root, fmt.Errorf("浏览器目录越出项目")
	}
	for _, name := range []string{"cache", "tmp", "playwright"} {
		path := filepath.Join(root, name)
		if e = os.MkdirAll(path, 0700); e != nil {
			return p, env, root, e
		}
		actual, e := filepath.EvalSymlinks(path)
		if e != nil {
			return p, env, root, e
		}
		rel, e := filepath.Rel(resolved, actual)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p, env, root, fmt.Errorf("浏览器缓存目录越出项目")
		}
	}
	return p, env, root, nil
}
func (s *Service) acquire(ctx context.Context, id string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if _, busy := s.cancel[id]; busy {
		s.mu.Unlock()
		cancel()
		return ctx, nil, fmt.Errorf("当前项目已有浏览器操作，请等待完成或取消")
	}
	s.cancel[id] = cancel
	s.mu.Unlock()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		cancel()
		s.mu.Lock()
		delete(s.cancel, id)
		s.mu.Unlock()
		return ctx, nil, ctx.Err()
	}
	return ctx, func() { cancel(); s.mu.Lock(); delete(s.cancel, id); s.mu.Unlock(); <-s.gate }, nil
}
func (s *Service) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.cancel[id]; f != nil {
		f()
	}
}
func (s *Service) run(ctx context.Context, env pythonenv.Environment, root, action, target string, p network.Proxy) ([]byte, error) {
	if e := writeRuntimeFile(root, "solve.py", []byte(cloudflare.Solver)); e != nil {
		return nil, e
	}
	if e := writeRuntimeFile(root, "bridge.py", []byte(bridge)); e != nil {
		return nil, e
	}
	f, e := os.CreateTemp(root, ".browser-result-*.json")
	if e != nil {
		return nil, e
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	payload, _ := json.Marshal(map[string]any{"root": root, "action": action, "url": target, "proxy": p.URL, "result": path})
	child, _ := localexec.CoreEnvironment(map[string]string{"SCIAIDE_BROWSER_REQUEST": string(payload), "PYTHONIOENCODING": "utf-8", "PYTHONUTF8": "1", "PYTHONDONTWRITEBYTECODE": "1"})
	child = network.PinnedEnvironment(child, p)
	res, e := s.runner.Execute(ctx, localexec.Request{Program: env.EnvironmentPythonPath, Args: []string{"-I", "-u", filepath.Join(root, "bridge.py")}, Dir: root, Env: child, NetworkPinned: true, Timeout: browserTimeout(action), MemoryLimitBytes: 2 << 30})
	if e != nil || res.ExitCode != 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("浏览器操作未完成；请检查项目依赖、网络代理或取消状态")
	}
	b, e := readPrivateResult(path)
	if len(b) > 256*1024 {
		return nil, fmt.Errorf("浏览器结果过大")
	}
	return b, e
}
func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	_, _, root, e := s.paths(ctx, id)
	if e != nil {
		return Status{Message: e.Error()}, nil
	}
	st := Status{Root: root}
	if _, e = os.Stat(filepath.Join(root, "installed.json")); e != nil {
		st.Message = "尚未配置浏览器环境"
		return st, nil
	}
	b, e := os.ReadFile(filepath.Join(root, "installed.json"))
	if e != nil {
		st.Message = e.Error()
		return st, nil
	}
	json.Unmarshal(b, &st)
	st.Root = root
	st.Ready = s.Available(ctx, id)
	return st, nil
}
func (s *Service) Install(ctx context.Context, id string) (Status, error) {
	_, _, root, e := s.paths(ctx, id)
	if e != nil {
		return Status{}, e
	}
	ctx, done, e := s.acquire(ctx, id)
	if e != nil {
		return Status{}, e
	}
	defer done()
	env, e := s.python.Install(ctx, id, []string{"cloakbrowser[geoip]==0.5.9"})
	if e != nil {
		return Status{}, e
	}
	p, _ := network.Resolve("dependencies")
	b, e := s.run(ctx, env, root, "install", "", p)
	if e != nil {
		return Status{}, e
	}
	var st Status
	if e = json.Unmarshal(b, &st); e != nil || !st.Ready {
		return st, fmt.Errorf("浏览器文件未通过检测")
	}
	st.Root = root
	e = writeRuntimeFile(root, "installed.json", b)
	return st, e
}
func (s *Service) Solve(ctx context.Context, id, target string, p network.Proxy, revision uint64) (Clearance, error) {
	var result Clearance
	u, e := url.Parse(target)
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || network.IsLoopback(u.Hostname()) {
		return result, fmt.Errorf("浏览器需公开 HTTP/HTTPS 地址")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsPrivate() || !ip.IsGlobalUnicast()) {
		return result, fmt.Errorf("浏览器不允许私有地址")
	}
	ctx, done, e := s.acquire(ctx, id)
	if e != nil {
		return result, e
	}
	defer done()
	_, env, root, e := s.paths(ctx, id)
	if e != nil {
		return result, e
	}
	if !s.Available(ctx, id) {
		return result, fmt.Errorf("请先配置当前项目浏览器环境")
	}
	verified, verifyErr := s.python.Verify(ctx, id)
	if verifyErr != nil {
		return result, fmt.Errorf("项目 Python 环境验证失败，请检查环境")
	}
	env = verified
	b, e := s.run(ctx, env, root, "solve", target, p)
	if e != nil {
		return result, e
	}
	defer clear(b)
	if e = json.Unmarshal(b, &result); e != nil {
		return result, e
	}
	final, e := url.Parse(result.FinalURL)
	if e != nil || final.User != nil || final.Scheme != u.Scheme || !strings.EqualFold(final.Hostname(), u.Hostname()) {
		return result, fmt.Errorf("浏览器最终页面越出请求站点，不复用 Cookie")
	}
	if !result.EgressStable || net.ParseIP(result.EgressIP) == nil {
		return result, fmt.Errorf("浏览器出口 IP 不稳定或无法核验，拒绝复用 Cookie")
	}
	result.Proxy = p
	result.Revision = revision
	return result, nil
}

// Client pins the proxy, UA and domain-scoped cookie jar for one operation only.
func (c Clearance) Client(ctx context.Context) (*http.Client, func(), error) {
	if !c.EgressStable || (!c.Solved && !c.ChallengeSolved) || c.UA == "" {
		return nil, nil, fmt.Errorf("浏览器未完成验证，不复用 Cookie")
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	if c.Proxy.Mode == "custom" {
		u, e := url.Parse(c.Proxy.URL)
		if e != nil {
			return nil, nil, e
		}
		base.Proxy = http.ProxyURL(u)
	}
	tr := browserhttp.NewFixed(base)
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	client := &http.Client{Transport: browserhttp.WithSessionUA(tr, c.UA), Jar: jar, Timeout: 45 * time.Second}
	ip, e := egress(ctx, client)
	if e != nil || ip != c.EgressIP {
		tr.CloseIdleConnections()
		return nil, nil, fmt.Errorf("Go 请求出口 IP 与浏览器不一致，拒绝发送 Cookie")
	}
	origin, e := url.Parse(c.FinalURL)
	if e != nil || origin.Hostname() == "" {
		tr.CloseIdleConnections()
		return nil, nil, fmt.Errorf("浏览器最终地址无效")
	}
	for _, v := range c.Cookies {
		domain := strings.TrimPrefix(v.Domain, ".")
		if origin.Hostname() != domain && !strings.HasSuffix(origin.Hostname(), "."+domain) {
			continue
		}
		cookie := &http.Cookie{Name: v.Name, Value: v.Value, Domain: v.Domain, Path: v.Path, Secure: v.Secure, HttpOnly: v.HTTPOnly}
		if v.Expires > 0 {
			cookie.Expires = time.Unix(int64(v.Expires), 0)
		}
		jar.SetCookies(origin, []*http.Cookie{cookie})
	}
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 4 || !strings.EqualFold(r.URL.Hostname(), origin.Hostname()) {
			return fmt.Errorf("浏览器会话重定向越出当前站点")
		}
		return nil
	}
	return client, tr.CloseIdleConnections, nil
}
func egress(ctx context.Context, c *http.Client) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
	res, e := c.Do(r)
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 128))
	ip := strings.TrimSpace(string(b))
	if e != nil || res.StatusCode != 200 || net.ParseIP(ip) == nil {
		return "", fmt.Errorf("无法核验出口 IP")
	}
	return ip, nil
}
func Fingerprint(p network.Proxy, revision uint64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", p.Mode, p.URL, revision)))
	return hex.EncodeToString(h[:])
}

// Available is a local, read-only capability check; it never starts Python.
func (s *Service) Available(ctx context.Context, id string) bool {
	p, e := s.projects.Get(ctx, id)
	if e != nil {
		return false
	}
	env, e := s.python.Get(ctx, id)
	if e != nil || env.State != pythonenv.StateReady || env.Kind != pythonenv.KindWorkspaceManaged {
		return false
	}
	b, e := os.ReadFile(filepath.Join(p.WorkspacePath, ".sciaide", "browser", "installed.json"))
	if e != nil {
		return false
	}
	var st Status
	if json.Unmarshal(b, &st) != nil || !st.Ready {
		return false
	}
	root, e := filepath.EvalSymlinks(filepath.Join(p.WorkspacePath, ".sciaide", "browser"))
	if e != nil {
		return false
	}
	binary, e := filepath.EvalSymlinks(st.BinaryPath)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(root, binary)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readPrivateResult(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 256*1024+1))
}

func browserTimeout(action string) time.Duration {
	if action == "install" {
		return 15 * time.Minute
	}
	if action == "status" {
		return 30 * time.Second
	}
	return 3 * time.Minute
}

func writeRuntimeFile(root, name string, b []byte) error {
	path := filepath.Join(root, name)
	if st, e := os.Lstat(path); e == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular()) {
		return fmt.Errorf("浏览器运行文件不是普通文件")
	}
	return os.WriteFile(path, b, 0600)
}
