package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/research/connectors"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
	"github.com/wangh00/SciAide/internal/tools/builtin"
	wails "github.com/wangh00/SciAide/internal/transport/wails"
)

type routingFixtureTransport struct {
	endpoint *url.URL
	calls    atomic.Int32
}

func (t *routingFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.endpoint.Scheme
	u.Host = t.endpoint.Host
	c.URL = &u
	c.Header.Set("X-Fixture-Source", r.URL.Host)
	c.Host = t.endpoint.Host
	response, err := http.DefaultTransport.RoundTrip(c)
	if response != nil {
		response.Request = r
	}
	return response, err
}

// Explicit fixture contract: no retries inside this local rewrite boundary.
func (t *routingFixtureTransport) RoundTripSingleAttempt(r *http.Request) (*http.Response, error) {
	return t.RoundTrip(r)
}

func TestLiteratureProfileRealAdaptersHTTPAndSQLitePreservePlan(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		allFail, explicit, explicitThree bool
	}{{"mixed-source-result", false, false, false}, {"all-upstreams-fail", true, false, false}, {"explicit-ERIC-overrides-profile", false, true, false}, {"explicit-three-does-not-run-five", false, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			allFail := tc.allFail
			ctx := context.Background()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if allFail {
					http.Error(w, "controlled upstream failure", 503)
					return
				}
				switch r.Header.Get("X-Fixture-Source") {
				case "api.ies.ed.gov":
					fmt.Fprint(w, `{"response":{"numFound":1,"docs":[{"id":"EJ1234567","title":"AI writing feedback in higher education","description":"Controlled abstract.","publicationdateyear":2025,"e_fulltextauth":0}]}}`)
				case "api.openalex.org":
					if r.URL.Path != "/works" {
						t.Error("per-record enrichment unexpectedly enabled", r.URL.Path)
						http.Error(w, "not allowed", 400)
						return
					}
					fmt.Fprint(w, `{"meta":{"count":1},"results":[{"id":"https://openalex.org/W123","title":"University writing feedback","publication_year":2025}]}`)
				case "api.crossref.org":
					fmt.Fprint(w, `{"status":"ok","message":{"items":[]}}`)
				case "www.ebi.ac.uk":
					fmt.Fprint(w, `{"hitCount":0,"resultList":{"result":[]}}`)
				case "api.semanticscholar.org":
					http.Error(w, "controlled rate limit", 429)
				default:
					t.Error("profile escaped expected sources", r.Header.Get("X-Fixture-Source"))
					http.Error(w, "unexpected source", 400)
				}
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			transport := &routingFixtureTransport{endpoint: endpoint}
			root := t.TempDir()
			options := Options{RootDir: root, ResearchConnectors: connectors.DefaultWithTransport(transport)}
			app, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if app != nil {
					app.Close()
				}
			}()
			project, err := app.ProjectFacade.CreateProject(struct {
				Name          string `json:"name"`
				Description   string `json:"description"`
				WorkspacePath string `json:"workspacePath"`
			}{Name: "routing controlled fixture"})
			if err != nil {
				t.Fatal(err)
			}
			conv, err := app.ConversationFacade.CreateConversation(wails.CreateConversationRequest{ProjectID: project.ID, Title: "source planning fixture"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			profile := modelprofile.Profile{ID: "unused-fixture-profile", Name: "No model call", ProviderType: modelprofile.ProviderOpenAICompatible, APIProtocol: modelprofile.ProtocolOpenAIChat, BaseURL: "https://example.test/v1", ModelID: "unused", Models: []modelprofile.ProfileModel{{ID: "unused", Enabled: true, IsDefault: true}}, SecretRef: "unused-fixture", TimeoutSeconds: 60, Enabled: true, CreatedAt: now, UpdatedAt: now}
			if err := sqlite.NewModelProfileRepository(app.store.DB()).Save(ctx, profile); err != nil {
				t.Fatal(err)
			}
			run := chat.Run{ID: "routing-run", ConversationID: conv.ID, UserMessageID: "routing-user", AssistantMessageID: "routing-assistant", ModelProfileID: profile.ID, ModelID: "unused", APIProtocol: profile.APIProtocol, PermissionMode: conversation.PermissionPlan, Status: chat.RunRunning, CreatedAt: now, StartedAt: &now, UpdatedAt: now}
			message := func(id string, role conversation.Role) conversation.Message {
				return conversation.Message{ID: id, ConversationID: conv.ID, RunID: run.ID, Role: role, Status: conversation.MessageComplete, Parts: []conversation.MessagePart{{ID: id + "-part", MessageID: id, Type: "text", Text: "controlled source fixture", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
			}
			if err := sqlite.NewRunRepository(app.store.DB()).CreateWithMessages(ctx, run, message(run.UserMessageID, conversation.RoleUser), message(run.AssistantMessageID, conversation.RoleAssistant)); err != nil {
				t.Fatal(err)
			}
			definition, _ := builtin.NewResearchWorkflowSearch(nil).Definition(ctx)
			service := tool.NewService(sqlite.NewToolRepository(app.store.DB()), tool.JSONSchemaValidator{})
			arguments := json.RawMessage(`{"query":"AI writing feedback","queries":["university writing assessment"],"limit":5,"referencesOnly":true,"retrievalProfile":"education","sourceRationale":"Education question","publicationYears":{"from":2022,"to":2026}}`)
			if tc.explicit {
				var obj map[string]any
				json.Unmarshal(arguments, &obj)
				obj["sourceIds"] = []string{"eric"}
				obj["retrievalProfile"] = "biomedical"
				arguments, _ = json.Marshal(obj)
			}
			if tc.explicitThree {
				var obj map[string]any
				_ = json.Unmarshal(arguments, &obj)
				obj["sourceIds"] = []string{"eric", "openalex", "crossref"}
				arguments, _ = json.Marshal(obj)
			}
			call, err := service.Propose(ctx, definition, tool.CreateCommand{RunID: run.ID, ProviderCallID: "routing-call", Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Start(ctx, call.ID); err != nil {
				t.Fatal(err)
			}
			execution, err := app.tools.Execute(ctx, project.ID, call.ID)
			if err != nil || execution.Result.Status != tool.ResultSuccess {
				t.Fatalf("%v %s %s", err, execution.Result.Status, execution.Result.Text)
			}
			var result struct {
				QueryIDs   []string `json:"queryIds"`
				Partial    bool     `json:"partial"`
				SearchPlan struct {
					Allocation research.RetrievalPlan `json:"allocation"`
				} `json:"searchPlan"`
			}
			if err = json.Unmarshal(execution.Result.Structured, &result); err != nil {
				t.Fatal(err)
			}
			p := result.SearchPlan.Allocation
			if len(result.QueryIDs) != 2 || p.Version != research.RetrievalPlanVersion || p.UsedHTTP != int(transport.calls.Load()) || p.UsedHTTP > 14 || result.Partial == (tc.explicit || tc.explicitThree) {
				t.Fatal(result, string(execution.Result.Structured))
			}
			if allFail && (p.BudgetDenied == 0 || p.UsedHTTP != 14) {
				t.Fatal("no shared/slot request budget was enforced", p)
			}
			history, err := sqlite.NewResearchRepository(app.store.DB()).ListQueries(ctx, project.ID, 50)
			if err != nil || len(history) != 2 {
				t.Fatal("history was fragmented into per-source entries", history, err)
			}
			for _, q := range history {
				wantSources := 5
				if tc.explicit {
					wantSources = 1
				}
				if tc.explicitThree {
					wantSources = 3
					ids := append([]string(nil), q.SourceIDs...)
					sort.Strings(ids) // Persisted query membership has canonical set order.
					if strings.Join(ids, ",") != "crossref,eric,openalex" {
						t.Fatal("three-source scope changed in persisted actual query", q.SourceIDs)
					}
				}
				if len(q.SourceIDs) != wantSources || strings.Contains(strings.Join(q.SourceIDs, ","), "core") || (tc.explicit && q.SourceIDs[0] != "eric") {
					t.Fatal(q)
				}
			}
			page, err := app.ResearchFacade.ListCandidates(research.CandidateListCommand{ProjectID: project.ID, QueryID: result.QueryIDs[0], Limit: 20})
			wantCandidates := 2
			if tc.explicit {
				wantCandidates = 1
			}
			if err != nil || (!allFail && len(page.Items) != wantCandidates) {
				t.Fatal(page, err)
			}
			if tc.explicit && (p.SelectionMode != "explicit" || p.UsedHTTP != 2) {
				t.Fatal("explicit source was silently replaced", p)
			}
			if tc.explicitThree && (p.SelectionMode != "explicit" || p.UsedHTTP != 6 || transport.calls.Load() != 6) {
				t.Fatal("two queries escaped their three-source HTTP scope", p)
			}
			before := string(execution.Result.Structured)
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			app = nil
			app, err = New(options)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := tool.NewService(sqlite.NewToolRepository(app.store.DB()), tool.JSONSchemaValidator{}).Get(ctx, call.ID)
			if err != nil || string(saved.Result.Structured) != before {
				t.Fatal("persisted plan changed on reopen", err)
			}
		})
	}
}
