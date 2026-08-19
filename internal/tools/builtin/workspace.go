package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

const (
	ListWorkspaceName = "builtin.workspace.list"
	ReadTextName      = "builtin.workspace.read_text"
	defaultListLimit  = 200
	maxListLimit      = 500
	defaultReadBytes  = 128 * 1024
	maxReadBytes      = 256 * 1024
)

type ProjectLoader interface {
	Get(ctx context.Context, projectID string) (project.Project, error)
}

type ListWorkspace struct{ projects ProjectLoader }
type ReadText struct{ projects ProjectLoader }

func NewListWorkspace(projects ProjectLoader) *ListWorkspace {
	return &ListWorkspace{projects: projects}
}
func NewReadText(projects ProjectLoader) *ReadText { return &ReadText{projects: projects} }

func (*ListWorkspace) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ListWorkspaceName,
		Description:   "列出当前科研项目 Workspace 中指定目录的一层内容，不递归。",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"path":{"type":"string","maxLength":4096},"limit":{"type":"integer","minimum":1,"maximum":500}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","entries","truncated"],"properties":{"path":{"type":"string"},"entries":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["name","path","kind","size"],"properties":{"name":{"type":"string"},"path":{"type":"string"},"kind":{"type":"string","enum":["file","directory","symlink","other"]},"size":{"type":"integer","minimum":0}}}},"truncated":{"type":"boolean"}}}`),
		Risk:          tool.RiskLow,
		Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
		Idempotent:    true,
		Version:       "1",
	}, nil
}

func (t *ListWorkspace) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.projects == nil {
		return tool.Result{}, fmt.Errorf("project loader is not configured")
	}
	var args struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Limit == 0 {
		args.Limit = defaultListLimit
	}
	if args.Limit < 1 || args.Limit > maxListLimit {
		return tool.Result{}, fmt.Errorf("directory limit is invalid")
	}
	guard, err := guardForProject(ctx, t.projects, invocation.ProjectID)
	if err != nil {
		return tool.Result{}, err
	}
	defer guard.Close()
	if err := rejectPrivateProjectPath(guard, args.Path); err != nil {
		return tool.Result{}, err
	}
	directory, clean, err := guard.OpenFile(args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return tool.Result{}, fmt.Errorf("workspace path is not a directory")
	}
	entries, err := directory.ReadDir(args.Limit + 2)
	if err != nil && !errors.Is(err, io.EOF) {
		return tool.Result{}, err
	}
	if clean == "." {
		visible := entries[:0]
		for _, entry := range entries {
			if !strings.EqualFold(entry.Name(), project.PrivateDirectoryName) {
				visible = append(visible, entry)
			}
		}
		entries = visible
	}
	truncated := len(entries) > args.Limit
	if truncated {
		entries = entries[:args.Limit]
	}
	type entryDTO struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Kind string `json:"kind"`
		Size int64  `json:"size"`
	}
	values := make([]entryDTO, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return tool.Result{}, err
		}
		kind := "other"
		typeBits := entry.Type()
		switch {
		case typeBits&os.ModeSymlink != 0:
			kind = "symlink"
		case entry.IsDir():
			kind = "directory"
		case typeBits.IsRegular():
			kind = "file"
		}
		size := int64(0)
		if kind == "file" {
			if itemInfo, infoErr := entry.Info(); infoErr == nil {
				size = itemInfo.Size()
			}
		}
		path := entry.Name()
		if clean != "." {
			path = filepath.Join(clean, entry.Name())
		}
		values = append(values, entryDTO{Name: entry.Name(), Path: filepath.ToSlash(path), Kind: kind, Size: size})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Kind == "directory" && values[j].Kind != "directory" {
			return true
		}
		if values[i].Kind != "directory" && values[j].Kind == "directory" {
			return false
		}
		return strings.ToLower(values[i].Name) < strings.ToLower(values[j].Name)
	})
	payload := struct {
		Path      string     `json:"path"`
		Entries   []entryDTO `json:"entries"`
		Truncated bool       `json:"truncated"`
	}{Path: filepath.ToSlash(clean), Entries: values, Truncated: truncated}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("列出了 %d 个 Workspace 条目。", len(values)), Structured: structured, Truncated: truncated}, nil
}

func (*ReadText) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ReadTextName,
		Description:   "读取当前科研项目 Workspace 中一个 UTF-8 文本文件的有界内容。",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","content","bytesRead","originalBytes","truncated"],"properties":{"path":{"type":"string"},"content":{"type":"string"},"bytesRead":{"type":"integer","minimum":0},"originalBytes":{"type":"integer","minimum":0},"truncated":{"type":"boolean"}}}`),
		Risk:          tool.RiskLow,
		Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "."}},
		Idempotent:    true,
		Version:       "1",
	}, nil
}

func (t *ReadText) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.projects == nil {
		return tool.Result{}, fmt.Errorf("project loader is not configured")
	}
	var args struct {
		Path     string `json:"path"`
		MaxBytes int    `json:"maxBytes"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.MaxBytes == 0 {
		args.MaxBytes = defaultReadBytes
	}
	if args.MaxBytes < 1 || args.MaxBytes > maxReadBytes {
		return tool.Result{}, fmt.Errorf("read limit is invalid")
	}
	guard, err := guardForProject(ctx, t.projects, invocation.ProjectID)
	if err != nil {
		return tool.Result{}, err
	}
	defer guard.Close()
	if err := rejectPrivateProjectPath(guard, args.Path); err != nil {
		return tool.Result{}, err
	}
	file, clean, err := guard.OpenFile(args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return tool.Result{}, err
	}
	if !info.Mode().IsRegular() {
		return tool.Result{}, fmt.Errorf("workspace path is not a regular file")
	}
	reader := bufio.NewReader(io.LimitReader(file, int64(args.MaxBytes)+1))
	contents, err := io.ReadAll(reader)
	if err != nil {
		return tool.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	truncated := len(contents) > args.MaxBytes
	if truncated {
		contents = contents[:args.MaxBytes]
		for len(contents) > 0 && !utf8.Valid(contents) {
			contents = contents[:len(contents)-1]
		}
	}
	if !utf8.Valid(contents) || containsBinary(contents) {
		return tool.Result{}, fmt.Errorf("workspace file is not supported UTF-8 text")
	}
	payload := struct {
		Path          string `json:"path"`
		Content       string `json:"content"`
		BytesRead     int    `json:"bytesRead"`
		OriginalBytes int64  `json:"originalBytes"`
		Truncated     bool   `json:"truncated"`
	}{Path: filepath.ToSlash(clean), Content: string(contents), BytesRead: len(contents), OriginalBytes: info.Size(), Truncated: truncated}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: string(contents), Structured: structured, Truncated: truncated, Meta: tool.ResultMeta{OriginalBytes: info.Size()}}, nil
}

func rejectPrivateProjectPath(guard *pathguard.Guard, value string) error {
	clean, err := guard.Relative(value)
	if err != nil {
		return err
	}
	first := clean
	if separator := strings.IndexRune(clean, os.PathSeparator); separator >= 0 {
		first = clean[:separator]
	}
	if strings.EqualFold(first, project.PrivateDirectoryName) {
		return fmt.Errorf("SciAide project data is available only through project-scoped tools")
	}
	return nil
}

func guardForProject(ctx context.Context, projects ProjectLoader, projectID string) (*pathguard.Guard, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	value, err := projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return pathguard.Open(value.WorkspacePath)
}

func containsBinary(contents []byte) bool {
	for _, value := range contents {
		if value == 0 {
			return true
		}
	}
	return false
}
