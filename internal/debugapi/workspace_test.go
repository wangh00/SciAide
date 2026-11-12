package debugapi

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestDebugBudgetIsDurableConcurrentAndFailClosed(t *testing.T) {
	root := t.TempDir()
	w, e := OpenWorkspace(root, "", true, 3)
	if e != nil {
		t.Fatal(e)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w.Gate(context.Background()) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 3 || w.budget.snapshot().Used != 3 {
		t.Fatal("budget overshot", accepted.Load())
	}
	if _, e = OpenWorkspace(root, "", true, 3); e == nil {
		t.Fatal("live root lock stolen")
	}
	_ = w.Close()
	w, e = OpenWorkspace(root, "", true, 3)
	if e != nil {
		t.Fatal(e)
	}
	if w.Gate(context.Background()) == nil || w.budget.snapshot().Used != 3 {
		t.Fatal("restart refunded budget")
	}
	_ = w.Close()
	for _, corrupt := range []string{`{"used":"invalid"}`, `{}`, `null`, `{"enabled":true,"limit":3}`, `{"enabled":true,"limit":3,"used":-1}`} {
		if e = os.WriteFile(filepath.Join(root, "model-budget.json"), []byte(corrupt), 0600); e != nil {
			t.Fatal(e)
		}
		if w, e = OpenWorkspace(root, "", true, 3); e == nil {
			_ = w.Close()
			t.Fatal("corrupt budget silently reset")
		}
	}
	disabled, e := OpenWorkspace(t.TempDir(), "", false, 3)
	if e != nil {
		t.Fatal(e)
	}
	defer disabled.Close()
	if disabled.Gate(context.Background()) == nil || disabled.budget.snapshot().Used != 0 {
		t.Fatal("disabled gate admitted/charged")
	}
}

func TestDebugWorkspaceCrashLock(t *testing.T) {
	if os.Getenv("SCIAIDE_DEBUG_LOCK_HELPER") == "1" {
		w, e := OpenWorkspace(os.Getenv("SCIAIDE_DEBUG_LOCK_ROOT"), "", true, 4)
		if e != nil {
			os.Exit(8)
		}
		if w.Gate(context.Background()) != nil {
			os.Exit(9)
		}
		os.Exit(7)
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDebugWorkspaceCrashLock$")
	cmd.Env = append(os.Environ(), "SCIAIDE_DEBUG_LOCK_HELPER=1", "SCIAIDE_DEBUG_LOCK_ROOT="+root)
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
		t.Fatal("helper did not reach crash", err)
	}
	w, e := OpenWorkspace(root, "", true, 4)
	if e != nil {
		t.Fatal("OS lock survived process death", e)
	}
	defer w.Close()
	if w.budget.snapshot().Used != 1 {
		t.Fatal("crash refunded call")
	}
}

func TestDebugWorkspaceRejectsForeignDataPolicyChangesAndExternalLinks(t *testing.T) {
	foreign := t.TempDir()
	_ = os.WriteFile(filepath.Join(foreign, "unrelated"), []byte("preserve"), 0600)
	if _, e := OpenWorkspace(foreign, "", false, 1); e == nil {
		t.Fatal("claimed foreign root")
	}
	root := t.TempDir()
	w, e := OpenWorkspace(root, "", false, 1)
	if e != nil {
		t.Fatal(e)
	}
	_ = w.Close()
	if _, e = OpenWorkspace(root, "", false, 1, true); e == nil {
		t.Fatal("silently changed permission policy")
	}
	outside := t.TempDir()
	link := filepath.Join(root, "data")
	if e = os.Symlink(outside, link); e != nil {
		if runtime.GOOS != "windows" || exec.Command("cmd", "/c", "mklink", "/J", link, outside).Run() != nil {
			t.Skipf("symlink/junction unavailable; explicit skip: %v", e)
		}
	}
	defer os.Remove(link)
	if accepted, e := OpenWorkspace(root, "", false, 1); e == nil {
		_ = accepted.Close()
		t.Fatal("external data symlink accepted")
	}
}

func TestDebugModelSourceIsReadOnlyDynamicAndNeverCopiesSensitiveSettings(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	_ = os.MkdirAll(filepath.Join(source, "data"), 0700)
	db, e := sqlite.Open(ctx, filepath.Join(source, "data", "sciaide.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	repo := sqlite.NewModelProfileRepository(db.DB())
	p := modelprofile.Profile{ID: "source-profile", ProviderType: modelprofile.ProviderOpenAICompatible, Name: "fixture", TimeoutSeconds: 10, Enabled: true, APIProtocol: modelprofile.ProtocolOpenAIChat, BaseURL: "https://fixture.invalid/v1", ModelID: "fixture", Models: []modelprofile.ProfileModel{{ID: "fixture", Enabled: true, IsDefault: true}}, CustomHeaders: map[string]string{"X-Tenant": "PRIVATE_HEADER_SENTINEL"}, SecretRef: "PRIVATE_REF_SENTINEL"}
	if e = repo.Save(ctx, p); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	w, e := OpenWorkspace(root, source, false, 3)
	if e != nil {
		t.Fatal(e)
	}
	defer w.Close()
	_ = os.MkdirAll(filepath.Join(root, "data"), 0700)
	local, e := sqlite.Open(ctx, filepath.Join(root, "data", "sciaide.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer local.Close()
	localRepo := sqlite.NewModelProfileRepository(local.DB())
	overlay := w.ProfileOverlay(localRepo)
	v, e := overlay.Get(ctx, p.ID)
	if e != nil || v.BaseURL != p.BaseURL || v.CustomHeaders["X-Tenant"] != "PRIVATE_HEADER_SENTINEL" {
		t.Fatal("source no longer available in memory", e)
	}
	stub, e := localRepo.Get(ctx, p.ID)
	encoded, _ := json.Marshal(stub)
	if e != nil || !strings.HasPrefix(stub.SecretRef, "research-debug-unused:") || strings.Contains(stub.SecretRef, "PRIVATE_") || strings.Contains(string(encoded), "PRIVATE_") || stub.BaseURL == p.BaseURL {
		t.Fatal("sensitive settings copied", e)
	}
	p.Enabled = false
	if e = repo.Save(ctx, p); e != nil {
		t.Fatal(e)
	}
	v, e = overlay.Get(ctx, p.ID)
	if e != nil || v.Enabled {
		t.Fatal("stale enabled source", e)
	}
	if overlay.Save(ctx, p) == nil || overlay.Delete(ctx, p.ID) == nil {
		t.Fatal("source writable through overlay")
	}
	p.BaseURL = "https://fixture.invalid/v1?key=PRIVATE_QUERY_SENTINEL"
	_ = repo.Save(ctx, p)
	if _, e = overlay.Get(ctx, p.ID); e == nil || strings.Contains(e.Error(), "PRIVATE_QUERY_SENTINEL") {
		t.Fatal("unsafe source URL accepted/leaked", e)
	}
	safe := p
	safe.ID, safe.Enabled, safe.BaseURL = "safe-profile", true, "https://safe.invalid/v1"
	safe.SecretRef = "PRIVATE_OTHER_REF_SENTINEL"
	if e = repo.Save(ctx, safe); e != nil {
		t.Fatal(e)
	}
	listed, e := overlay.List(ctx)
	if e != nil || len(listed) != 1 || listed[0].ID != safe.ID {
		t.Fatal("unrelated unsafe source blocked a safe model", e)
	}
	_ = w.Close()
	if _, e = OpenWorkspace(root, t.TempDir(), false, 3); e == nil {
		t.Fatal("source identity changed on restart")
	}
}

func TestDebugWorkspaceExplicitFullAccessUpgradeIsAudited(t *testing.T) {
	root := t.TempDir()
	w, err := OpenWorkspace(root, "", false, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if _, err = OpenWorkspaceWithPolicy(root, "", false, 2, true, false); err == nil {
		t.Fatal("permission policy changed without explicit upgrade")
	}
	if _, err = OpenWorkspaceWithPolicy(root, "", false, 2, false, true); err == nil {
		t.Fatal("full-access upgrade accepted without full-access opt-in")
	}
	w, err = OpenWorkspaceWithPolicy(root, "", false, 2, true, true)
	if err != nil {
		t.Fatal("explicit full-access upgrade failed", err)
	}
	if !w.AllowFullAccess {
		t.Fatal("workspace did not retain upgraded policy")
	}
	_ = w.Close()
	owner, err := os.ReadFile(filepath.Join(root, ownerMarker))
	if err != nil || !strings.Contains(string(owner), `"fullAccess":true`) {
		t.Fatal("owner marker did not record full access", err, string(owner))
	}
	audit, err := os.ReadFile(filepath.Join(root, policyUpgradeAudit))
	if err != nil || !strings.Contains(string(audit), `"status":"requested"`) || !strings.Contains(string(audit), `"status":"committed"`) {
		t.Fatal("policy upgrade audit is incomplete", err, string(audit))
	}
	if reopened, err := OpenWorkspace(root, "", false, 2, true); err != nil {
		t.Fatal("upgraded root could not be reopened with the same policy", err)
	} else {
		_ = reopened.Close()
	}
}

func TestDebugWorkspaceRejectsCrossPathHardLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	w, e := OpenWorkspace(root, "", false, 1)
	if e != nil {
		t.Fatal(e)
	}
	_ = w.Close()
	original := filepath.Join(outside, "other.db")
	if e = os.WriteFile(original, []byte("not an app db; preserve exactly"), 0600); e != nil {
		t.Fatal(e)
	}
	_ = os.MkdirAll(filepath.Join(root, "data"), 0700)
	link := filepath.Join(root, "data", "sciaide.db")
	if e = os.Link(original, link); e != nil {
		t.Skipf("filesystem hard links unavailable: %v", e)
	}
	if accepted, e := OpenWorkspace(root, "", false, 1); e == nil {
		_ = accepted.Close()
		t.Fatal("cross-relative-path file alias accepted")
	}
	content, e := os.ReadFile(original)
	if e != nil || string(content) != "not an app db; preserve exactly" {
		t.Fatal("source was changed")
	}
}

func TestDebugConcurrentRootInitializationCannotReplaceOwnerPolicy(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		root := filepath.Join(t.TempDir(), "owned")
		start := make(chan struct{})
		results := make(chan *Workspace, 2)
		for _, full := range []bool{true, false} {
			go func(full bool) {
				<-start
				w, _ := OpenWorkspace(root, "", false, 1, full)
				results <- w
			}(full)
		}
		close(start)
		var winner *Workspace
		for i := 0; i < 2; i++ {
			if w := <-results; w != nil {
				if winner != nil {
					_ = w.Close()
					_ = winner.Close()
					t.Fatal("two policies owned the same root")
				}
				winner = w
			}
		}
		if winner == nil {
			t.Fatal("neither initializer owned the new root")
		}
		full := winner.AllowFullAccess
		_ = winner.Close()
		if rejected, e := OpenWorkspace(root, "", false, 1, !full); e == nil {
			_ = rejected.Close()
			t.Fatal("failed initializer replaced the successful owner's policy")
		}
		owned, e := OpenWorkspace(root, "", false, 1, full)
		if e != nil {
			t.Fatal("successful policy was damaged by concurrent initialization", e)
		}
		_ = owned.Close()
	}
}
