package research

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Explicit syntactic diagnostic labels, not a relevance/recall gold standard.
func TestRankedQueryDiagnosticCorpus(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		limitations       []string
	}{
		{"education-natural", `AI feedback university student writing`, `AI feedback university student writing`, nil},
		{"education-boolean", `(ChatGPT OR Grammarly) AND writing`, `ChatGPT Grammarly writing`, []string{"boolean_logic_not_enforced"}},
		{"education-suffix", `"AI feedback"[Title/Abstract] AND writing`, `AI feedback writing`, []string{"boolean_logic_not_enforced", "field_scope_not_enforced", "phrase_matching_not_enforced"}},
		{"education-negation", `writing NOT animal-based`, `writing`, []string{"negation_not_enforced"}},
		{"negative-field-suffix", `writing NOT cancer[Title/Abstract]`, `writing`, []string{"negation_not_enforced", "field_scope_not_enforced"}},
		{"population-negative-group", `insomnia AND NOT (children OR adolescents)`, `insomnia`, []string{"boolean_logic_not_enforced", "negation_not_enforced"}},
		{"double-negation", `NOT NOT writing`, `writing`, []string{"negation_not_enforced"}},
		{"nested-negation", `writing NOT (animals NOT adults)`, `writing adults`, []string{"negation_not_enforced"}},
		{"andnot", `writing ANDNOT "primary school"`, `writing`, []string{"boolean_logic_not_enforced", "negation_not_enforced", "phrase_matching_not_enforced"}},
		{"cpp", `C++ tutoring student feedback`, `C++ tutoring student feedback`, nil},
		{"ratio", `5:2 diet AND adults`, `5:2 diet adults`, []string{"boolean_logic_not_enforced"}},
		{"hyphenated", `time-restricted eating`, `time-restricted eating`, nil},
		{"natural-colon", `writing: AI feedback`, `writing: AI feedback`, nil},
		{"name-apostrophe", `O'Brien feedback O’Brien writing`, `O'Brien feedback O’Brien writing`, nil},
		{"identifier", `10.1234/example.2025`, `10.1234/example.2025`, nil},
		{"lowercase-natural", `feedback and writing not scoring`, `feedback and writing not scoring`, nil},
		{"unicode", `人工智能 反馈 大学生 写作`, `人工智能 反馈 大学生 写作`, nil},
		{"quoted-operator", `"NOT" AND writing`, `NOT writing`, []string{"boolean_logic_not_enforced", "phrase_matching_not_enforced"}},
		{"escaped-quote", `"student \"voice\"" AND feedback`, `student "voice" feedback`, []string{"boolean_logic_not_enforced", "phrase_matching_not_enforced"}},
		{"duplicate-atoms", `(writing OR WRITING) AND feedback`, `writing feedback`, []string{"boolean_logic_not_enforced"}},
		{"field-on-group", `(feedback OR writing)[Title/Abstract]`, `feedback writing`, []string{"boolean_logic_not_enforced", "field_scope_not_enforced"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RankedBibliographicQuery(tc.input)
			if err != nil || got.Query != tc.want || !reflect.DeepEqual(got.Limitations, tc.limitations) {
				t.Fatalf("input=%q got=%+v err=%v; want=%q limitations=%v", tc.input, got, err, tc.want, tc.limitations)
			}
		})
	}
}

func TestRankedQueryRejectsUnsafeProjection(t *testing.T) {
	for _, input := range []string{
		``, `NOT cancer`, `NOT (mice OR rats)`, `"unclosed`, `()`, `writing AND`,
		`writing OR`, `writing NOT`, `writing AND OR feedback`, `(writing`, `writing)`,
		`writing[`, `writing[]`, `writing[Title`, `writing]`, `[Title]writing`,
		`writing[Title][Abstract]`, `writing[Title:~3]`, `writ*`, `feed?ack`,
		`TITLE_ABS:writing`, `title:(writing OR feedback)`, "writing\x00feedback",
		`writing NOT 'cancer treatment'`, `writing ANDNOT 'animal studies'`, `writing NOT ‘cancer treatment’`, `writing NOT “cancer treatment”`,
		`"writing* feedback"`, `"x {y}"`, `TITLE-ABS-KEY(writing NOT animals)`, `TITLE-ABS-KEY:(writing NOT animals)`, `title-field:writing`, `custom_field:(writing NOT animals)`, `custom_field: "writing feedback"`,
		strings.Repeat("(", 34) + "writing" + strings.Repeat(")", 34), strings.Repeat("x", MaxQueryRunes+1),
	} {
		t.Run(input, func(t *testing.T) {
			got, err := RankedBibliographicQuery(input)
			if err == nil || got.Query != "" {
				t.Fatalf("unsafe projection was not rejected: input=%q got=%+v err=%v", input, got, err)
			}
		})
	}
}

