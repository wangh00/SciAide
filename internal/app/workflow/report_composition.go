package workflow

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/wangh00/SciAide/internal/app/reportview"
)

const reportAssemblyVersion = "host-report-assembly-v1"
const reportReviewPartsVersion = reportview.PartsVersion

func usesExplicitReportAssembly(input json.RawMessage) bool {
	var stage struct {
		Assembly string `json:"reportAssembly"`
	}
	return json.Unmarshal(input, &stage) == nil && stage.Assembly == reportAssemblyVersion
}

func assembledReportAppendices(markdown string, input json.RawMessage) ([]string, error) {
	materials := reportDesignMaterialAppendix(input)
	// Material citations participate just as in the legacy enrich-then-assemble
	// path. Retrieval/audit text is not a source of authored citation markers.
	parts := researchHandoffAppendices(markdown, input, materials)
	if materials != "" {
		parts = append(parts, materials)
	}
	retrieval, err := reportRetrievalAppendix(input)
	if err != nil {
		return nil, err
	}
	if retrieval != "" {
		parts = append(parts, retrieval)
	}
	computation, err := reportComputationAppendix(input)
	if err != nil {
		return nil, err
	}
	if computation != "" {
		parts = append(parts, computation)
	}
	if usesExplicitReportAssembly(input) {
		for i, part := range parts {
			parts[i] = managedReportSection(part)
		}
	}
	return parts, nil
}

// Only the EXACT deterministic suffix is outside the authored-body allowance.
// Old inputs keep their existing computation-only projection and limits.
func reportParsingSuffix(input json.RawMessage, markdown string) (string, error) {
	if !usesDesignMaterialAssembly(input) {
		return computationRecordSection(input)
	}
	parts, err := assembledReportAppendices(markdown, input)
	return strings.Join(parts, "\n\n"), err
}

func managedReportSection(part string) string {
	if at := strings.IndexByte(part, '\n'); at >= 0 {
		return part[:at] + "\n\n> 程序管理的记录附录（依据保存记录核对并组装）。" + part[at:]
	}
	return part
}

type reportManagedSection = reportview.ManagedSection
type reportComposition = reportview.Composition

