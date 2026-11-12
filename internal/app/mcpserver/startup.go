package mcpserver

import "context"

// Called once by desktop startup, never by List/discovery/Save. A manual
// disconnect lasts for this process; disable autoStart to persist that choice.
func (s *Service) ConnectOnStartup(ctx context.Context) (BatchResult, error) {
	servers, err := s.List(ctx)
	if err != nil {
		return BatchResult{}, err
	}
	ids := []string{}
	for _, server := range servers {
		if server.AutoStart && server.Enabled && server.Trust == TrustUser {
			ids = append(ids, server.ID)
		}
	}
	return s.ConnectBatch(ctx, ids)
}
