package gateway

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/modelutil"
)

func TestRequestSnapshotConfigurationCannotChangeCachedClients(t *testing.T) {
	profile := modelprofile.Profile{ID: "fixture", Enabled: true, APIProtocol: modelprofile.ProtocolOpenAIResponses, Models: []modelprofile.ProfileModel{{ID: "model", Enabled: true}}}
	resolver := NewResolver(loader{profile: profile})
	observer, err := modelutil.NewRequestSnapshotObserver(func(modelutil.DebugRequestSnapshot) {})
	if err != nil {
		t.Fatal(err)
	}
	if err = resolver.SetDebugRequestSnapshotObserver(observer); err != nil {
		t.Fatal(err)
	}
	first, err := resolver.Resolve(context.Background(), profile.ID, "model")
	if err != nil {
		t.Fatal(err)
	}
	if err = resolver.SetDebugRequestSnapshotObserver(nil); err == nil {
		t.Fatal("changed an already-shared observer")
	}
	second, err := resolver.Resolve(context.Background(), profile.ID, "model")
	if err != nil {
		t.Fatal(err)
	}
	if first.Model != second.Model || resolver.debugRequestSnapshots != observer {
		t.Fatal("cache state or observation mutated")
	}
}
