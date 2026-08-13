package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
)

const maxImportDocumentBytes = 256 * 1024

// ImportCommand accepts the common Claude Desktop/Cursor style MCP document.
// The field is intentionally a string so parsing and secret classification stay
// inside the trusted application boundary instead of the webview.
type ImportCommand struct {
	JSON string `json:"json"`
}

type ImportError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

type ImportResult struct {
	Imported []Server      `json:"imported"`
	Errors   []ImportError `json:"errors"`
}

type compatibleDocument struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

type compatibleServer struct {
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	CWD        string            `json:"cwd"`
	WorkingDir string            `json:"workingDir"`
	URL        string            `json:"url"`
	Type       string            `json:"type"`
	Transport  string            `json:"transport"`
	Headers    map[string]string `json:"headers"`
	Enabled    *bool             `json:"enabled"`
	Disabled   bool              `json:"disabled"`
}

// Import parses and persists independent MCP entries. A malformed document is
// rejected as a whole, while an invalid server is reported without preventing
// other valid entries from being imported.
func (s *Service) Import(ctx context.Context, request ImportCommand) (ImportResult, error) {
	result := ImportResult{Imported: []Server{}, Errors: []ImportError{}}
	documentJSON := strings.TrimSpace(request.JSON)
	if documentJSON == "" {
		return result, fmt.Errorf("MCP import JSON is required")
	}
	if len(documentJSON) > maxImportDocumentBytes {
		return result, fmt.Errorf("MCP import JSON exceeds the 256 KiB limit")
	}

	var document compatibleDocument
	decoder := json.NewDecoder(strings.NewReader(documentJSON))
	if err := decoder.Decode(&document); err != nil {
		return result, fmt.Errorf("invalid MCP import JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return result, fmt.Errorf("invalid MCP import JSON: multiple top-level values")
	}
	if document.MCPServers == nil {
		return result, fmt.Errorf(`MCP import JSON must contain an "mcpServers" object`)
	}
	if len(document.MCPServers) == 0 {
		return result, fmt.Errorf(`"mcpServers" must contain at least one server`)
	}
	if len(document.MCPServers) > 128 {
		return result, fmt.Errorf(`"mcpServers" cannot contain more than 128 servers`)
	}

	existing, err := s.List(ctx)
	if err != nil {
		return result, err
	}
	occupiedNamespaces := make(map[string]struct{}, len(existing)+len(document.MCPServers))
	occupiedNames := make(map[string]struct{}, len(existing)+len(document.MCPServers))
	for _, server := range existing {
		occupiedNamespaces[strings.ToLower(server.Namespace)] = struct{}{}
		occupiedNames[strings.ToLower(strings.TrimSpace(server.Name))] = struct{}{}
	}

	names := make([]string, 0, len(document.MCPServers))
	for name := range document.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			result.Errors = append(result.Errors, ImportError{Name: name, Message: "server name cannot be empty"})
			continue
		}
		nameKey := strings.ToLower(trimmedName)
		if _, exists := occupiedNames[nameKey]; exists {
			result.Errors = append(result.Errors, ImportError{Name: trimmedName, Message: "a server with this name already exists; existing configuration was not overwritten"})
			continue
		}

		var input compatibleServer
		if err := json.Unmarshal(document.MCPServers[name], &input); err != nil {
			result.Errors = append(result.Errors, ImportError{Name: trimmedName, Message: "invalid server configuration: " + publicError(err)})
			continue
		}
		command, err := compatibleSaveCommand(trimmedName, input, occupiedNamespaces)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{Name: trimmedName, Message: publicError(err)})
			continue
		}
		server, err := s.Save(ctx, command)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{Name: trimmedName, Message: publicError(err)})
			continue
		}
		result.Imported = append(result.Imported, server)
		occupiedNames[nameKey] = struct{}{}
		occupiedNamespaces[server.Namespace] = struct{}{}
	}
	return result, nil
}

