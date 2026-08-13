package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/id"
)

type Transport string

const (
	TransportStdio          Transport = "stdio"
	TransportStreamableHTTP Transport = "streamable_http"
)

type Status string

const (
	StatusDisabled     Status = "disabled"
	StatusDisconnected Status = "disconnected"
	StatusStarting     Status = "starting"
	StatusInitializing Status = "initializing"
	StatusReady        Status = "ready"
	StatusDegraded     Status = "degraded"
	StatusFailed       Status = "failed"
	StatusStopping     Status = "stopping"
)

type Trust string

const (
	TrustUntrusted Trust = "untrusted"
	TrustUser      Trust = "user_trusted"
)

type Server struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	Transport        Transport         `json:"transport"`
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args"`
	WorkingDir       string            `json:"workingDir,omitempty"`
	URL              string            `json:"url,omitempty"`
	Headers          map[string]string `json:"headers"`
	Env              map[string]string `json:"env"`
	SecretEnv        map[string]string `json:"-"`
	SecretConfigured map[string]bool   `json:"secretConfigured"`
	Enabled          bool              `json:"enabled"`
	AutoStart        bool              `json:"autoStart"`
	Trust            Trust             `json:"trust"`
	TimeoutSeconds   int               `json:"timeoutSeconds"`
	Status           Status            `json:"status"`
	ProtocolVersion  string            `json:"protocolVersion,omitempty"`
	ServerVersion    string            `json:"serverVersion,omitempty"`
	ToolCount        int               `json:"toolCount"`
	ResourceCount    int               `json:"resourceCount"`
	PromptCount      int               `json:"promptCount"`
	LastError        string            `json:"lastError,omitempty"`
	LastConnectedAt  *time.Time        `json:"lastConnectedAt,omitempty"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
}

type SaveCommand struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Namespace      string            `json:"namespace"`
	Transport      Transport         `json:"transport"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	WorkingDir     string            `json:"workingDir"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	Env            map[string]string `json:"env"`
	SecretValues   map[string]string `json:"secretValues"`
	ClearSecrets   []string          `json:"clearSecrets"`
	Enabled        bool              `json:"enabled"`
	AutoStart      bool              `json:"autoStart"`
	Trust          Trust             `json:"trust"`
	TimeoutSeconds int               `json:"timeoutSeconds"`
}

type ToolInfo struct {
	OriginalName  string          `json:"originalName"`
	QualifiedName string          `json:"qualifiedName"`
	Description   string          `json:"description"`
	InputSchema   json.RawMessage `json:"inputSchema"`
	OutputSchema  json.RawMessage `json:"outputSchema,omitempty"`
	Version       string          `json:"version"`
}

type CapabilitySnapshot struct {
	ProtocolVersion string     `json:"protocolVersion,omitempty"`
	ServerVersion   string     `json:"serverVersion,omitempty"`
	Tools           []ToolInfo `json:"tools"`
	Resources       []string   `json:"resources"`
	Prompts         []string   `json:"prompts"`
}

type Repository interface {
	Save(ctx context.Context, value Server) error
	Get(ctx context.Context, id string) (Server, error)
	List(ctx context.Context) ([]Server, error)
	Delete(ctx context.Context, id string) error
	UpdateRuntime(ctx context.Context, id string, status Status, protocolVersion, serverVersion string, toolCount, resourceCount, promptCount int, lastError string, connectedAt *time.Time, updatedAt time.Time) error
}

type Connector interface {
	Connect(ctx context.Context, server Server) (CapabilitySnapshot, error)
	Disconnect(ctx context.Context, serverID string) error
	Connected(serverID string) bool
}

type SecretStore interface {
	Put(ctx context.Context, ref string, value []byte) error
	Get(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, ref string) error
	Configured(ctx context.Context, ref string) (bool, string, error)
}

type Service struct {
	repository Repository
	connector  Connector
	secrets    SecretStore
	now        func() time.Time
}

func NewService(repository Repository, connector Connector, secrets ...SecretStore) *Service {
	var secretStore SecretStore
	if len(secrets) > 0 {
		secretStore = secrets[0]
	}
	return &Service{repository: repository, connector: connector, secrets: secretStore, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Save(ctx context.Context, cmd SaveCommand) (Server, error) {
	if s.repository == nil {
		return Server{}, fmt.Errorf("MCP repository is not configured")
	}
	cmd = normalizeSave(cmd)
	if err := validateSave(cmd); err != nil {
		return Server{}, err
	}
	var value Server
	var originalSecretEnv map[string]string
	var err error
	if cmd.ID != "" {
		value, err = s.repository.Get(ctx, cmd.ID)
		if err != nil {
			return Server{}, err
		}
		if s.connector != nil && s.connector.Connected(value.ID) {
			return Server{}, fmt.Errorf("disconnect the MCP server before changing its configuration")
		}
		originalSecretEnv = cloneMap(value.SecretEnv)
	}
	if (len(cmd.SecretValues) > 0 || len(cmd.ClearSecrets) > 0) && s.secrets == nil {
		return Server{}, fmt.Errorf("MCP secret store is not configured")
	}
	now := s.now()
	if value.ID == "" {
		value.ID, err = id.New()
		if err != nil {
			return Server{}, err
		}
		value.CreatedAt = now
		value.Status = StatusDisconnected
	}
	if err := s.ensureNamespaceAvailable(ctx, value.ID, cmd.Namespace); err != nil {
		return Server{}, err
	}
	secretRefs, err := s.reconcileSecrets(ctx, value, cmd)
	if err != nil {
		return Server{}, err
	}
	value.Name, value.Namespace, value.Transport = cmd.Name, cmd.Namespace, cmd.Transport
	value.Command, value.Args, value.WorkingDir, value.URL = cmd.Command, cloneStrings(cmd.Args), cmd.WorkingDir, cmd.URL
	value.Headers, value.Env, value.SecretEnv = cloneMap(cmd.Headers), cloneMap(cmd.Env), secretRefs
	value.Enabled, value.AutoStart, value.Trust, value.TimeoutSeconds, value.UpdatedAt = cmd.Enabled, cmd.AutoStart, cmd.Trust, cmd.TimeoutSeconds, now
	if !value.Enabled {
		value.Status = StatusDisabled
	} else if value.Status == StatusDisabled {
		value.Status = StatusDisconnected
	}
	if err := s.repository.Save(ctx, value); err != nil {
		if cmd.ID == "" {
			s.deleteSecretRefs(context.Background(), value.SecretEnv)
		}
		return Server{}, fmt.Errorf("save MCP server: %w", err)
	}
	s.deleteRemovedSecrets(context.Background(), originalSecretEnv, value.SecretEnv)
	return s.repository.Get(ctx, value.ID)
}

type namespaceChecker interface {
	NamespaceOwner(ctx context.Context, namespace string) (string, error)
}

func (s *Service) ensureNamespaceAvailable(ctx context.Context, id, namespace string) error {
	checker, ok := s.repository.(namespaceChecker)
	if !ok {
		return nil
	}
	owner, err := checker.NamespaceOwner(ctx, namespace)
	if err != nil {
		return err
	}
	if owner != "" && owner != id {
		return fmt.Errorf("MCP namespace %q is already used by another server", namespace)
	}
	return nil
}

func (s *Service) List(ctx context.Context) ([]Server, error) {
	if s.repository == nil {
		return nil, fmt.Errorf("MCP repository is not configured")
	}
	return s.repository.List(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (Server, error) {
	if s.repository == nil {
		return Server{}, fmt.Errorf("MCP repository is not configured")
	}
	return s.repository.Get(ctx, strings.TrimSpace(id))
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if s.repository == nil {
		return fmt.Errorf("MCP repository is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("MCP server id is required")
	}
	if s.connector != nil && s.connector.Connected(id) {
		return fmt.Errorf("disconnect the MCP server before deleting it")
	}
	value, err := s.repository.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repository.Delete(ctx, id); err != nil {
		return err
	}
	s.deleteSecretRefs(context.Background(), value.SecretEnv)
	return nil
}

func (s *Service) Connect(ctx context.Context, id string) (Server, error) {
	if s.connector == nil {
		return Server{}, fmt.Errorf("MCP connector is not configured")
	}
	value, err := s.Get(ctx, id)
	if err != nil {
		return Server{}, err
	}
	if !value.Enabled {
		return Server{}, fmt.Errorf("MCP server is disabled")
	}
	if value.Trust != TrustUser {
		return Server{}, fmt.Errorf("MCP server must be explicitly trusted before connecting")
	}
	if s.connector.Connected(value.ID) {
		return Server{}, fmt.Errorf("MCP server is already connected or connecting")
	}
	now := s.now()
	_ = s.repository.UpdateRuntime(ctx, value.ID, StatusStarting, "", "", 0, 0, 0, "", nil, now)
	snapshot, err := s.connector.Connect(ctx, value)
	if err != nil {
		message := publicError(err)
		_ = s.repository.UpdateRuntime(context.Background(), value.ID, StatusFailed, "", "", 0, 0, 0, message, nil, s.now())
		return Server{}, fmt.Errorf("connect MCP server: %s", message)
	}
	connected := s.now()
	if err := s.repository.UpdateRuntime(ctx, value.ID, StatusReady, snapshot.ProtocolVersion, snapshot.ServerVersion, len(snapshot.Tools), len(snapshot.Resources), len(snapshot.Prompts), "", &connected, connected); err != nil {
		_ = s.connector.Disconnect(context.Background(), value.ID)
		return Server{}, err
	}
	return s.repository.Get(ctx, value.ID)
}

func (s *Service) ResolveSecretEnv(ctx context.Context, server Server) (map[string]string, error) {
	if len(server.SecretEnv) == 0 {
		return map[string]string{}, nil
	}
	if s.secrets == nil {
		return nil, fmt.Errorf("MCP secret store is not configured")
	}
	result := make(map[string]string, len(server.SecretEnv))
	for name, ref := range server.SecretEnv {
		value, err := s.secrets.Get(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("read MCP secret %q: %w", name, err)
		}
		result[name] = string(value)
		for i := range value {
			value[i] = 0
		}
	}
	return result, nil
}

func (s *Service) RecoverRuntime(ctx context.Context) (int, error) {
	if s.repository == nil {
		return 0, fmt.Errorf("MCP repository is not configured")
	}
	values, err := s.repository.List(ctx)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, value := range values {
		if value.Status == StatusStarting || value.Status == StatusInitializing || value.Status == StatusReady || value.Status == StatusDegraded || value.Status == StatusStopping {
			status := StatusDisconnected
			if !value.Enabled {
				status = StatusDisabled
			}
			if err := s.repository.UpdateRuntime(ctx, value.ID, status, "", "", 0, 0, 0, "", value.LastConnectedAt, s.now()); err != nil {
				return recovered, err
			}
			recovered++
		}
	}
	return recovered, nil
}

func (s *Service) RuntimeChanged(serverID string, snapshot CapabilitySnapshot, err error) {
	if s.repository == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, getErr := s.repository.Get(ctx, serverID)
	if getErr != nil {
		return
	}
	status, message := StatusReady, ""
	if err != nil {
		status, message = StatusFailed, publicError(err)
	}
	_ = s.repository.UpdateRuntime(ctx, serverID, status, snapshot.ProtocolVersion, snapshot.ServerVersion, len(snapshot.Tools), len(snapshot.Resources), len(snapshot.Prompts), message, value.LastConnectedAt, s.now())
}

func (s *Service) Starting(serverID string) {
	if s.repository == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := s.repository.Get(ctx, serverID)
	if err != nil || value.Status != StatusStarting {
		return
	}
	_ = s.repository.UpdateRuntime(ctx, serverID, StatusInitializing, "", "", 0, 0, 0, "", value.LastConnectedAt, s.now())
}

func (s *Service) reconcileSecrets(ctx context.Context, existing Server, cmd SaveCommand) (map[string]string, error) {
	refs := cloneMap(existing.SecretEnv)
	if existing.ID == "" {
		refs = map[string]string{}
	}
	for _, name := range cmd.ClearSecrets {
		name = strings.TrimSpace(name)
		delete(refs, name)
	}
	for name, secret := range cmd.SecretValues {
		name = strings.TrimSpace(name)
		ref := "sciaide/mcp/" + existing.ID + "/" + name
		if err := s.secrets.Put(ctx, ref, []byte(secret)); err != nil {
			return nil, fmt.Errorf("store MCP secret %q: %w", name, err)
		}
		refs[name] = ref
	}
	return refs, nil
}

func (s *Service) deleteSecretRefs(ctx context.Context, refs map[string]string) {
	if s.secrets == nil {
		return
	}
	for _, ref := range refs {
		_ = s.secrets.Delete(ctx, ref)
	}
}

func (s *Service) deleteRemovedSecrets(ctx context.Context, previous, current map[string]string) {
	if s.secrets == nil {
		return
	}
	for name, ref := range previous {
		if current[name] != ref {
			_ = s.secrets.Delete(ctx, ref)
		}
	}
}

func (s *Service) Disconnect(ctx context.Context, id string) (Server, error) {
	value, err := s.Get(ctx, id)
	if err != nil {
		return Server{}, err
	}
	if s.connector != nil {
		if err := s.connector.Disconnect(ctx, value.ID); err != nil {
			return Server{}, err
		}
	}
	status := StatusDisconnected
	if !value.Enabled {
		status = StatusDisabled
	}
	if err := s.repository.UpdateRuntime(ctx, value.ID, status, "", "", 0, 0, 0, "", value.LastConnectedAt, s.now()); err != nil {
		return Server{}, err
	}
	return s.repository.Get(ctx, value.ID)
}

func (s *Service) Capabilities(ctx context.Context, id string) (CapabilitySnapshot, error) {
	if reader, ok := s.connector.(interface {
		Capabilities(string) (CapabilitySnapshot, bool)
	}); ok {
		if value, exists := reader.Capabilities(strings.TrimSpace(id)); exists {
			return value, nil
		}
	}
	return CapabilitySnapshot{Tools: []ToolInfo{}, Resources: []string{}, Prompts: []string{}}, nil
}

func normalizeSave(cmd SaveCommand) SaveCommand {
	cmd.ID, cmd.Name, cmd.Namespace, cmd.Command = strings.TrimSpace(cmd.ID), strings.TrimSpace(cmd.Name), strings.ToLower(strings.TrimSpace(cmd.Namespace)), strings.TrimSpace(cmd.Command)
	cmd.WorkingDir, cmd.URL = strings.TrimSpace(cmd.WorkingDir), strings.TrimSpace(cmd.URL)
	cmd.Args = cloneStrings(cmd.Args)
	cmd.Headers, cmd.Env = cloneMap(cmd.Headers), cloneMap(cmd.Env)
	cmd.SecretValues = cloneMap(cmd.SecretValues)
	cmd.ClearSecrets = cloneStrings(cmd.ClearSecrets)
	if cmd.TimeoutSeconds == 0 {
		cmd.TimeoutSeconds = 30
	}
	return cmd
}

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validateSave(cmd SaveCommand) error {
	if cmd.Name == "" || len([]rune(cmd.Name)) > 100 || !namespacePattern.MatchString(cmd.Namespace) {
		return fmt.Errorf("MCP name and stable namespace are required")
	}
	if cmd.Transport != TransportStdio && cmd.Transport != TransportStreamableHTTP {
		return fmt.Errorf("unsupported MCP transport")
	}
	if cmd.Trust != TrustUntrusted && cmd.Trust != TrustUser {
		return fmt.Errorf("invalid MCP trust level")
	}
	if cmd.AutoStart {
		return fmt.Errorf("MCP auto-start is not enabled in this release")
	}
	if cmd.TimeoutSeconds < 5 || cmd.TimeoutSeconds > 300 {
		return fmt.Errorf("MCP timeout must be between 5 and 300 seconds")
	}
	if len(cmd.Args) > 64 || len(cmd.Env) > 64 || len(cmd.SecretValues) > 32 || len(cmd.ClearSecrets) > 32 || len(cmd.Headers) > 32 {
		return fmt.Errorf("MCP configuration exceeds size limits")
	}
	for _, arg := range cmd.Args {
		if len(arg) > 4096 || strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("invalid MCP argument")
		}
	}
	for name, value := range cmd.Env {
		if !envNamePattern.MatchString(name) || len(value) > 8192 || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("invalid MCP environment variable %q", name)
		}
	}
	for name, value := range cmd.SecretValues {
		if !envNamePattern.MatchString(name) || strings.TrimSpace(value) == "" || len(value) > 2560 || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("invalid MCP secret environment value %q", name)
		}
	}
	seenClear := map[string]struct{}{}
	for _, name := range cmd.ClearSecrets {
		name = strings.TrimSpace(name)
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("invalid MCP secret environment name %q", name)
		}
		if _, exists := cmd.SecretValues[name]; exists {
			return fmt.Errorf("MCP secret %q cannot be set and cleared together", name)
		}
		if _, exists := seenClear[name]; exists {
			return fmt.Errorf("duplicate MCP secret clear request %q", name)
		}
		seenClear[name] = struct{}{}
	}
	for name, value := range cmd.Headers {
		if strings.ContainsAny(name+value, "\r\n") || len(name) > 128 || len(value) > 8192 || sensitiveHeader(name) {
			return fmt.Errorf("invalid or sensitive MCP header %q", name)
		}
	}
	if cmd.Transport == TransportStdio {
		if cmd.Command == "" || len(cmd.Command) > 4096 || strings.IndexByte(cmd.Command, 0) >= 0 || cmd.URL != "" {
			return fmt.Errorf("stdio MCP requires a command and no URL")
		}
		if cmd.WorkingDir != "" && !filepath.IsAbs(cmd.WorkingDir) {
			return fmt.Errorf("MCP working directory must be absolute")
		}
	} else {
		if cmd.Command != "" || len(cmd.Args) != 0 || cmd.WorkingDir != "" || len(cmd.Env) != 0 || len(cmd.SecretValues) != 0 || len(cmd.ClearSecrets) != 0 {
			return fmt.Errorf("HTTP MCP cannot contain stdio process settings")
		}
		parsed, err := url.Parse(cmd.URL)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("invalid MCP URL")
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && localHost(parsed.Hostname())) {
			return fmt.Errorf("remote MCP requires HTTPS; HTTP is only allowed for localhost")
		}
	}
	return nil
}

func localHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sensitiveHeader(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "authorization" || name == "cookie" || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "api-key") || strings.Contains(name, "apikey")
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func cloneMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[strings.TrimSpace(key)] = value
	}
	return result
}

func publicError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func SortSnapshot(value *CapabilitySnapshot) {
	sort.Slice(value.Tools, func(i, j int) bool { return value.Tools[i].QualifiedName < value.Tools[j].QualifiedName })
	sort.Strings(value.Resources)
	sort.Strings(value.Prompts)
}
