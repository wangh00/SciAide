package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"strings"
	"testing"
)

func TestLiteratureHierarchyScalesPreservesIDsAndReadback(t *testing.T) {
	t.Run("mostly_excluded", func(t *testing.T) { testLiteratureHierarchy(t, false) })
	t.Run("all_retained", func(t *testing.T) { testLiteratureHierarchy(t, true) })
}

func testLiteratureHierarchy(t *testing.T, allRetained bool) {
	s, repo, reader, detail, node := literatureFixture(t, 307)
	for i := range reader.values {
		reader.values[i].Preferred.Abstract = strings.Repeat("Original evidence statement. ", 180)
	}
	seen := map[string]bool{}
	synthesisCount := 0
	readback := false
	coverageCount := 0
	for iteration := 0; iteration < 100; iteration++ {
		input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{"researchContext":{"question":"frozen population"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(input) > 220*1024 {
			t.Fatal("unbounded hierarchy input")
		}
		var in struct {
			Literature literatureInput            `json:"_literature"`
			Candidates []literatureCandidate      `json:"candidates"`
			Records    []literatureEvidenceRecord `json:"evidenceRecords"`
			Children   []literatureSummary        `json:"childSummaries"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			t.Fatal(err)
		}
		if in.Literature.Phase == "selection" {
			prior, fingerprints := literaturePrior(detail, in.Literature.State)
			input, err = prepareLiteratureSynthesis(detail, literatureCandidates(reader.values), prior, fingerprints, map[string]any{"researchContext": map[string]any{"question": "frozen population"}}, in.Literature)
			if err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(input, &in)
		}
		output := decodeObject(screenFixture(input, true))
		if in.Literature.Phase == "batch" {
			if allRetained {
				assessments := []literatureAssessment{}
				for _, c := range in.Candidates {
					decision := "support"
					if c.ID == "44" {
						decision = "core"
					}
					assessments = append(assessments, literatureAssessment{CandidateID: c.ID, Decision: decision, Relevance: "medium", Reason: "Source requires verification", ImportAction: map[string]string{"core": "direct", "support": "verify"}[decision], Purpose: "Verify source relevance"})
				}
				output["candidateAssessments"] = assessments
			}
			for _, c := range in.Candidates {
				if in.Literature.Readback {
					if c.ID != "44" || !strings.HasPrefix(c.Abstract, "Original evidence") {
						t.Fatal("wrong readback")
					}
					readback = true
				} else if in.Literature.Triage {
					if seen[c.ID] {
						t.Fatal("duplicate screening", c.ID)
					}
					seen[c.ID] = true
				}
			}
			// Large but schema-valid evidence notes exercise multi-level packing.
			notes := []literatureNote{}
			for _, c := range in.Candidates {
				n := fixtureLiteratureNote(c)
				n.Finding = strings.Repeat("Evidence. ", 30)
				n.Uncertainty = strings.Repeat("Unknown. ", 30)
				notes = append(notes, n)
			}
			if in.Literature.Triage {
				light := []map[string]any{}
				for _, n := range notes {
					light = append(light, map[string]any{"candidateId": n.CandidateID, "quotes": n.Quotes})
				}
				output["evidenceNotes"] = light
			} else {
				output["evidenceNotes"] = notes
			}
		} else if in.Literature.Phase == "synthesis" {
			synthesisCount++
		} else {
			coverageCount++
			ids := []string{}
			for _, r := range in.Records {
				ids = append(ids, r.CandidateID)
			}
			for _, c := range in.Children {
				ids = appendUnique(ids, c.CandidateIDs...)
			}
			if len(ids) != 307 {
				t.Fatal("hierarchy dropped candidates", len(ids))
			}
			if coverageCount == 1 {
				output["recheckCandidateIds"] = []string{"44"}
				output["rechecks"] = []literatureRecheck{{CandidateID: "44", Question: "Does the supplied abstract support the retained finding?"}}
				output["coverage"].(map[string]any)["sufficientForClaimedScope"] = false
			}
		}
		if in.Literature.Phase == "coverage" {
			output["supplementalQueries"] = []string{}
		}
		encoded := mustJSON(output)
		phaseNode := literaturePhaseNode(node, input)
		if err := (tool.JSONSchemaValidator{}).Validate(phaseNode.OutputSchema, encoded); err != nil {
			t.Fatal(err)
		}
		if err := validateWorkflowAIStageOutput(phaseNode, encoded, input); err != nil {
			t.Fatal(err)
		}
		step := detail.Steps[1]
		step.Input = input
		e := AIExecution{ID: fmt.Sprint(iteration), Status: "completed", Output: encoded}
		before := repo.continuations
		if err := s.completeLiteratureScreening(context.Background(), detail, step, node, e, encoded); err != nil {
			t.Fatal(err)
		}
		detail.AIExecutions = append(detail.AIExecutions, e)
		if repo.continuations == before {
			break
		}
		detail.Events = append(detail.Events, repo.event)
		s = &RuntimeService{repository: repo, literature: reader, now: s.now, newID: s.newID}
	}
	if len(seen) != 307 || allRetained && synthesisCount < 2 || !readback || coverageCount != 2 || len(repo.output) == 0 {
		t.Fatal(len(seen), synthesisCount, readback, coverageCount)
	}
	var final struct {
		Manifest []any `json:"evidenceManifest"`
	}
	json.Unmarshal(repo.output, &final)
	if len(final.Manifest) != 307 {
		t.Fatal("missing final provenance manifest")
	}
	input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{"researchContext":{"question":"frozen population"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var unchanged struct {
		Literature literatureInput `json:"_literature"`
	}
	json.Unmarshal(input, &unchanged)
	if unchanged.Literature.Phase == "synthesis" {
		t.Fatal("unchanged evidence required repeated synthesis")
	}
}

func TestLiteratureRejectsUntraceableNotesAndPhaseMixing(t *testing.T) {
	_, _, _, _, node := literatureFixture(t, 0)
	c := literatureCandidate{ID: "a", Title: "A title", Abstract: "Actual source words"}
	input := mustJSON(map[string]any{"_literature": literatureInput{Phase: "batch"}, "candidates": []literatureCandidate{c}})
	output := decodeObject(screenFixture(input, false))
	if err := validateWorkflowAIStageOutput(node, mustJSON(output), input); err != nil {
		t.Fatal(err)
	}
	minimal := decodeObject(screenFixture(input, false))
	minimal["evidenceNotes"] = []any{map[string]any{"candidateId": c.ID, "quotes": fixtureLiteratureNote(c).Quotes}}
	if err := (tool.JSONSchemaValidator{}).Validate(literaturePhaseNode(node, input).OutputSchema, mustJSON(minimal)); err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowAIStageOutput(node, mustJSON(minimal), input); err != nil {
		t.Fatal("concise exclusion rejected", err)
	}
	minimal["candidateAssessments"] = []literatureAssessment{{CandidateID: c.ID, Decision: "support", Relevance: "medium", Reason: "needs evidence", ImportAction: "verify", Purpose: "Verify source"}}
	if validateWorkflowAIStageOutput(node, mustJSON(minimal), input) == nil {
		t.Fatal("retained candidate without evidence fields accepted")
	}
	n := fixtureLiteratureNote(c)
	n.Quotes[1].Quote = "fabricated quotation"
	output["evidenceNotes"] = []literatureNote{n}
	if validateWorkflowAIStageOutput(node, mustJSON(output), input) == nil {
		t.Fatal("fabricated source quote accepted")
	}
	output = decodeObject(screenFixture(input, false))
	output["coverage"] = map[string]any{}
	if validateWorkflowAIStageOutput(node, mustJSON(output), input) == nil {
		t.Fatal("batch global coverage accepted")
	}
}

func TestLiteratureYearScopeIsGroundedAndConservative(t *testing.T) {
	input := raw(`{"originalRequest":"Find studies published 2020-2025.","researchContext":"2022-2024"}`)
	if err := validateLiteratureScope(raw(`{"publicationYears":{"from":2020,"to":2025},"yearEvidence":"published 2020-2025"}`), input); err != nil {
		t.Fatal(err)
	}
	if validateLiteratureScope(raw(`{"publicationYears":{"from":2022,"to":2024},"yearEvidence":"2022-2024"}`), input) == nil {
		t.Fatal("model-created scope narrowed original request")
	}
	years := research.PublicationYears{From: 2020, To: 2025}
	for _, dates := range [][]int{{2026, 2025}, {2026, 0}, {0}, {2024}} {
		if automaticLiteratureExclusion(literatureCandidate{Years: dates}, years) != "" {
			t.Fatal("ambiguous or matching date removed", dates)
		}
	}
	if automaticLiteratureExclusion(literatureCandidate{Years: []int{2026, 2026}}, years) == "" {
		t.Fatal("known out-of-scope record reached AI")
	}
}

func TestLargeLiteratureHierarchyBuildsMultipleLevels(t *testing.T) {
	s, repo, _, detail, node := literatureFixture(t, 0)
	candidates := []literatureCandidate{}
	prior := map[string]literatureAssessment{}
	fingerprints := map[string]string{}
	for i := 0; i < 900; i++ {
		id := fmt.Sprintf("candidate-%04d-%s", i, strings.Repeat("x", 25))
		c := literatureCandidate{ID: id, Title: strings.Repeat("Long publication title ", 30)}
		candidates = append(candidates, c)
		prior[id] = literatureAssessment{CandidateID: id, Decision: "exclude", Relevance: "low", Reason: strings.Repeat("Out of scope. ", 20)}
		fingerprints[id] = hashJSON(mustJSON(c))
	}
	state := literatureCheckpoint{QueryIDs: []string{"q1"}}
	maxLevel := 0
	for i := 0; i < 100; i++ {
		input, err := prepareLiteratureSynthesis(detail, candidates, prior, fingerprints, map[string]any{}, literatureInput{Total: len(candidates), State: state})
		if err != nil {
			t.Fatal(err)
		}
		var in struct {
			Literature literatureInput            `json:"_literature"`
			Records    []literatureEvidenceRecord `json:"evidenceRecords"`
			Children   []literatureSummary        `json:"childSummaries"`
		}
		json.Unmarshal(input, &in)
		maxLevel = max(maxLevel, in.Literature.Level)
		if in.Literature.Phase == "coverage" {
			all := []string{}
			for _, child := range in.Children {
				all = appendUnique(all, child.CandidateIDs...)
			}
			if len(all) != 900 || maxLevel < 2 {
				t.Fatal("missing IDs or second level", len(all), maxLevel)
			}
			return
		}
		findings := []literatureFinding{}
		for j := 0; j < 12; j++ {
			ids := []string{}
			if len(in.Records) > 0 {
				ids = []string{in.Records[j%len(in.Records)].CandidateID}
			} else {
				for _, child := range []literatureSummary{in.Children[j%len(in.Children)]} {
					for _, f := range child.Findings {
						ids = appendUnique(ids, f.CandidateIDs[0])
					}
					for _, f := range child.Uncertainties {
						ids = appendUnique(ids, f.CandidateIDs[0])
					}
				}
				ids = ids[:min(32, len(ids))]
			}
			findings = append(findings, literatureFinding{Text: strings.Repeat("Detail. ", 65), CandidateIDs: ids})
		}
		output := mustJSON(map[string]any{"phase": "synthesis", "summary": "group evidence", "findings": findings, "uncertainties": findings})
		if err := (tool.JSONSchemaValidator{}).Validate(literaturePhaseNode(node, input).OutputSchema, output); err != nil {
			t.Fatal(err)
		}
		if err := validateWorkflowAIStageOutput(node, output, input); err != nil {
			t.Fatal(err)
		}
		step := detail.Steps[1]
		step.Input = input
		e := AIExecution{ID: fmt.Sprint(i), Status: "completed", Output: output}
		if err := s.completeLiteratureScreening(context.Background(), detail, step, node, e, output); err != nil {
			t.Fatal(err)
		}
		detail.Events = append(detail.Events, repo.event)
		detail.AIExecutions = append(detail.AIExecutions, e)
		state = literatureState(detail)
	}
	t.Fatal("hierarchy did not converge")
}
