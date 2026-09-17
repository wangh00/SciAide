package builtin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/opensciskill"
)

type missingSkillFixture struct{ err error }

func (f missingSkillFixture) Catalog(context.Context, string) (opensciskill.Snapshot, error) {
	return opensciskill.Snapshot{}, nil
}
func (f missingSkillFixture) Browse(context.Context, string, string) ([]opensciskill.Info, error) {
	return nil, nil
}
func (f missingSkillFixture) LoadStructuredForRun(context.Context, string, string, string, string, string, int, int) (opensciskill.Info, opensciskill.Chunk, bool, error) {
	return opensciskill.Info{}, opensciskill.Chunk{}, false, f.err
}

func TestSkillNotFoundIsActionableButInternalErrorsStayPrivate(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		safe bool
	}{
		{"missing", fmt.Errorf("resolve: %w", opensciskill.ErrSkillNotFound), true},
		{"private", errors.New("database not found at private/location"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSkillLoad(missingSkillFixture{err: tc.err}).Invoke(context.Background(), tool.Invocation{Arguments: []byte(`{"name":"plant-biology"}`)})
			var public interface{ UserFacingMessage() string }
			if errors.As(err, &public) != tc.safe {
				t.Fatalf("unexpected disclosure: %v", err)
			}
			if tc.safe {
				s := public.UserFacingMessage()
				if !strings.Contains(s, "plant-biology") || !strings.Contains(s, "category") || !strings.Contains(s, "name") {
					t.Fatalf("missing correction instructions: %s", s)
				}
			}
		})
	}
}
