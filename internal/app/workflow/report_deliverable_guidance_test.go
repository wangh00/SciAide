package workflow

import (
	"strings"
	"testing"
)

func TestReportDeliveryFormCannotBeWaivedByMethodTemplates(t *testing.T) {
	report := CompiledNode{ID: "report_drafting", PromptVersion: "report-v1"}
	guidance := researchReportScopePrompt(report)
	if !strings.Contains(guidance, reportDeliverableInstruction) || !strings.Contains(guidance, "clearly separated appendices") || !strings.Contains(guidance, "confirmed deliveryScope amendment") {
		t.Fatal("missing user-delivery priority or confirmed scope boundary")
	}
	version := reportExecutionPromptVersion(report, guidance)
	if !strings.Contains(version, reportDeliverableVersion) || report.PromptVersion != "report-v1" || reportExecutionPromptVersion(CompiledNode{PromptVersion: version}, guidance) != version {
		t.Fatal("delivery guidance identity was lost/non-idempotent", version)
	}
	ordinary := CompiledNode{ID: "method_implementation", PromptVersion: "implementation-v1"}
	if researchReportScopePrompt(ordinary) != "" || reportExecutionPromptVersion(ordinary, "") != ordinary.PromptVersion {
		t.Fatal("unrelated frozen implementation identity changed")
	}
}
