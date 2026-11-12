package tool

import (
	"encoding/json"
	"testing"
)

func TestResearchTaskReadUpgradeIsExactAndPreservesPendingCallScope(t *testing.T) {
	current := Definition{QualifiedName: "builtin.research.task.read", Version: "8", Risk: RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","properties":{"section":{"type":"string","enum":["overview","planning_discussion","source_content"]}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
	prior, ok := ResearchTaskReadVersion7(current)
	if !ok || !ResearchTaskReadUpgradeCompatible(prior, current) {
		t.Fatal("exact v7 contract rejected")
	}
	call := Call{ToolName: prior.QualifiedName, ToolVersion: "7", Risk: prior.Risk, Idempotent: true, IdempotencyKey: "pending", ContractSHA256: DefinitionFingerprint(prior), Arguments: json.RawMessage(`{"section":"overview"}`)}
	if !definitionMatchesSnapshot(current, call) {
		t.Fatal("pending old read cannot finish after upgrade")
	}
	for _, changed := range []func(*Definition){
		func(d *Definition) { d.Version = "6" },
		func(d *Definition) { d.Risk = RiskHigh },
		func(d *Definition) { d.Permissions = []PermissionRequirement{{Kind: PermissionWorkspaceRead}} },
		func(d *Definition) { d.OutputSchema = json.RawMessage(`{"type":"array"}`) },
		func(d *Definition) { d.InputSchema = json.RawMessage(`{"type":"object"}`) },
	} {
		mutated := prior
		changed(&mutated)
		if ResearchTaskReadUpgradeCompatible(mutated, current) {
			t.Fatal("unrelated old contract gained upgrade exemption", mutated)
		}
	}
	call.Arguments = json.RawMessage(`{"section":"planning_discussion"}`)
	if definitionMatchesSnapshot(current, call) {
		t.Fatal("pending old call gained new scope")
	}
	call.Arguments = json.RawMessage(`{"section":"overview"}`)
	call.ContractSHA256 = "wrong"
	if definitionMatchesSnapshot(current, call) {
		t.Fatal("wrong old fingerprint accepted")
	}
}

func TestResearchTaskReadV9DerivesBothLegacyContractsWithoutPointerScope(t *testing.T) {
	current := Definition{QualifiedName: "builtin.research.task.read", Version: "9", Risk: RiskLow, Idempotent: true, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"section":{"type":"string","enum":["overview","planning_discussion","stage_input"]},"pointer":{"type":"string","maxLength":2048}}}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
	for _, version := range []string{"7", "8"} {
		prior, ok := ResearchTaskReadVersion8(current)
		if version == "7" {
			prior, ok = ResearchTaskReadVersion7(current)
		}
		if !ok || !ResearchTaskReadUpgradeCompatible(prior, current) {
			t.Fatal("exact legacy contract not recognized", version)
		}
		call := Call{ToolName: prior.QualifiedName, ToolVersion: version, Risk: prior.Risk, Idempotent: true, IdempotencyKey: "pending", ContractSHA256: DefinitionFingerprint(prior), Arguments: json.RawMessage(`{"section":"stage_input"}`)}
		if !definitionMatchesSnapshot(current, call) {
			t.Fatal("queued legacy read could not finish", version)
		}
		for _, args := range []string{`{"section":"stage_input","pointer":"/x"}`, `{"section":"stage_input","pointer":""}`, `{"section":"stage_input","pointer":null}`} {
			call.Arguments = json.RawMessage(args)
			if definitionMatchesSnapshot(current, call) {
				t.Fatal("queued legacy read gained v9 selector", version, args)
			}
		}
		call.Arguments = json.RawMessage(`{"section":"planning_discussion"}`)
		if definitionMatchesSnapshot(current, call) != (version == "8") {
			t.Fatal("planning scope changed", version)
		}
		mutated := prior
		mutated.OutputSchema = json.RawMessage(`{"type":"array"}`)
		if ResearchTaskReadUpgradeCompatible(mutated, current) {
			t.Fatal("unrelated contract change accepted")
		}
	}
}
