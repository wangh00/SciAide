package responses

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
	"github.com/wangh00/SciAide/internal/model"
)

func TestStalledResponsesDescribeOutputItemsWithoutAcceptingJSON(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg\",\"role\":\"assistant\",\"content\":[]}}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"{\\\"summary\\\":\\\"ready\\\"}\"}\n\n")
				if completed {
					fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"{\\\"summary\\\":\\\"ready\\\"}\"}]}}\n\n")
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			s, err := New(modelprofile.Profile{BaseURL: server.URL, ModelID: "fixture", TimeoutSeconds: 5}, nil).Stream(context.Background(), model.ChatRequest{StreamInactivityTimeout: 50 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var text string
			for {
				e, eErr := s.Recv()
				if e.Type == model.EventDone {
					t.Fatal("accepted unterminated JSON")
				}
				text += e.Text
				if eErr != nil {
					err = eErr
					break
				}
			}
			var public *apperr.Error
			if !errors.As(err, &public) || public.Code != "MODEL_STREAM_STALLED" || !strings.Contains(public.Details, "terminal_received=false") || text != `{"summary":"ready"}` {
				t.Fatal(err, text)
			}
			want := "completed_items=0"
			if completed {
				want = "completed_items=1"
			}
			if !strings.Contains(public.Details, want) || !strings.Contains(public.Details, "seen_items=1") {
				t.Fatal(public.Details)
			}
		})
	}
}
