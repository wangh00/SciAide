package gateway

import (
	"context"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/model/responses"
)

func TestDebugObserverCannotMutateSharedResolvedClients(t *testing.T) {
	r := NewResolver(loader{profile: modelprofile.Profile{Enabled: true, APIProtocol: modelprofile.ProtocolOpenAIResponses, Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true}}}})
	if err := r.SetDebugErrorObserver(func(responses.DebugProviderError) {}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDebugStreamFaultObserver(func(responses.DebugStreamFault) {}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDebugHTTPResponseObserver(func(responses.DebugHTTPResponse) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), "fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDebugErrorObserver(nil); err == nil {
		t.Fatal("observer mutated after client was shared")
	}
	if err := r.SetDebugStreamFaultObserver(nil); err == nil {
		t.Fatal("stream observer mutated after client was shared")
	}
	if err := r.SetDebugHTTPResponseObserver(nil); err == nil {
		t.Fatal("HTTP observer mutated after client was shared")
	}
}
