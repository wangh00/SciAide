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
	guard, err := guardForInvocation(ctx, t.projects, invocation)
	if err != nil {
		return tool.Result{}, err
	}
	defer guard.Close()
	if err := rejectPrivateProjectPath(guard, args.Path); err != nil {
		return tool.Result{}, err
	}
	directory, clean, err := guard.OpenFile(args.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("Workspace 目录不存在：%s。请先调用 builtin.workspace.list，或使用当前任务提供的精确路径。", filepath.ToSlash(filepath.Clean(args.Path))))
		}
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
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"offset":{"type":"integer","minimum":0,"maximum":67108864},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`),
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
		Offset   int64  `json:"offset"`
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
	if args.Offset < 0 || args.Offset > 67108864 {
		return tool.Result{}, fmt.Errorf("read offset is invalid")
	}
	guard, err := guardForInvocation(ctx, t.projects, invocation)
	if err != nil {
		return tool.Result{}, err
	}
	defer guard.Close()
	if err := rejectPrivateProjectPath(guard, args.Path); err != nil {
		return tool.Result{}, err
	}
	file, clean, err := guard.OpenFile(args.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("Workspace 文件不存在：%s。请先调用 builtin.workspace.list，或使用 workflow_state.inputs.input_paths 中的精确路径。", filepath.ToSlash(filepath.Clean(args.Path))))
		}
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
	if args.Offset > info.Size() {
		return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("Workspace 文件偏移超出范围：%s 的大小为 %d 字节，offset 不能超过该值。", filepath.ToSlash(clean), info.Size()))
	}
	if args.Offset > 0 {
		if _, err := file.Seek(args.Offset, io.SeekStart); err != nil {
			return tool.Result{}, fmt.Errorf("seek workspace file: %w", err)
		}
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
	}
	contents = trimUTF8Window(contents, args.Offset > 0)
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

// trimUTF8Window removes only incomplete UTF-8 runes introduced by a byte
// offset or the bounded read. Invalid bytes in the middle of a file are left
// intact and rejected by the caller instead of being silently skipped.
func trimUTF8Window(contents []byte, allowPartialPrefix bool) []byte {
	if utf8.Valid(contents) {
		return contents
	}
	// A page may begin on a UTF-8 continuation byte when the caller uses a
	// byte offset. Only continuation bytes are safe to discard at the prefix;
	// an invalid lead byte must remain visible and be rejected below.
	start := 0
	if allowPartialPrefix {
		for start < len(contents) && start < 3 && contents[start]&0xc0 == 0x80 {
			start++
		}
	}
	if start > 0 {
		contents = contents[start:]
	}
	if utf8.Valid(contents) {
		return contents
	}
	// A bounded page can end in an incomplete multi-byte rune. Remove only a
	// suffix that is provably a valid UTF-8 prefix; an invalid lead byte such
	// as 0xff remains and is rejected by the caller.
	for cut := 1; cut <= 3 && cut < len(contents); cut++ {
		candidate := contents[:len(contents)-cut]
		tail := contents[len(contents)-cut:]
		if utf8.Valid(candidate) && isIncompleteUTF8Prefix(tail) {
			return candidate
		}
	}
	return contents
}

func isIncompleteUTF8Prefix(value []byte) bool {
	if len(value) == 0 || len(value) > 3 {
		return false
	}
	width := utf8RuneWidth(value[0])
	if width == 0 || width <= len(value) {
		return false
	}
	for _, item := range value[1:] {
		if item&0xc0 != 0x80 {
			return false
		}
	}
	return true
}

func utf8RuneWidth(value byte) int {
	switch {
	case value < 0x80:
		return 1
	case value >= 0xc2 && value <= 0xdf:
		return 2
	case value >= 0xe0 && value <= 0xef:
		return 3
	case value >= 0xf0 && value <= 0xf4:
		return 4
	default:
		return 0
	}
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

func guardForInvocation(ctx context.Context, projects ProjectLoader, invocation tool.Invocation) (*pathguard.Guard, error) {
	if strings.TrimSpace(invocation.WorkspaceRoot) != "" {
		return pathguard.Open(invocation.WorkspaceRoot)
	}
	if taskID := strings.TrimSpace(invocation.ResearchTaskID); taskID != "" {
		selected, err := projects.Get(ctx, invocation.ProjectID)
		if err != nil {
			return nil, err
		}
		root, err := project.ResearchTaskWorkspacePath(selected, taskID)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, fmt.Errorf("create research task workspace: %w", err)
		}
		return pathguard.Open(root)
	}
	return guardForProject(ctx, projects, invocation.ProjectID)
}

func containsBinary(contents []byte) bool {
	for _, value := range contents {
		if value == 0 {
			return true
		}
	}
	return false
}
