package gateway

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
)

type loader struct{ profile modelprofile.Profile }

func (l loader) Secret(context.Context, string) (modelprofile.Profile, []byte, error) {
	return l.profile, nil, nil
}

func TestResolverRejectsModelOutsideProfile(t *testing.T) {
	resolver := NewResolver(loader{profile: modelprofile.Profile{Enabled: true, Models: []modelprofile.ProfileModel{{ID: "allowed", Enabled: true}}}})
	if _, err := resolver.Resolve(context.Background(), "profile", "other"); err == nil {
		t.Fatal("Resolve() accepted a model not configured on the profile")
	}
	if _, err := resolver.Resolve(context.Background(), "profile", "allowed"); err != nil {
		t.Fatalf("Resolve() rejected configured model: %v", err)
	}
}
