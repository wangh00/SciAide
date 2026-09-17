package research

import (
	"context"
	"fmt"
)

type QueryPageCommand struct {
	Origin    string `json:"origin,omitempty"`
	ProjectID string `json:"projectId"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
}
type DeleteQueriesCommand struct {
	Origin    string   `json:"origin,omitempty"`
	ProjectID string   `json:"projectId"`
	QueryIDs  []string `json:"queryIds"`
}

type QueryOrigin struct {
	CreatedAt string `json:"createdAt,omitempty"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Count     int    `json:"count"`
}

type DeleteCandidatesCommand struct {
	ProjectID    string   `json:"projectId"`
	QueryID      string   `json:"queryId"`
	CandidateIDs []string `json:"candidateIds"`
}

func (s *DiscoveryService) DeleteQueryCandidates(ctx context.Context, c DeleteCandidatesCommand) error {
	if _, err := s.projects.Get(ctx, c.ProjectID); err != nil {
		return err
	}
	if c.QueryID == "" || len(c.CandidateIDs) < 1 || len(c.CandidateIDs) > 100 {
		return fmt.Errorf("select 1-100 candidates from one query")
	}
	r, ok := s.repository.(interface {
		DeleteQueryCandidates(context.Context, DeleteCandidatesCommand) error
	})
	if !ok {
		return fmt.Errorf("candidate deletion unavailable")
	}
	return r.DeleteQueryCandidates(ctx, c)
}

func (s *DiscoveryService) QueryOrigins(ctx context.Context, projectID string) ([]QueryOrigin, error) {
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return nil, err
	}
	r, ok := s.repository.(interface {
		QueryOrigins(context.Context, string) ([]QueryOrigin, error)
	})
	if !ok {
		return nil, fmt.Errorf("history origins unavailable")
	}
	return r.QueryOrigins(ctx, projectID)
}

type historyRepository interface {
	QueryHistory(context.Context, QueryPageCommand) ([]Query, error)
	DeleteQueryHistory(context.Context, DeleteQueriesCommand) error
}

func (s *DiscoveryService) QueryHistory(ctx context.Context, c QueryPageCommand) ([]Query, error) {
	if _, err := s.projects.Get(ctx, c.ProjectID); err != nil {
		return nil, err
	}
	if c.Offset < 0 || c.Limit < 1 || c.Limit > 100 {
		return nil, fmt.Errorf("invalid history page")
	}
	r, ok := s.repository.(historyRepository)
	if !ok {
		return nil, fmt.Errorf("history repository unavailable")
	}
	return r.QueryHistory(ctx, c)
}
func (s *DiscoveryService) DeleteQueryHistory(ctx context.Context, c DeleteQueriesCommand) error {
	if _, err := s.projects.Get(ctx, c.ProjectID); err != nil {
		return err
	}
	if (len(c.QueryIDs) == 0 && c.Origin == "") || len(c.QueryIDs) > 10000 || (len(c.QueryIDs) > 0 && c.Origin != "") {
		return fmt.Errorf("select query IDs or one history group")
	}
	r, ok := s.repository.(historyRepository)
	if !ok {
		return fmt.Errorf("history repository unavailable")
	}
	return r.DeleteQueryHistory(ctx, c)
}