func TestSourceQueryAuditMatchesDispatch(t *testing.T) {
	for _, sourceID := range []string{"crossref", "semantic-scholar", "arxiv", "openalex", "pubmed", "europepmc", "eric", "core", "fixture-native"} {
		for _, planned := range []bool{false, true} {
			name := sourceID
			if planned {
				name += "-planned"
			}
			t.Run(name, func(t *testing.T) {
				logical := `("AI feedback" OR Grammarly) AND writing`
				input, sent, origin := logical, "<not-called>", "logical_query"
				projections := map[string]string{}
				if planned {
					input, origin = `writing NOT animal-based`, "planned_provider_query"
					projections[sourceID] = input
				}
				service, err := NewService([]Connector{projectedConnector{fixtureConnector: fixtureConnector{source: testSource(sourceID)}, query: &sent}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := service.Search(context.Background(), SearchCommand{Query: logical, ProviderQueries: projections, SourceIDs: []string{sourceID}})
				if err != nil {
					t.Fatal(err)
				}
				status := result.Sources[0]
				if status.LogicalQuery != logical || status.InputQuery != input || status.QueryOrigin != origin || status.QueryProjectionVersion != sourceQueryProjectionVersion || status.EffectiveQuery != sent || result.Query != logical {
					t.Fatal("dispatch/audit/input diverged", sent, result)
				}
				wantQuery, wantMode := input, "native_query"
				switch sourceID {
				case "crossref", "semantic-scholar":
					wantMode = "ranked_bibliographic"
					want, e := RankedBibliographicQuery(input)
					if e != nil {
						t.Fatal(e)
					}
					wantQuery = want.Query
				case "arxiv":
					wantMode = "arxiv_boolean"
					wantQuery, err = ArXivQuery(input)
					if err != nil {
						t.Fatal(err)
					}
				}
				if status.ProviderQuery != wantQuery || status.QueryMode != wantMode {
					t.Fatal("provider query/mode was guessed from its origin", status)
				}
			})
		}
	}
}

func TestInvalidRankedQueryDoesNotDispatchOrDisableNativeSources(t *testing.T) {
	for _, input := range []string{`NOT cancer`, `writing AND`, `title:writing`, ``, `   `, `writing NOT 'cancer treatment'`, strings.Repeat("x", MaxQueryRunes+1)} {
		t.Run(input, func(t *testing.T) {
			crossref, eric := "<not-called>", "<not-called>"
			service, err := NewService([]Connector{
				projectedConnector{fixtureConnector: fixtureConnector{source: testSource("crossref")}, query: &crossref},
				projectedConnector{fixtureConnector: fixtureConnector{source: testSource("eric")}, query: &eric},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Search(context.Background(), SearchCommand{Query: `writing`, ProviderQueries: map[string]string{"crossref": input}, Years: PublicationYears{From: 2023, To: 2026}})
			if err != nil {
				t.Fatal(err)
			}
			status := result.Sources[0]
			if crossref != "<not-called>" || eric != "writing" || !result.Partial || status.Status != SearchFailed || status.ErrorCode != FailureInvalidQuery || status.Retryable || status.ProviderQuery != "" || status.EffectiveQuery != "" || status.QueryOrigin != "planned_provider_query" || status.YearFilterApplied || result.Sources[1].Status != SearchEmpty {
				t.Fatal("local input error was dispatched or misreported", result, crossref, eric)
			}
		})
	}
}

func TestSourceProjectionKeepsLogicalAssociationForIdenticalPlannedQuery(t *testing.T) {
	for _, logical := range []string{`writing NOT adults`, `writing NOT children`} {
		projection, err := compileSourceQuery("crossref", logical, map[string]string{"crossref": "writing"}, PublicationYears{})
		if err != nil || projection.LogicalQuery != logical || projection.InputQuery != "writing" || projection.ProviderQuery != "writing" {
			t.Fatal(projection, err)
		}
	}
}

func FuzzRankedQueryProjection(f *testing.F) {
	for _, input := range []string{`writing NOT animal-based`, `C++ AND 5:2`, `NOT 'cancer treatment'`, `"AI feedback"[Title/Abstract]`, `NOT (mice OR rats)`, `writing AND`, `人工智能 反馈`} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		got, err := RankedBibliographicQuery(input)
		again, nextErr := RankedBibliographicQuery(input)
		if (err == nil) != (nextErr == nil) || !reflect.DeepEqual(got, again) {
			t.Fatal("non-deterministic projection")
		}
		if err != nil && got.Query != "" {
			t.Fatal("error exposed a fallback query", got)
		}
		if err == nil && strings.TrimSpace(got.Query) == "" {
			t.Fatal("empty successful projection")
		}
	})
}

func TestOldSourceQuerySnapshotIsNotUpgraded(t *testing.T) {
	old := `{"sourceId":"crossref","status":"empty","count":0,"queryMode":"planned_provider_query","effectiveQuery":"old saved query","publicationYears":{},"yearFilterApplied":false,"retryable":false}`
	var status SourceSearch
	if err := json.Unmarshal([]byte(old), &status); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if status.QueryMode != "planned_provider_query" || status.QueryProjectionVersion != "" || strings.Contains(string(b), "inputQuery") || strings.Contains(string(b), "queryLimitations") || strings.Contains(string(b), "queryOrigin") {
		t.Fatal("old snapshot was silently recompiled", string(b))
	}
}
