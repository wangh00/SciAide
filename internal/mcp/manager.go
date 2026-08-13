package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wangh00/SciAide/internal/app/mcpserver"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type Manager struct {
	registry tool.MutableRegistry
	logger   *slog.Logger

	mu               sync.RWMutex
	sessions         map[string]*connection
	pending          map[string]*pendingConnection
	connectWG        sync.WaitGroup
	observer         RuntimeObserver
	secrets          SecretResolver
	transportFactory func(mcpserver.Server, map[string]string) (mcpsdk.Transport, error)
	closing          bool
}

type RuntimeObserver interface {
	Starting(serverID string)
	RuntimeChanged(serverID string, snapshot mcpserver.CapabilitySnapshot, err error)
}

type SecretResolver interface {
	ResolveSecretEnv(ctx context.Context, server mcpserver.Server) (map[string]string, error)
}

type connection struct {
	server       mcpserver.Server
	session      *mcpsdk.ClientSession
	capabilities mcpserver.CapabilitySnapshot
}

type pendingConnection struct{ cancel context.CancelFunc }

func NewManager(registry tool.MutableRegistry, logger *slog.Logger) *Manager {
	return &Manager{registry: registry, logger: logger, sessions: make(map[string]*connection), pending: make(map[string]*pendingConnection), transportFactory: buildTransport}
}

func (m *Manager) SetRuntimeObserver(observer RuntimeObserver) { m.observer = observer }
func (m *Manager) SetSecretResolver(resolver SecretResolver)   { m.secrets = resolver }

func (m *Manager) Connect(ctx context.Context, server mcpserver.Server) (mcpserver.CapabilitySnapshot, error) {
	if m.registry == nil {
		return mcpserver.CapabilitySnapshot{}, fmt.Errorf("MCP tool registry is not configured")
	}
	if server.TimeoutSeconds <= 0 {
		return mcpserver.CapabilitySnapshot{}, fmt.Errorf("MCP timeout must be positive")
	}
	if m.transportFactory == nil {
		return mcpserver.CapabilitySnapshot{}, fmt.Errorf("MCP transport factory is not configured")
	}
	connectCtx, cancel := context.WithTimeout(ctx, time.Duration(server.TimeoutSeconds)*time.Second)
	pending := &pendingConnection{cancel: cancel}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		cancel()
		return mcpserver.CapabilitySnapshot{}, fmt.Errorf("MCP manager is closing")
	}
	if m.sessions[server.ID] != nil || m.pending[server.ID] != nil {
		m.mu.Unlock()
		cancel()
		return mcpserver.CapabilitySnapshot{}, fmt.Errorf("MCP server is already connected or connecting")
	}
	m.pending[server.ID] = pending
	m.connectWG.Add(1)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.pending[server.ID] == pending {
			delete(m.pending, server.ID)
		}
		m.mu.Unlock()
		cancel()
		m.connectWG.Done()
	}()
	if m.observer != nil {
		m.observer.Starting(server.ID)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "sciaide", Title: "SciAide", Version: "0.3.0"}, &mcpsdk.ClientOptions{
		Capabilities: &mcpsdk.ClientCapabilities{},
		KeepAlive:    30 * time.Second,
		ToolListChangedHandler: func(context.Context, *mcpsdk.ToolListChangedRequest) {
			go m.refresh(server.ID)
		},
		PromptListChangedHandler: func(context.Context, *mcpsdk.PromptListChangedRequest) {
			go m.refresh(server.ID)
		},
		ResourceListChangedHandler: func(context.Context, *mcpsdk.ResourceListChangedRequest) {
			go m.refresh(server.ID)
		},
	})
	secretEnv := map[string]string{}
	var err error
	if len(server.SecretEnv) > 0 {
		if m.secrets == nil {
			return mcpserver.CapabilitySnapshot{}, fmt.Errorf("MCP secret resolver is not configured")
		}
		secretEnv, err = m.secrets.ResolveSecretEnv(connectCtx, server)
		if err != nil {
			return mcpserver.CapabilitySnapshot{}, err
		}
	}
	transport, err := m.transportFactory(server, secretEnv)
	if err != nil {
		return mcpserver.CapabilitySnapshot{}, err
	}
	session, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return mcpserver.CapabilitySnapshot{}, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = session.Close()
		}
	}()
	snapshot, adapters, err := discover(connectCtx, server, session)
	if err != nil {
		return mcpserver.CapabilitySnapshot{}, err
	}
	if err := m.registry.ReplaceNamespace(connectCtx, namespacePrefix(server.Namespace), adapters); err != nil {
		return mcpserver.CapabilitySnapshot{}, err
	}
	m.mu.Lock()
	if m.pending[server.ID] != pending || connectCtx.Err() != nil {
		m.mu.Unlock()
		_ = m.registry.ReplaceNamespace(context.Background(), namespacePrefix(server.Namespace), nil)
		return mcpserver.CapabilitySnapshot{}, fmt.Errorf("MCP connection was cancelled")
	}
	delete(m.pending, server.ID)
	m.sessions[server.ID] = &connection{server: server, session: session, capabilities: snapshot}
	m.mu.Unlock()
	closeOnError = false
	go m.monitor(server.ID, session)
	return snapshot, nil
}