// This is management/navigation, not proof of authorship, scientific evidence
// or a verdict. Derive
// it from the exact current producer input/output, never from headings alone,
// an old review's claims, or model-supplied provenance. Do not duplicate text.
func currentReportComposition(d RunDetail, node CompiledNode, input json.RawMessage) *reportComposition {
	if !isIndependentReviewSchema(node.OutputSchema) {
		return nil
	}
	original, ok := reviewAssembledInput(input)
	if !ok {
		return nil
	}
	fields := decodeObject(original)
	for _, edge := range d.Run.Compilation.Edges {
		if edge.ToNode != node.ID || edge.FromPort != "analysis" {
			continue
		}
		producer := compilationNodeMap(d.Run.Compilation)[edge.FromNode]
		step := findStepByNode(d.Steps, edge.FromNode)
		if !usesReportProvenance(producer) || step == nil || step.Status != StepCompleted || step.InputSHA256 == "" || hashJSON(step.Input) != step.InputSHA256 {
			continue
		}
		output, err := resolveWorkflowEdgeValue(d, edge)
		if err != nil || !rawJSONEqual(output, mustJSON(fields[edge.ToPort])) {
			continue
		}
		markdown, ok := decodeObject(output)["markdown"].(string)
		if !ok {
			continue
		}
		parts, err := assembledReportAppendices(markdown, step.Input)
		suffix := strings.Join(parts, "\n\n")
		text := strings.TrimSpace(markdown)
		if err != nil || len(parts) == 0 || !strings.HasSuffix(text, suffix) {
			continue
		}
		// Include leading whitespace when locating within the original field.
		start := len([]rune(markdown)) - len([]rune(strings.TrimLeftFunc(markdown, unicode.IsSpace)))
		start += len([]rune(text)) - len([]rune(suffix))
		result := &reportComposition{NodeID: step.NodeID, Attempt: step.Attempt, OutputSHA256: hashJSON(step.Output), Pointer: "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(edge.ToPort) + "/markdown"}
		for _, part := range parts {
			end := start + len([]rune(part))
			result.Sections = append(result.Sections, reportManagedSection{Heading: strings.SplitN(part, "\n", 2)[0], Start: start, End: end, SHA256: hashBytes([]byte(part))})
			start = end + 2
		}
		var view struct {
			Composition reportComposition `json:"reportComposition"`
		}
		_ = json.Unmarshal(input, &view)
		result.ViewVersion = view.Composition.ViewVersion
		return result
	}
	return nil
}

func bindReportComposition(d RunDetail, node CompiledNode, input json.RawMessage) json.RawMessage {
	if !usesReportProvenance(node) && !isIndependentReviewSchema(node.OutputSchema) {
		return input
	}
	fields := decodeObject(input)
	if usesReportProvenance(node) {
		fields["reportAssembly"] = reportAssemblyVersion
		// Only a newly COMPILED report opts in. A queued stage in an older
		// frozen graph must not acquire different assembly on restart/retry.
		delete(fields, "designMaterialAssembly")
		if strings.Contains(node.PromptVersion+"-", designMaterialPromptSuffix+"-") {
			fields["designMaterialAssembly"] = designMaterialAssemblyVersion
		}
		// Never accept a copied/model-authored record. Rebind from the current
		// completed producer and exact graph edges before freezing this input.
		if usesProportionateDataDelivery(node) {
			delete(fields, "computationRecord")
			delete(fields, "computationRecordPolicy")
			if record := bindComputationRecord(d, node, input); record != nil {
				fields["computationRecord"] = record
				fields["computationRecordPolicy"] = computationRecordVersion
			}
		}
	} else {
		// Rebinding derives from the complete source, never recursively splits
		// an already separated body or trusts author-supplied headings.
		if original, ok := reviewAssembledInput(input); ok {
			input, fields = original, decodeObject(original)
		}
		delete(fields, "reportComposition")
		delete(fields, "reportRecordAppendices")
		input = mustJSON(fields)
		if composition := currentReportComposition(d, node, input); composition != nil {
			if composition.Pointer == "/context/markdown" {
				context := decodeObject(mustJSON(fields["context"]))
				text := []rune(context["markdown"].(string))
				parts := make([]string, 0, len(composition.Sections))
				for i, section := range composition.Sections {
					end := len(text)
					if i+1 < len(composition.Sections) {
						end = composition.Sections[i+1].Start
					}
					parts = append(parts, string(text[section.Start:end]))
				}
				context["markdown"] = string(text[:composition.Sections[0].Start])
				fields["context"], fields["reportRecordAppendices"] = context, parts
				composition.ViewVersion = reportReviewPartsVersion
			}
			fields["reportComposition"] = composition
		}
	}
	return mustJSON(fields)
}

// Only new review inputs separate authored body and verified host records.
// Concatenation restores EVERY character, including whitespace, for dependency
// comparison. The actual report, frozen older reviews and UI are unchanged.
func reviewAssembledInput(input json.RawMessage) (json.RawMessage, bool) {
	assembled, err := reportview.AssembleInput(input)
	if err != nil {
		return input, false
	}
	return assembled, true
}

const reportCompositionReviewInstruction = `
Report ownership: reportComposition identifies exact host-managed sections, not scientific validity or who first typed them. With viewVersion=report-review-parts-v1, /context/markdown is ONLY the authored body; /reportRecordAppendices/0, /1, etc. contain the separate program-managed records, each retained verbatim. Their concatenation is the complete report; stored offsets refer to that complete report. Cite these actual scalar paths, not composition metadata. Records are expected, not authored duplicates: report rewriting cannot delete them. Check real content/source inconsistencies separately; a defective generator is not repaired by prose. Reassess historical deletion demands against the separated CURRENT content, withdrawing unsupported accusations with their stable IDs. Never automatically approve or suppress scientific defects.
`
