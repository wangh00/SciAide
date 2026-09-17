// Package researchtask defines the durable identity and resource boundary of
// a user-facing research task. Workflow runs are execution records; a task
// remains addressable after those records are archived or deleted.
package researchtask

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusArchived  Status = "archived"
)

type OriginKind string

const (
	OriginAI       OriginKind = "ai_route"
	OriginTemplate OriginKind = "template"
)

// Task is the stable user-facing identity shared by all task-owned resources.
type Task struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"projectId"`
	Title             string     `json:"title"`
	ResearchQuestion  string     `json:"researchQuestion"`
	OriginKind        OriginKind `json:"originKind"`
	Status            Status     `json:"status"`
	LatestRunID       string     `json:"latestRunId,omitempty"`
	LatestRunStatus   string     `json:"latestRunStatus,omitempty"`
	AttachmentCount   int        `json:"attachmentCount"`
	KnowledgeCount    int        `json:"knowledgeCount"`
	ArtifactCount     int        `json:"artifactCount"`
	EvidenceCount     int        `json:"evidenceCount"`
	BibliographyCount int        `json:"bibliographyCount"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	ArchivedAt        *time.Time `json:"archivedAt,omitempty"`
}

// ResourceScope is the common presentation contract used by the resource
// manager. It deliberately does not include conversation-local uploads.
type ResourceScope string

const (
	ScopeProjectShared ResourceScope = "project_shared"
	ScopeTask          ResourceScope = "task"
	ScopeLegacy        ResourceScope = "legacy_project"
)

type UpsertCommand struct {
	ID               string
	ProjectID        string
	Title            string
	ResearchQuestion string
	OriginKind       OriginKind
	At               time.Time
}

type Repository interface {
	Upsert(ctx context.Context, command UpsertCommand) error
	Exists(ctx context.Context, projectID, taskID string) (bool, error)
	List(ctx context.Context, projectID string, limit int) ([]Task, error)
	Archive(ctx context.Context, projectID, taskID string, at time.Time) error
}

// Validator is the narrow ownership check shared by task-scoped services.
// A task id is never sufficient by itself: it must belong to the project that
// owns the resource being read or written.
type Validator interface {
	Exists(ctx context.Context, projectID, taskID string) (bool, error)
}

// ArchivedValidator is an optional extension for resource managers that must
// continue managing immutable outputs after a research task is archived.
// Workflow execution keeps using Validator and therefore still requires an
// active task.
type ArchivedValidator interface {
	Validator
	ExistsIncludingArchived(ctx context.Context, projectID, taskID string) (bool, error)
}

func Validate(ctx context.Context, validator Validator, projectID, taskID string) error {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" {
		return fmt.Errorf("research project and task are required")
	}
	if validator == nil {
		return fmt.Errorf("research task ownership validator is not configured")
	}
	found, err := validator.Exists(ctx, projectID, taskID)
	if err != nil {
		return fmt.Errorf("validate research task ownership: %w", err)
	}
	if !found {
		return fmt.Errorf("research task does not belong to the current project")
	}
	return nil
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) List(ctx context.Context, projectID string, limit int) ([]Task, error) {
	return s.repository.List(ctx, projectID, limit)
}

func (s *Service) Exists(ctx context.Context, projectID, taskID string) (bool, error) {
	return s.repository.Exists(ctx, projectID, taskID)
}

// ExistsIncludingArchived verifies project ownership without treating an
// archived task as missing. It is intended for artifact history management.
func (s *Service) ExistsIncludingArchived(ctx context.Context, projectID, taskID string) (bool, error) {
	if s == nil || s.repository == nil {
		return false, fmt.Errorf("research task service is not configured")
	}
	if repository, ok := s.repository.(interface {
		ExistsIncludingArchived(context.Context, string, string) (bool, error)
	}); ok {
		return repository.ExistsIncludingArchived(ctx, projectID, taskID)
	}
	return s.repository.Exists(ctx, projectID, taskID)
}

// Ensure materializes task metadata for a valid, current-project Workflow
// Run. New databases create this row through SQLite triggers, but older
// databases (or rows created before the task migration) may not have the
// bookkeeping row even though the immutable Run still owns the task. Upsert
// preserves the existing task and refuses archived/cross-project identities.
func (s *Service) Ensure(ctx context.Context, command UpsertCommand) error {
	if s == nil || s.repository == nil {
		return fmt.Errorf("research task service is not configured")
	}
	return s.repository.Upsert(ctx, command)
}
