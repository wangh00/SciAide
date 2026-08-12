package project

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/id"
)

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Create(ctx context.Context, name, description string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("project name is required")
	}
	projectID, err := id.New()
	if err != nil {
		return Project{}, err
	}
	now := s.now()
	value := Project{
		ID:          projectID,
		Name:        name,
		Description: strings.TrimSpace(description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repository.Create(ctx, value); err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	return value, nil
}

func (s *Service) Get(ctx context.Context, projectID string) (Project, error) {
	return s.repository.Get(ctx, projectID)
}

func (s *Service) List(ctx context.Context) ([]Project, error) {
	return s.repository.List(ctx)
}
