package wails

import (
	"github.com/wangh00/SciAide/internal/app/permission"
	"github.com/wangh00/SciAide/internal/app/tool"
)

// projectApprovalCoordination creates the transport-safe copy returned by the
// approval facade. The permission coordinator works with the authoritative
// domain values; Wails must never accidentally expose a raw command, URL,
// credential-bearing reason or resource from that domain object.
func projectApprovalCoordination(value permission.Coordination) permission.Coordination {
	value.Evaluation.Reason = tool.SafeActivityText(value.Evaluation.Reason, permission.ApprovalProjectionReasonLimit)
	value.Evaluation.Missing = tool.SafeActivityPermissions(value.Evaluation.Missing)
	if value.Approval != nil {
		projected := permission.SafeApproval(*value.Approval)
		value.Approval = &projected
	}
	if value.Grant != nil {
		projected := *value.Grant
		projected.Resource = tool.SafeActivityText(projected.Resource, permission.ApprovalProjectionResourceLimit)
		value.Grant = &projected
	}
	value.ToolCall.Arguments = tool.SafeActivityArguments(value.ToolCall.Arguments)
	value.ToolCall.Permissions = tool.SafeActivityPermissions(value.ToolCall.Permissions)
	value.ToolCall.ErrorMessage = tool.SafeActivityText(value.ToolCall.ErrorMessage, 500)
	return value
}
