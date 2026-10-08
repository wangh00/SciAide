// Package resource defines durable, run-bound actions over host-discovered
// resources. Model input selects an issued action; it never supplies a locator.
package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/tool"
)

const OpenTool = "builtin.resource.open"
const SearchTool = "builtin.resource.search"
const Version = "resource-actions-v1"

// Discovery may be paged; already issued capabilities never expire by paging.
const MaxSessionActions = 8192

// Scope is supplied by the host after resolving the persisted Chat/Workflow
// binding. It is not part of any model-facing JSON schema.
type Scope struct {
	RunID                 string            `json:"runId"`
	ProjectID             string            `json:"projectId"`
	TaskID                string            `json:"taskId"`
	WorkspaceRoot         string            `json:"workspaceRoot"`
	WorkflowStepID        string            `json:"workflowStepId"`
	ToolContracts         map[string]string `json:"toolContracts"`
	SkillNames            []string          `json:"skillNames"`
	SkillDiscovery        bool              `json:"skillDiscovery"`
	SelectedAttachmentIDs []string          `json:"selectedAttachmentIds,omitempty"`
	SelectedOnly          bool              `json:"selectedOnly,omitempty"`
}

func (s Scope) Hash() string {
	s.SkillNames = append([]string(nil), s.SkillNames...)
	sort.Strings(s.SkillNames)
	s.SelectedAttachmentIDs = append([]string(nil), s.SelectedAttachmentIDs...)
	sort.Strings(s.SelectedAttachmentIDs)
	return Hash(s)
}
func Hash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Seed is a sealed action recipe. Arguments, hashes and menu contents stay on
// the host; the model sees only Option. Menus publish their children lazily.
type Seed struct {
	Label       string          `json:"label"`
	Kind        string          `json:"kind"`
	ToolName    string          `json:"toolName,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	ContentHash string          `json:"contentHash,omitempty"`
	PackageHash string          `json:"packageHash,omitempty"`
	Children    []Seed          `json:"children,omitempty"`
	Search      bool            `json:"search,omitempty"`
}

type Action struct {
	RunID     string    `json:"runId"`
	ID        string    `json:"id"`
	ScopeHash string    `json:"scopeHash"`
	Seed      Seed      `json:"seed"`
	Root      bool      `json:"root"`
	CreatedAt time.Time `json:"createdAt"`
}

func NewAction(scope Scope, seed Seed, root bool, now time.Time) Action {
	seed.Label = strings.Join(strings.Fields(seed.Label), " ")
	if label := []rune(seed.Label); len(label) > 240 {
		seed.Label = string(label[:240]) + "…"
	}
	sh := scope.Hash()
	digest := Hash(struct {
		Scope string
		Seed  Seed
	}{sh, seed})
	id := ""
	if len(digest) == 64 {
		id = "res_" + digest[:32]
	}
	return Action{RunID: scope.RunID, ID: id, ScopeHash: sh, Seed: seed, Root: root, CreatedAt: now.UTC()}
}
func (a Action) Validate(scope Scope) error {
	want := NewAction(scope, a.Seed, a.Root, a.CreatedAt)
	if want.ID == "" || a.Seed.Label != want.Seed.Label || a.RunID != scope.RunID || a.ScopeHash != scope.Hash() || a.ID != want.ID {
		return fmt.Errorf("resource action binding or integrity mismatch")
	}
	if a.Seed.ToolName != "" && scope.ToolContracts[a.Seed.ToolName] == "" {
		return fmt.Errorf("resource action is outside frozen tool scope")
	}
	return nil
}

type Option struct {
	ID    string `json:"actionId"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Tool  string `json:"tool"`
}

func (a Action) Option() Option {
	n := OpenTool
	if a.Seed.Search {
		n = SearchTool
	}
	return Option{a.ID, a.Seed.Label, a.Seed.Kind, n}
}

type Session struct {
	Scope     Scope           `json:"scope"`
	ScopeHash string          `json:"scopeHash"`
	Facts     json.RawMessage `json:"facts"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Repository interface {
	GetSession(context.Context, string) (Session, bool, error)
	CreateSession(context.Context, Session, []Action) error
	PutActions(context.Context, Scope, []Action) error
	GetAction(context.Context, string, string) (Action, bool, error)
	ListActions(context.Context, string, int) ([]Action, error)
}

type Request struct {
	RunID                 string
	ProjectID             string
	WorkflowStepID        string
	Definitions           []tool.Definition
	SkillNames            []string
	SkillDiscovery        bool
	SelectedAttachmentIDs []string
	SelectedOnly          bool
}

type View struct {
	Definitions []tool.Definition
	Context     string
	// SkillActions is host-only preload metadata, never model arguments.
	SkillActions map[string]string
	// Invocation validates only the model-facing selection against this exact
	// request's choices. The executor independently resolves the durable action.
	OpenIDs   map[string]bool
	SearchIDs map[string]bool
}

func (v View) Allows(name string, args json.RawMessage) bool {
	var a struct {
		ActionID string `json:"actionId"`
		Query    string `json:"query"`
	}
	if json.Unmarshal(args, &a) != nil {
		return false
	}
	if name == OpenTool {
		return v.OpenIDs[a.ActionID]
	}
	if name == SearchTool {
		return v.SearchIDs[a.ActionID]
	}
	return false
}

// Give the model a specific, safe recovery instruction, never a guessed ID or
// permission expansion. The exact same-run menu remains authoritative.
func (v View) RejectionMessage(name string, args json.RawMessage) string {
	var a struct {
		ActionID string `json:"actionId"`
	}
	if json.Unmarshal(args, &a) == nil {
		if name == SearchTool && v.OpenIDs[a.ActionID] {
			return fmt.Sprintf("资源操作类型不匹配：actionId %s 是核读/打开操作，请调用 builtin.resource.open，仅传 actionId，不传 query。当前未提供该资源的搜索操作；不要重复用此 ID 调用 search。", a.ActionID)
		}
		if name == OpenTool && v.SearchIDs[a.ActionID] {
			return fmt.Sprintf("资源操作类型不匹配：actionId %s 是搜索操作，请调用 builtin.resource.search 并提供 query。", a.ActionID)
		}
	}
	return "当前阶段只接受本轮资源菜单签发的操作；请选择菜单标明的 tool 和 actionId，不填写路径、Skill 名称或附件 ID，不沿用其他运行的 ID。"
}

type Provider interface {
	Prepare(context.Context, Request) (View, error)
}

// WrappedTool names remain registered for deterministic host execution and
// immutable historical contracts, but are not advertised to resource-mode AI.
func WrappedTool(name string) bool {
	switch name {
	case "builtin.workspace.list", "builtin.workspace.read_text", "builtin.attachment.list", "builtin.document.inspect", "builtin.document.read", "builtin.document.search", "builtin.knowledge.read", "builtin.skill.load", "builtin.skill.resource.list", "builtin.skill.resource.read_text":
		return true
	}
	return false
}
