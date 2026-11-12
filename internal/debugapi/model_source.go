package debugapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
)

type readOnlyProfiles struct{ local, source modelprofile.Repository }

func safeSourceURL(profile modelprofile.Profile) bool {
	u, err := url.Parse(profile.BaseURL)
	return err == nil && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func (p *readOnlyProfiles) stub(ctx context.Context, profile modelprofile.Profile) error {
	if !safeSourceURL(profile) {
		return fmt.Errorf("debug model source URL must not contain userinfo, query or fragment")
	}
	stub := profile
	stub.BaseURL = "https://research-debug.invalid/v1"
	stub.CustomHeaders = map[string]string{}
	// The schema requires globally unique non-null refs. An inert per-profile
	// placeholder is not a copied source credential reference and is never
	// used by this overlay to resolve the real model's secret.
	stub.SecretRef = fmt.Sprintf("research-debug-unused:%x", sha256.Sum256([]byte(profile.ID)))
	stub.SecretConfigured = false
	stub.SecretMasked = ""
	return p.local.Save(ctx, stub)
}
func (p *readOnlyProfiles) Get(ctx context.Context, id string) (modelprofile.Profile, error) {
	v, e := p.source.Get(ctx, id)
	if e == nil {
		e = p.stub(ctx, v)
	}
	return v, e
}
func (p *readOnlyProfiles) List(ctx context.Context) ([]modelprofile.Profile, error) {
	values, e := p.source.List(ctx)
	if e != nil {
		return nil, e
	}
	usable := make([]modelprofile.Profile, 0, len(values))
	for _, v := range values {
		// An unrelated legacy/disabled URL must neither leak embedded values
		// nor block a separate safe model. Get still rejects that exact profile.
		if !safeSourceURL(v) {
			continue
		}
		if e = p.stub(ctx, v); e != nil {
			return nil, e
		}
		usable = append(usable, v)
	}
	return usable, nil
}
func (p *readOnlyProfiles) Save(context.Context, modelprofile.Profile) error {
	return fmt.Errorf("debug model source is read-only")
}
func (p *readOnlyProfiles) Delete(context.Context, string) error {
	return fmt.Errorf("debug model source is read-only")
}
