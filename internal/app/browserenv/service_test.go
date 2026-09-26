package browserenv

import (
	"context"
	"github.com/wangh00/SciAide/internal/network"
	"testing"
	"time"
)

func TestCookieBindingAndCancellation(t *testing.T) {
	p := network.Proxy{Mode: "custom", URL: "http://127.0.0.1:7890"}
	if Fingerprint(p, 1) == Fingerprint(p, 2) || Fingerprint(p, 1) == Fingerprint(network.Proxy{Mode: "direct"}, 1) {
		t.Fatal("binding collision")
	}
	for _, c := range []Clearance{{Solved: true, UA: "test"}, {EgressStable: true, UA: "test"}, {EgressStable: true, Solved: true}} {
		if _, _, e := c.Client(context.Background()); e == nil {
			t.Fatal("incomplete clearance accepted")
		}
	}
	s := New(nil, nil, nil)
	ctx, done, e := s.acquire(context.Background(), "p")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, err := s.acquire(context.Background(), "p"); err == nil {
		t.Fatal("concurrent operation replaced cancellation handle")
	}
	s.Cancel("p")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel lost")
	}
	done()
	ctx, done, e = s.acquire(context.Background(), "p")
	if e != nil || ctx.Err() != nil {
		t.Fatal("gate not released")
	}
	done()
}
