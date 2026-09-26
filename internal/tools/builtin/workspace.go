package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
		Description:   "列出当前 Workspace 目录的一层内容，不递归；limit 是条目数。允许查看 .sciaide 顶层受控目录概览，不允许深入内部存储。科研任务相对路径基于当前任务工作区。托管材料请用 attachment.list / document.inspect / document.read；MCP 配置请在设置 → MCP 查看，不要扫描内部目录或改用 Shell 绕过拒绝。",
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
	if err := checkWorkspaceReadPath(guard, args.Path, workspaceList); err != nil {
		return tool.Result{}, err
	}
	directory, clean, err := guard.OpenFile(args.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("Workspace 目录不存在：%s。请先调用 builtin.workspace.list，或使用当前任务提供的精确路径。", filepath.ToSlash(filepath.Clean(args.Path))))
		}
		return tool.Result{}, workspaceOpenError(err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return tool.Result{}, tool.NewUserFacingError("指定路径不是可列出的目录；文本文件请使用 builtin.workspace.read_text，文献请使用 builtin.document.read。")
	}
	var entries []os.DirEntry
	if strings.EqualFold(clean, project.PrivateDirectoryName) {
		// Enumerate only known children, not arbitrary private file names.
		for _, name := range workspacePrivateOverview {
			file, _, openErr := guard.OpenFile(filepath.Join(clean, name))
			if openErr != nil {
				continue
			}
			info, statErr := file.Stat()
			file.Close()
			if statErr == nil && info.IsDir() {
				entries = append(entries, fs.FileInfoToDirEntry(info))
			}
		}
	} else {
		entries, err = directory.ReadDir(args.Limit + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return tool.Result{}, workspaceOpenError(err)
		}
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
	summary := fmt.Sprintf("列出了 %d 个 Workspace 条目。", len(values))
	if strings.EqualFold(clean, project.PrivateDirectoryName) {
		summary += "这是内部目录概览（隐藏未知文件及配置），不是所有文件清单。attachments/artifacts 是托管材料与产物；tasks 是隔离的任务工作区；cache/python/browser/tmp 是缓存、运行环境和临时数据。请使用对应专用工具或设置入口，不要通过 Shell/Python 绕过内部存储限制；禁止直接修改内部索引、配置与任务状态。"
	}
	return tool.Result{Status: tool.ResultSuccess, Text: summary, Structured: structured, Truncated: truncated}, nil
}

func (*ReadText) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ReadTextName,
		Description:   "只读当前 Workspace 的 UTF-8 文件，支持有界分页；科研任务中相对路径基于当前任务工作区，可读取自己的输入、脚本和结果。不能读取 .sciaide 内部数据库、配置、Cookie 或跨任务存储；托管文献请使用 attachment.list / document.read 保留归属和原文定位。此工具不修改文件。",
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
	if err := checkWorkspaceReadPath(guard, args.Path, workspaceRead); err != nil {
		return tool.Result{}, err
	}
	file, clean, err := guard.OpenFile(args.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tool.Result{}, tool.NewUserFacingError(fmt.Sprintf("Workspace 文件不存在：%s。请先调用 builtin.workspace.list，或使用 workflow_state.inputs.input_paths 中的精确路径。", filepath.ToSlash(filepath.Clean(args.Path))))
		}
		return tool.Result{}, workspaceOpenError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return tool.Result{}, err
	}
	if !info.Mode().IsRegular() {
		return tool.Result{}, tool.NewUserFacingError("指定路径不是普通文件；目录请使用 builtin.workspace.list。")
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
		return tool.Result{}, tool.NewUserFacingError("文件不是可读取的 UTF-8 文本；PDF、Word 等研究材料请先通过资料入口添加，再用 builtin.document.inspect / builtin.document.read 读取。")
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

type workspaceReadOperation int

const (
	workspaceList workspaceReadOperation = iota
	workspaceRead
)

var workspacePrivateOverview = []string{"attachments", "artifacts", "tasks", "cache", "python", "browser", "tmp"}

// Materialization and local-execution working/output paths remain stricter
// than directory inspection. This does not sandbox arbitrary script contents.
func rejectPrivateProjectPath(guard *pathguard.Guard, value string) error {
	return checkWorkspaceReadPath(guard, value, workspaceRead)
}

func workspaceOpenError(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return tool.NewUserFacingError("操作系统拒绝读取此路径，请检查文件访问权限；不会自动提权或改用其他工具绕过。")
	}
	return tool.NewUserFacingError("无法安全打开 Workspace 路径：可能为符号链接/重解析点、路径无效或文件不可访问。请使用当前工作区内的真实相对路径；文献使用 builtin.document.read，内部配置通过设置页面查看。")
}

func checkWorkspaceReadPath(guard *pathguard.Guard, value string, operation workspaceReadOperation) error {
	clean, err := guard.Relative(value)
	if err != nil {
		return tool.NewUserFacingError("Workspace 路径必须是当前工作区内的相对路径，不能使用绝对路径或越过根目录；请先用 builtin.workspace.list 查看可用路径。科研任务以当前任务目录为根。")
	}
	parts := strings.FieldsFunc(clean, func(r rune) bool { return r == '/' || r == '\\' })
	for i, part := range parts {
		// Windows normalizes trailing dots/spaces in path components.
		if !strings.EqualFold(strings.TrimRight(part, ". "), project.PrivateDirectoryName) {
			continue
		}
		if operation == workspaceList && i == 0 && len(parts) == 1 && strings.EqualFold(part, project.PrivateDirectoryName) {
			return nil
		}
		hint := "内部配置、数据库、缓存和浏览器 Cookie 不通过通用文件工具开放；项目/网络/Python/MCP 配置请在设置或对应环境面板查看。"
		if i+1 < len(parts) {
			switch strings.ToLower(parts[i+1]) {
			case "attachments":
				hint = "托管研究材料请用 builtin.attachment.list 获取 ID，再用 builtin.document.inspect / builtin.document.read 读取，以保留材料归属和定位。"
			case "artifacts":
				hint = "已登记产物请在科研产物面板查看；本次分析尚未登记的结果请按当前任务提供的工作区相对路径读取。"
			case "tasks":
				hint = "任务存储不能从项目根目录跨任务遍历；请进入对应科研任务，使用任务工作区相对路径读取其输入、脚本和结果。"
			}
		}
		return tool.NewUserFacingError("此路径属于 SciAide 内部受管数据，仅允许查看 .sciaide 顶层目录概览。" + hint + " 不要改用 Shell/Python 绕过拒绝，也不要直接修改内部状态；需要修改请通过对应管理入口。")
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