func compatibleSaveCommand(name string, input compatibleServer, occupied map[string]struct{}) (SaveCommand, error) {
	command, endpoint := strings.TrimSpace(input.Command), strings.TrimSpace(input.URL)
	if command != "" && endpoint != "" {
		return SaveCommand{}, fmt.Errorf("command and url cannot be configured together")
	}
	if command == "" && endpoint == "" {
		return SaveCommand{}, fmt.Errorf("either command or url is required")
	}

	transportHint := strings.ToLower(strings.TrimSpace(input.Transport))
	typeHint := strings.ToLower(strings.TrimSpace(input.Type))
	if transportHint != "" && typeHint != "" && normalizeTransportHint(transportHint) != normalizeTransportHint(typeHint) {
		return SaveCommand{}, fmt.Errorf("type and transport describe different transports")
	}
	hint := transportHint
	if hint == "" {
		hint = typeHint
	}
	normalizedHint := normalizeTransportHint(hint)
	if hint != "" && normalizedHint == "" {
		return SaveCommand{}, fmt.Errorf("unsupported MCP transport %q", hint)
	}

	workingDir := strings.TrimSpace(input.CWD)
	if strings.TrimSpace(input.WorkingDir) != "" {
		if workingDir != "" && workingDir != strings.TrimSpace(input.WorkingDir) {
			return SaveCommand{}, fmt.Errorf("cwd and workingDir cannot contain different paths")
		}
		workingDir = strings.TrimSpace(input.WorkingDir)
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.Disabled {
		enabled = false
	}

	namespace := availableImportNamespace(name, occupied)
	result := SaveCommand{
		Name:           name,
		Namespace:      namespace,
		Enabled:        enabled,
		AutoStart:      false,
		Trust:          TrustUntrusted,
		TimeoutSeconds: 30,
		Headers:        map[string]string{},
		Env:            map[string]string{},
		SecretValues:   map[string]string{},
	}
	if command != "" {
		if normalizedHint != "" && normalizedHint != TransportStdio {
			return SaveCommand{}, fmt.Errorf("command configuration conflicts with HTTP transport")
		}
		if endpoint != "" || len(input.Headers) > 0 {
			return SaveCommand{}, fmt.Errorf("stdio MCP cannot contain HTTP settings")
		}
		result.Transport = TransportStdio
		result.Command = command
		result.Args = cloneStrings(input.Args)
		result.WorkingDir = workingDir
		for key, value := range input.Env {
			if sensitiveEnvironmentName(key) {
				result.SecretValues[key] = value
			} else {
				result.Env[key] = value
			}
		}
		return result, nil
	}

	if normalizedHint != "" && normalizedHint != TransportStreamableHTTP {
		return SaveCommand{}, fmt.Errorf("url configuration conflicts with stdio transport")
	}
	if len(input.Args) > 0 || len(input.Env) > 0 || workingDir != "" {
		return SaveCommand{}, fmt.Errorf("HTTP MCP cannot contain stdio process settings")
	}
	result.Transport = TransportStreamableHTTP
	result.URL = endpoint
	result.Headers = cloneMap(input.Headers)
	return result, nil
}

func normalizeTransportHint(value string) Transport {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "stdio":
		return TransportStdio
	case "http", "streamable-http", "streamable_http", "streamablehttp":
		return TransportStreamableHTTP
	default:
		return ""
	}
}

func availableImportNamespace(name string, occupied map[string]struct{}) string {
	base := importNamespace(name)
	if _, exists := occupied[base]; !exists {
		return base
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
	digest := hex.EncodeToString(sum[:4])
	for attempt := 0; ; attempt++ {
		suffix := digest
		if attempt > 0 {
			suffix = fmt.Sprintf("%s-%x", digest, attempt)
		}
		maxBase := 32 - len(suffix) - 1
		prefix := "s"
		if maxBase > 0 {
			prefix = strings.TrimRight(base[:min(len(base), maxBase)], "-_")
		}
		candidate := prefix + "-" + suffix
		if _, exists := occupied[candidate]; !exists {
			return candidate
		}
	}
}

func importNamespace(name string) string {
	var builder strings.Builder
	separator := false
	for _, char := range strings.ToLower(strings.TrimSpace(name)) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			builder.WriteRune(char)
			separator = false
			continue
		}
		if !separator {
			builder.WriteByte('-')
			separator = true
		}
	}
	value := strings.Trim(builder.String(), "-_")
	if value == "" {
		value = "server"
	}
	if !unicode.IsLetter(rune(value[0])) || value[0] > unicode.MaxASCII {
		value = "server-" + value
	}
	if len(value) > 32 {
		value = strings.TrimRight(value[:32], "-_")
	}
	return value
}

func sensitiveEnvironmentName(name string) bool {
	value := strings.ToUpper(strings.TrimSpace(name))
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "AUTH", "CREDENTIAL", "COOKIE"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