func (m *Manager) Disconnect(_ context.Context, serverID string) error {
	serverID = strings.TrimSpace(serverID)
	m.mu.Lock()
	if pending := m.pending[serverID]; pending != nil {
		delete(m.pending, serverID)
		pending.cancel()
	}
	value := m.sessions[serverID]
	delete(m.sessions, serverID)
	m.mu.Unlock()
	if value == nil {
		return nil
	}
	_ = m.registry.ReplaceNamespace(context.Background(), namespacePrefix(value.server.Namespace), nil)
	return value.session.Close()
}

func (m *Manager) Connected(serverID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	serverID = strings.TrimSpace(serverID)
	return m.sessions[serverID] != nil || m.pending[serverID] != nil
}

func (m *Manager) Capabilities(serverID string) (mcpserver.CapabilitySnapshot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value := m.sessions[strings.TrimSpace(serverID)]
	if value == nil {
		return mcpserver.CapabilitySnapshot{}, false
	}
	return cloneSnapshot(value.capabilities), true
}

func (m *Manager) Close() error {
	m.mu.Lock()
	m.closing = true
	for id, pending := range m.pending {
		delete(m.pending, id)
		pending.cancel()
	}
	m.mu.Unlock()
	m.connectWG.Wait()
	m.mu.Lock()
	values := make([]*connection, 0, len(m.sessions))
	for _, value := range m.sessions {
		values = append(values, value)
	}
	clear(m.sessions)
	m.mu.Unlock()
	var first error
	for _, value := range values {
		_ = m.registry.ReplaceNamespace(context.Background(), namespacePrefix(value.server.Namespace), nil)
		if err := value.session.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) monitor(serverID string, session *mcpsdk.ClientSession) {
	err := session.Wait()
	m.mu.Lock()
	value := m.sessions[serverID]
	if value != nil && value.session == session {
		delete(m.sessions, serverID)
	}
	m.mu.Unlock()
	if value != nil && value.session == session {
		_ = m.registry.ReplaceNamespace(context.Background(), namespacePrefix(value.server.Namespace), nil)
		if m.observer != nil {
			endedErr := err
			if endedErr == nil {
				endedErr = fmt.Errorf("MCP session ended unexpectedly")
			}
			m.observer.RuntimeChanged(serverID, mcpserver.CapabilitySnapshot{}, endedErr)
		}
		if err != nil && m.logger != nil {
			m.logger.Warn("MCP session ended", "server_id", serverID, "error", redact(err.Error()))
		}
	}
}

func (m *Manager) refresh(serverID string) {
	m.mu.RLock()
	value := m.sessions[serverID]
	m.mu.RUnlock()
	if value == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(value.server.TimeoutSeconds)*time.Second)
	defer cancel()
	snapshot, adapters, err := discover(ctx, value.server, value.session)
	if err == nil {
		m.mu.Lock()
		current := m.sessions[serverID]
		if current == value {
			err = m.registry.ReplaceNamespace(ctx, namespacePrefix(value.server.Namespace), adapters)
			if err == nil {
				current.capabilities = snapshot
			}
		}
		m.mu.Unlock()
		if current != value {
			return
		}
	}
	if err != nil {
		m.mu.RLock()
		current := m.sessions[serverID]
		m.mu.RUnlock()
		if current != value {
			return
		}
		_ = m.registry.ReplaceNamespace(context.Background(), namespacePrefix(value.server.Namespace), nil)
		if m.logger != nil {
			m.logger.Warn("refresh MCP capabilities", "server_id", serverID, "error", redact(err.Error()))
		}
		if m.observer != nil {
			m.observer.RuntimeChanged(serverID, value.capabilities, err)
		}
		return
	}
	if m.observer != nil {
		m.observer.RuntimeChanged(serverID, snapshot, nil)
	}
}

