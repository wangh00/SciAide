package builtin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangh00/SciAide/internal/app/project"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
	"github.com/wangh00/SciAide/internal/platform/filepublish"
	"github.com/wangh00/SciAide/internal/tools/pathguard"
)

const (
	ReadSkillResourceName        = "builtin.skill.resource.read_text"
	ListSkillResourcesName       = "builtin.skill.resource.list"
	MaterializeSkillResourceName = "builtin.skill.resource.materialize"
)

type SkillResourceLoader interface {
	ReadResource(ctx context.Context, runID, name, resourcePath string, offset, maxBytes int) (opensciskill.Resource, error)
}

type SkillResourceLister interface {
	ListResources(ctx context.Context, runID, name string) (opensciskill.ResourceList, error)
}

type SkillResourceMaterializer interface {
	MaterializeResource(ctx context.Context, runID, name, resourcePath string) (opensciskill.MaterializedResource, error)
}

type ReadSkillResource struct{ skills SkillResourceLoader }
type ListSkillResources struct{ skills SkillResourceLister }
type MaterializeSkillResource struct {
	skills   SkillResourceMaterializer
	projects ProjectLoader
}

func NewReadSkillResource(skills SkillResourceLoader) *ReadSkillResource {
	return &ReadSkillResource{skills: skills}
}

func NewListSkillResources(skills SkillResourceLister) *ListSkillResources {
	return &ListSkillResources{skills: skills}
}

func NewMaterializeSkillResource(skills SkillResourceMaterializer, projects ProjectLoader) *MaterializeSkillResource {
	return &MaterializeSkillResource{skills: skills, projects: projects}
}

func (*ListSkillResources) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ListSkillResourcesName,
		Description:   "List the immutable package resources of a Skill already loaded in this Run. Returns package-relative path, class, size, media type and whether bounded UTF-8 text reading is supported. It does not execute scripts or publish assets.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string","minLength":1,"maxLength":64}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","packageHash","resources"],"properties":{"name":{"type":"string"},"packageHash":{"type":"string"},"resources":{"type":"array","items":{"type":"object"}}}}`),
		Risk:          tool.RiskLow, Permissions: []tool.PermissionRequirement{}, Idempotent: true, Version: "1",
	}, nil
}

func (t *ListSkillResources) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.skills == nil {
		return tool.Result{}, fmt.Errorf("Skill resource loader is not configured")
	}
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	value, err := t.skills.ListResources(ctx, invocation.RunID, args.Name)
	if err != nil {
		return tool.Result{}, err
	}
	structured, err := json.Marshal(value)
	if err != nil {
		return tool.Result{}, err
	}
	lines := []string{fmt.Sprintf("## Skill resources: %s", value.Name)}
	if len(value.Resources) == 0 {
		lines = append(lines, "No package resources are present.")
	}
	for _, item := range value.Resources {
		access := "binary/metadata only"
		if item.Text {
			access = "readable text"
		}
		lines = append(lines, fmt.Sprintf("- `%s` [%s, %d bytes, %s]", item.Path, item.Kind, item.Size, access))
	}
	return tool.Result{Status: tool.ResultSuccess, Text: strings.Join(lines, "\n"), Structured: structured}, nil
}

func (*ReadSkillResource) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: ReadSkillResourceName,
		Description:   "Read a UTF-8 reference, asset metadata, or script source from a Skill already loaded in this Run. Paths are package-relative and cannot access an unloaded Skill or arbitrary host files. Scripts are returned only as text and are never executed by this tool.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","path"],"properties":{"name":{"type":"string","minLength":1,"maxLength":64},"path":{"type":"string","minLength":1,"maxLength":4096},"offset":{"type":"integer","minimum":0,"maximum":2097152},"maxBytes":{"type":"integer","minimum":1,"maximum":262144}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","path","bytesRead","originalBytes","truncated"],"properties":{"name":{"type":"string"},"path":{"type":"string"},"bytesRead":{"type":"integer","minimum":0},"originalBytes":{"type":"integer","minimum":0},"truncated":{"type":"boolean"}}}`),
		Risk:          tool.RiskLow,
		Permissions:   []tool.PermissionRequirement{},
		Idempotent:    true,
		Version:       "1",
	}, nil
}

