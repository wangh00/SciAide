package gateway

import (
	"context"
	"reflect"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model/anthropic"
	"github.com/wangh00/SciAide/internal/model/openai"
	"github.com/wangh00/SciAide/internal/model/responses"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type loader struct{ profile modelprofile.Profile }

func (l loader) Secret(context.Context, string) (modelprofile.Profile, []byte, error) {
	return l.profile, nil, nil
}

func TestResolverRoutesAllSupportedProtocols(t *testing.T) {
	tests := []struct {
		protocol modelcap.APIProtocol
		want     any
	}{
		{modelprofile.ProtocolOpenAIChat, &openai.Client{}},
		{modelprofile.ProtocolOpenAIResponses, &responses.Client{}},
		{modelprofile.ProtocolAnthropic, &anthropic.Client{}},
	}
	for _, test := range tests {
		profile := modelprofile.Profile{Enabled: true, APIProtocol: test.protocol, Models: []modelprofile.ProfileModel{{ID: "model", Enabled: true}}}
		resolved, err := NewResolver(loader{profile: profile}).Resolve(context.Background(), "profile", "model")
		if err != nil {
			t.Fatalf("Resolve(%s): %v", test.protocol, err)
		}
		if resolved.APIProtocol != test.protocol || reflect.TypeOf(resolved.Model) != reflect.TypeOf(test.want) {
			t.Fatalf("Resolve(%s) = %T, %s", test.protocol, resolved.Model, resolved.APIProtocol)
		}
	}
}

func TestResolverReturnsAutomaticModelReasoningCapabilities(t *testing.T) {
	resolver := NewResolver(loader{profile: modelprofile.Profile{Enabled: true, Models: []modelprofile.ProfileModel{{ID: "reasoning", Enabled: true, ReasoningLevels: []modelcap.ReasoningLevel{modelcap.ReasoningHigh, modelcap.ReasoningXHigh}}}}})
	resolved, err := resolver.Resolve(context.Background(), "profile", "reasoning")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.SupportedReasoningLevels) != 5 || resolved.SupportedReasoningLevels[4] != modelcap.ReasoningMax {
		t.Fatalf("reasoning levels = %#v", resolved.SupportedReasoningLevels)
	}
}

func TestResolverFiltersRuntimeRejectedReasoningLevels(t *testing.T) {
	profile := modelprofile.Profile{Enabled: true, Models: []modelprofile.ProfileModel{{ID: "future-model", Enabled: true, ReasoningCapabilitySource: "inferred", ReasoningRejectedLevels: []modelcap.ReasoningLevel{modelcap.ReasoningMax}}}}
	resolved, err := NewResolver(loader{profile: profile}).Resolve(context.Background(), "profile", "future-model")
	if err != nil {
		t.Fatal(err)
	}
	if got := modelcap.ResolveReasoningLevel(modelcap.ReasoningMax, resolved.SupportedReasoningLevels); got != modelcap.ReasoningXHigh {
		t.Fatalf("resolved max after rejection = %q", got)
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