func buildTransport(server mcpserver.Server, secretEnv map[string]string) (mcpsdk.Transport, error) {
	switch server.Transport {
	case mcpserver.TransportStdio:
		command := exec.Command(server.Command, server.Args...)
		configureBackgroundCommand(command)
		command.Dir = server.WorkingDir
		command.Env = minimalEnvironment(server.Env, secretEnv)
		return &mcpsdk.CommandTransport{Command: command, TerminateDuration: 3 * time.Second}, nil
	case mcpserver.TransportStreamableHTTP:
		baseTransport := http.DefaultTransport.(*http.Transport).Clone()
		baseTransport.Proxy = http.ProxyFromEnvironment
		client := &http.Client{
			Timeout:   time.Duration(server.TimeoutSeconds) * time.Second,
			Transport: &headerTransport{base: baseTransport, headers: cloneMap(server.Headers)},
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many MCP HTTP redirects")
				}
				if err := validateRedirect(server.URL, request.URL); err != nil {
					return err
				}
				return nil
			},
		}
		return &mcpsdk.StreamableClientTransport{Endpoint: server.URL, HTTPClient: client, MaxRetries: 3}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport")
	}
}

func validateRedirect(configured string, target *url.URL) error {
	original, err := url.Parse(configured)
	if err != nil || target == nil {
		return fmt.Errorf("invalid MCP redirect")
	}
	if !strings.EqualFold(original.Scheme, target.Scheme) || !strings.EqualFold(original.Host, target.Host) {
		return fmt.Errorf("MCP redirects may not change scheme or authority")
	}
	return nil
}

func discover(ctx context.Context, server mcpserver.Server, session *mcpsdk.ClientSession) (mcpserver.CapabilitySnapshot, []tool.Tool, error) {
	snapshot := mcpserver.CapabilitySnapshot{Tools: []mcpserver.ToolInfo{}, Resources: []string{}, Prompts: []string{}}
	adapters := []tool.Tool{}
	initialized := session.InitializeResult()
	if initialized == nil {
		return snapshot, nil, fmt.Errorf("MCP initialize result is missing")
	}
	snapshot.ProtocolVersion = initialized.ProtocolVersion
	if initialized.ServerInfo != nil {
		snapshot.ServerVersion = initialized.ServerInfo.Version
	}
	capabilities := initialized.Capabilities
	if capabilities != nil && capabilities.Tools != nil {
		for native, err := range session.Tools(ctx, nil) {
			if err != nil {
				return snapshot, nil, fmt.Errorf("list MCP tools: %w", err)
			}
			adapter, info, err := newToolAdapter(server, native, session)
			if err != nil {
				return snapshot, nil, err
			}
			adapters = append(adapters, adapter)
			snapshot.Tools = append(snapshot.Tools, info)
		}
	}
	if capabilities != nil && capabilities.Resources != nil {
		for resource, err := range session.Resources(ctx, nil) {
			if err != nil {
				return snapshot, nil, fmt.Errorf("list MCP resources: %w", err)
			}
			if resource != nil && strings.TrimSpace(resource.URI) != "" {
				snapshot.Resources = append(snapshot.Resources, resource.URI)
			}
		}
	}
	if capabilities != nil && capabilities.Prompts != nil {
		for prompt, err := range session.Prompts(ctx, nil) {
			if err != nil {
				return snapshot, nil, fmt.Errorf("list MCP prompts: %w", err)
			}
			if prompt != nil && strings.TrimSpace(prompt.Name) != "" {
				snapshot.Prompts = append(snapshot.Prompts, prompt.Name)
			}
		}
	}
	mcpserver.SortSnapshot(&snapshot)
	return snapshot, adapters, nil
}

type toolAdapter struct {
	serverID string
	original string
	session  *mcpsdk.ClientSession
	def      tool.Definition
}

func newToolAdapter(server mcpserver.Server, native *mcpsdk.Tool, session *mcpsdk.ClientSession) (*toolAdapter, mcpserver.ToolInfo, error) {
	if native == nil || strings.TrimSpace(native.Name) == "" {
		return nil, mcpserver.ToolInfo{}, fmt.Errorf("MCP returned a tool without a name")
	}
	input, err := json.Marshal(native.InputSchema)
	if err != nil {
		return nil, mcpserver.ToolInfo{}, fmt.Errorf("encode MCP tool input schema: %w", err)
	}
	output := json.RawMessage(nil)
	if native.OutputSchema != nil {
		output, err = json.Marshal(native.OutputSchema)
		if err != nil {
			return nil, mcpserver.ToolInfo{}, fmt.Errorf("encode MCP tool output schema: %w", err)
		}
	}
	qualified := namespacePrefix(server.Namespace) + sanitizeName(native.Name)
	version := definitionVersion(server.ID, native.Name, input, output)
	description := strings.TrimSpace(native.Description)
	if description == "" {
		description = "MCP tool " + native.Name
	}
	if len(description) > 4000 {
		description = description[:4000]
	}
	def := tool.Definition{QualifiedName: qualified, Description: description, InputSchema: input, OutputSchema: output, Risk: tool.RiskModerate, Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionToolInvoke, Resource: qualified}}, Idempotent: false, Version: version}
	if err := tool.ValidateDefinition(def); err != nil {
		return nil, mcpserver.ToolInfo{}, fmt.Errorf("invalid MCP tool %q: %w", native.Name, err)
	}
	adapter := &toolAdapter{serverID: server.ID, original: native.Name, session: session, def: tool.SnapshotDefinition(def)}
	return adapter, mcpserver.ToolInfo{OriginalName: native.Name, QualifiedName: qualified, Description: description, InputSchema: input, OutputSchema: output, Version: version}, nil
}

