package gateway

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type loader struct{ profile modelprofile.Profile }

func (l loader) Secret(context.Context, string) (modelprofile.Profile, []byte, error) {
	return l.profile, nil, nil
}

func TestResolverReturnsSelectedModelReasoningCapabilities(t *testing.T) {
	resolver := NewResolver(loader{profile: modelprofile.Profile{Enabled: true, Models: []modelprofile.ProfileModel{{ID: "reasoning", Enabled: true, ReasoningLevels: []modelcap.ReasoningLevel{modelcap.ReasoningHigh, modelcap.ReasoningXHigh}}}}})
	resolved, err := resolver.Resolve(context.Background(), "profile", "reasoning")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.SupportedReasoningLevels) != 2 || resolved.SupportedReasoningLevels[1] != modelcap.ReasoningXHigh {
		t.Fatalf("reasoning levels = %#v", resolved.SupportedReasoningLevels)
	}
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