func (t *ReadSkillResource) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.skills == nil {
		return tool.Result{}, fmt.Errorf("Skill resource loader is not configured")
	}
	var args struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Offset   int    `json:"offset"`
		MaxBytes int    `json:"maxBytes"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	value, err := t.skills.ReadResource(ctx, invocation.RunID, args.Name, args.Path, args.Offset, args.MaxBytes)
	if err != nil {
		return tool.Result{}, err
	}
	payload := struct {
		Name         string `json:"name"`
		Path         string `json:"path"`
		BytesRead    int    `json:"bytesRead"`
		OriginalSize int64  `json:"originalBytes"`
		Truncated    bool   `json:"truncated"`
	}{Name: args.Name, Path: value.Path, BytesRead: value.BytesRead, OriginalSize: value.OriginalBytes, Truncated: value.Truncated}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: value.Content, Structured: structured, Truncated: value.Truncated, Meta: tool.ResultMeta{OriginalBytes: value.OriginalBytes}}, nil
}

func (*MaterializeSkillResource) Definition(context.Context) (tool.Definition, error) {
	return tool.Definition{
		QualifiedName: MaterializeSkillResourceName,
		Description:   "Copy one immutable resource from a Skill already loaded in this Run into a new file in the current project Workspace. The destination and its parent must already be safe Workspace paths, and existing files are never replaced. This only materializes bytes; scripts still require a separate approved Shell or Python tool call to execute.",
		InputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","path","destination"],"properties":{"name":{"type":"string","minLength":1,"maxLength":64},"path":{"type":"string","minLength":1,"maxLength":4096},"destination":{"type":"string","minLength":1,"maxLength":4096}}}`),
		OutputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","sourcePath","destination","packageHash","sha256","size"],"properties":{"name":{"type":"string"},"sourcePath":{"type":"string"},"destination":{"type":"string"},"packageHash":{"type":"string","pattern":"^[0-9a-f]{64}$"},"sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"size":{"type":"integer","minimum":0}}}`),
		Risk:          tool.RiskModerate,
		Permissions:   []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceWrite, Resource: "."}},
		Idempotent:    false,
		Version:       "1",
	}, nil
}

func (t *MaterializeSkillResource) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if t == nil || t.skills == nil || t.projects == nil {
		return tool.Result{}, fmt.Errorf("Skill resource materialization is not configured")
	}
	var args struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Destination string `json:"destination"`
	}
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	selected, err := t.projects.Get(ctx, strings.TrimSpace(invocation.ProjectID))
	if err != nil {
		return tool.Result{}, err
	}
	if err := project.VerifyPrivateDataLayout(selected); err != nil {
		return tool.Result{}, err
	}
	guard, err := pathguard.Open(selected.WorkspacePath)
	if err != nil {
		return tool.Result{}, err
	}
	defer guard.Close()
	if err := rejectPrivateProjectPath(guard, args.Destination); err != nil {
		return tool.Result{}, err
	}
	clean, err := guard.Relative(args.Destination)
	if err != nil || clean == "." {
		if err == nil {
			err = fmt.Errorf("resource destination must name a new file")
		}
		return tool.Result{}, err
	}
	parent := filepath.Dir(clean)
	directory, _, err := guard.OpenFile(parent)
	if err != nil {
		return tool.Result{}, fmt.Errorf("resource destination parent is unavailable: %w", err)
	}
	info, err := directory.Stat()
	directory.Close()
	if err != nil || !info.IsDir() {
		return tool.Result{}, fmt.Errorf("resource destination parent is not a Workspace directory")
	}
	destination, err := guard.Absolute(clean)
	if err != nil {
		return tool.Result{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return tool.Result{}, fmt.Errorf("resource destination already exists")
	} else if !os.IsNotExist(err) {
		return tool.Result{}, err
	}
	resource, err := t.skills.MaterializeResource(ctx, invocation.RunID, args.Name, args.Path)
	if err != nil {
		return tool.Result{}, err
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return tool.Result{}, err
	}
	temporary := filepath.Join(filepath.Dir(destination), fmt.Sprintf(".%s.sciaide-%x.tmp", filepath.Base(destination), random))
	if err := os.WriteFile(temporary, resource.Contents, 0o600); err != nil {
		return tool.Result{}, err
	}
	defer os.Remove(temporary)
	if err := filepublish.NoReplace(temporary, destination); err != nil {
		return tool.Result{}, fmt.Errorf("publish Skill resource: %w", err)
	}
	payload := struct {
		Name        string `json:"name"`
		SourcePath  string `json:"sourcePath"`
		Destination string `json:"destination"`
		PackageHash string `json:"packageHash"`
		SHA256      string `json:"sha256"`
		Size        int64  `json:"size"`
	}{resource.Name, resource.SourcePath, filepath.ToSlash(clean), resource.PackageHash, resource.SHA256, resource.Size}
	structured, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Status: tool.ResultSuccess, Text: fmt.Sprintf("Materialized Skill resource %s to Workspace path %s (%d bytes, SHA256 %s).", resource.SourcePath, filepath.ToSlash(clean), resource.Size, resource.SHA256), Structured: structured}, nil
}