func (t *toolAdapter) Definition(context.Context) (tool.Definition, error) {
	return tool.SnapshotDefinition(t.def), nil
}

func (t *toolAdapter) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	var arguments map[string]any
	if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
		return tool.Result{}, err
	}
	result, err := t.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.original, Arguments: arguments})
	if err != nil {
		return tool.Result{}, fmt.Errorf("MCP tool call failed: %w", err)
	}
	if result == nil {
		return tool.Result{}, fmt.Errorf("MCP tool returned no result")
	}
	text, artifacts := encodeContent(result.Content)
	structured := json.RawMessage(nil)
	if result.StructuredContent != nil {
		structured, err = json.Marshal(result.StructuredContent)
		if err != nil {
			return tool.Result{}, fmt.Errorf("encode MCP structured result: %w", err)
		}
	}
	status := tool.ResultSuccess
	if result.IsError {
		status = tool.ResultError
	}
	return tool.Result{Status: status, Text: text, Structured: structured, Artifacts: artifacts, Citations: []tool.CitationRef{}}, nil
}

func encodeContent(values []mcpsdk.Content) (string, []tool.ArtifactRef) {
	parts := make([]string, 0, len(values))
	artifacts := []tool.ArtifactRef{}
	for index, content := range values {
		switch value := content.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, value.Text)
		case *mcpsdk.ImageContent:
			artifacts = append(artifacts, tool.ArtifactRef{ID: fmt.Sprintf("mcp-image-%d", index), Name: "MCP image", MIMEType: value.MIMEType})
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", value.MIMEType, decodedSize(value.Data)))
		case *mcpsdk.AudioContent:
			artifacts = append(artifacts, tool.ArtifactRef{ID: fmt.Sprintf("mcp-audio-%d", index), Name: "MCP audio", MIMEType: value.MIMEType})
			parts = append(parts, fmt.Sprintf("[audio %s, %d bytes]", value.MIMEType, decodedSize(value.Data)))
		case *mcpsdk.EmbeddedResource:
			if value.Resource != nil {
				parts = append(parts, value.Resource.Text)
			}
		default:
			encoded, _ := content.MarshalJSON()
			parts = append(parts, string(encoded))
		}
	}
	return strings.Join(parts, "\n"), artifacts
}

func namespacePrefix(namespace string) string {
	return "mcp." + strings.ToLower(strings.TrimSpace(namespace)) + "."
}

func sanitizeName(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	result := strings.Trim(b.String(), "_-")
	if result == "" {
		result = "tool"
	}
	if len(result) > 96 {
		result = result[:96]
	}
	return result
}

func definitionVersion(serverID, name string, input, output []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(serverID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(name))
	_, _ = hash.Write(input)
	_, _ = hash.Write(output)
	return "mcp-" + hex.EncodeToString(hash.Sum(nil)[:8])
}

func minimalEnvironment(extra, secrets map[string]string) []string {
	allowed := map[string]string{}
	for _, key := range []string{"PATH", "PATHEXT", "SYSTEMROOT", "WINDIR", "COMSPEC", "TEMP", "TMP", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA"} {
		if value := os.Getenv(key); value != "" {
			allowed[key] = value
		}
	}
	for key, value := range extra {
		allowed[key] = value
	}
	for key, value := range secrets {
		allowed[key] = value
	}
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+allowed[key])
	}
	return result
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for key, value := range t.headers {
		cloned.Header.Set(key, value)
	}
	return t.base.RoundTrip(cloned)
}

func cloneMap(values map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range values {
		result[key] = value
	}
	return result
}
func cloneSnapshot(value mcpserver.CapabilitySnapshot) mcpserver.CapabilitySnapshot {
	value.Tools = append([]mcpserver.ToolInfo(nil), value.Tools...)
	value.Resources = append([]string(nil), value.Resources...)
	value.Prompts = append([]string(nil), value.Prompts...)
	return value
}
func decodedSize(value []byte) int {
	decoded, err := base64.StdEncoding.DecodeString(string(value))
	if err == nil {
		return len(decoded)
	}
	return len(value)
}
func redact(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}
