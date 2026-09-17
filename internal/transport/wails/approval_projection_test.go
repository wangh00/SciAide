package wails

import (
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestProjectApprovalCoordinationRedactsTransportFields(t *testing.T) {
	value := permission.Coordination{
		Evaluation: permission.Evaluation{
			Reason:  "authorization: Bearer hidden-token",
			Missing: []tool.PermissionRequirement{{Kind: tool.PermissionNetworkDomain, Resource: "https://example.test/?token=hidden-token"}},
		},
		Approval: &permission.Approval{Resource: "https://example.test/?token=hidden-token", Reason: "Bearer hidden-token"},
		Grant:    &permission.Grant{Resource: "https://example.test/?token=hidden-token"},
		ToolCall: tool.Call{Arguments: []byte(`{"apiKey":"hidden-token","path":"results.csv"}`), Permissions: []tool.PermissionRequirement{{Kind: tool.PermissionWorkspaceRead, Resource: "results.csv"}}},
	}
	projected := projectApprovalCoordination(value)
	encoded := string(projected.ToolCall.Arguments)
	if strings.Contains(encoded, "hidden-token") || strings.Contains(projected.Approval.Resource, "hidden-token") || strings.Contains(projected.Grant.Resource, "hidden-token") || strings.Contains(projected.Evaluation.Reason, "hidden-token") {
		t.Fatalf("transport projection leaked secret: %#v", projected)
	}
	if value.ToolCall.Arguments[0] != '{' || string(value.ToolCall.Arguments) == encoded {
		t.Fatalf("projection mutated or failed to copy tool arguments: original=%s projected=%s", value.ToolCall.Arguments, encoded)
	}
}
