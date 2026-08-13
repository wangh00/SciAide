package project

import (
	"context"
	"time"
)

type Repository interface {
	Create(ctx context.Context, project Project) error
	Get(ctx context.Context, id string) (Project, error)
	List(ctx context.Context) ([]Project, error)
	UpdateWorkspace(ctx context.Context, id, path, kind string, updatedAt time.Time) error
	Delete(ctx context.Context, id string) error
}
