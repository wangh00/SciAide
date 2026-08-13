package project

import "time"

type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	WorkspacePath string    `json:"workspacePath"`
	WorkspaceKind string    `json:"workspaceKind"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

const (
	WorkspaceManaged  = "managed"
	WorkspaceExternal = "external"
)

type CreateCommand struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	WorkspacePath string `json:"workspacePath"`
}

type RemoveResult struct {
	ProjectID          string `json:"projectId"`
	WorkspacePath      string `json:"workspacePath"`
	WorkspacePreserved bool   `json:"workspacePreserved"`
	TrashPath          string `json:"trashPath,omitempty"`
}
