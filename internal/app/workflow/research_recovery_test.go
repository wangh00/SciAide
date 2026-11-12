package workflow

import "testing"

func TestCancelledAIRecoveryStep(t *testing.T) {
	base := RunDetail{Run: Run{Status: RunCancelled, CurrentStep: 4, Compilation: Compilation{Nodes: []CompiledNode{{ID: "method", Kind: NodeAgentStage}}}}, Steps: []Step{
		{ID: "before", NodeID: "before", Ordinal: 3, NodeKind: NodeAgentStage, Status: StepCompleted},
		{ID: "current", NodeID: "method", Ordinal: 4, NodeKind: NodeAgentStage, Status: StepCancelled},
		{ID: "future", NodeID: "future", Ordinal: 5, NodeKind: NodeAgentStage, Status: StepCancelled},
	}}
	for _, test := range []struct {
		name   string
		change func(*RunDetail)
		want   string
	}{
		{name: "current agent stage", want: "current"},
		{name: "current analysis", want: "current", change: func(d *RunDetail) {
			d.Steps[1].NodeKind = NodeAIAnalysis
			d.Run.Compilation.Nodes[0].Kind = NodeAIAnalysis
		}},
		{name: "not cancelled", change: func(d *RunDetail) { d.Run.Status = RunRunning }},
		{name: "stage still running", change: func(d *RunDetail) { d.Steps[1].Status = StepRunning }},
		{name: "current tool", change: func(d *RunDetail) { d.Steps[1].NodeKind = NodeTool; d.Run.Compilation.Nodes[0].Kind = NodeTool }},
		{name: "current python", change: func(d *RunDetail) { d.Steps[1].NodeKind = NodePython; d.Run.Compilation.Nodes[0].Kind = NodePython }},
		{name: "current confirmation", change: func(d *RunDetail) {
			d.Steps[1].NodeKind = NodeHumanConfirmation
			d.Run.Compilation.Nodes[0].Kind = NodeHumanConfirmation
		}},
		{name: "no current stage", change: func(d *RunDetail) { d.Run.CurrentStep = 8 }},
		{name: "previous completed", change: func(d *RunDetail) { d.Run.CurrentStep = 3 }},
		{name: "node missing", change: func(d *RunDetail) { d.Run.Compilation.Nodes = nil }},
		{name: "node kind mismatch", change: func(d *RunDetail) { d.Run.Compilation.Nodes[0].Kind = NodeTool }},
	} {
		t.Run(test.name, func(t *testing.T) {
			detail := base
			detail.Steps = append([]Step(nil), base.Steps...)
			detail.Run.Compilation.Nodes = append([]CompiledNode(nil), base.Run.Compilation.Nodes...)
			if test.change != nil {
				test.change(&detail)
			}
			step := CancelledAIRecoveryStep(detail)
			if test.want == "" {
				if step != nil {
					t.Fatalf("unexpected recovery: %+v", step)
				}
			} else if step == nil || step.ID != test.want {
				t.Fatalf("recovery = %+v, want %q", step, test.want)
			}
		})
	}
}
