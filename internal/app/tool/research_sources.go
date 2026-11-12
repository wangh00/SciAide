package tool

import (
	"github.com/wangh00/SciAide/internal/researchsources"
	"net/url"
)

func ResearchSearchNetworkPermissions() []PermissionRequirement {
	result := []PermissionRequirement{}
	seen := map[string]bool{}
	for _, source := range researchsources.Definitions() {
		u, err := url.Parse(source.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			continue
		}
		resource := u.Hostname() + ":443"
		if !seen[resource] {
			result = append(result, PermissionRequirement{Kind: PermissionNetworkDomain, Resource: resource})
			seen[resource] = true
		}
	}
	return result
}
