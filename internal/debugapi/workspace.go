package debugapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/platform/appdirs"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

const ownerMarker = "research-debug-owner.json"
const policyUpgradeAudit = "research-debug-policy-upgrades.jsonl"

type debugOwner struct {
	Kind         string `json:"kind"`
	Version      int    `json:"version"`
	SourceSHA256 string `json:"sourceSha256"`
	FullAccess   bool   `json:"fullAccess"`
}

type policyUpgradeEvent struct {
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	SourceSHA256 string `json:"sourceSha256"`
	FromFull     bool   `json:"fromFullAccess"`
	ToFull       bool   `json:"toFullAccess"`
	At           string `json:"at"`
}

type pendingPolicyUpgrade struct {
	saved debugOwner
	want  debugOwner
}

type Workspace struct {
	Root            string
	AllowFullAccess bool
	budget          *requestBudget
	lock            *sql.DB
	sourceDB        *sql.DB
	source          modelprofile.Repository
	once            sync.Once
}

// OpenWorkspace keeps the original API for callers that do not need to change
// the permission policy of an already-owned root. A policy change must use
// OpenWorkspaceWithPolicy and opt into the explicit upgrade path.
func OpenWorkspace(root, source string, allowModels bool, maxRequests int, fullAccess ...bool) (*Workspace, error) {
	full := len(fullAccess) > 0 && fullAccess[0]
	return openWorkspace(root, source, allowModels, maxRequests, full, false)
}

// OpenWorkspaceWithPolicy allows one explicit, auditable upgrade from a
// plan-owned debug root to full access. Downgrades, source changes and policy
// changes without UpgradeFullAccess remain rejected.
func OpenWorkspaceWithPolicy(root, source string, allowModels bool, maxRequests int, fullAccess, upgradeFullAccess bool) (*Workspace, error) {
	if upgradeFullAccess && !fullAccess {
		return nil, fmt.Errorf("full-access policy upgrade requires full access")
	}
	return openWorkspace(root, source, allowModels, maxRequests, fullAccess, upgradeFullAccess)
}

func openWorkspace(root, source string, allowModels bool, maxRequests int, full, upgrade bool) (*Workspace, error) {
	root, pending, err := prepareRoot(root, source, full, upgrade)
	if err != nil {
		return nil, err
	}
	// OS-held SQLite writer lock: process death releases it; never delete a
	// lock file to steal a session. The dedicated lock DB has no app data.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock, err := sqlite.OpenExisting(ctx, filepath.Join(root, "research-debug-lock.db"), false)
	if err == nil {
		_, err = lock.ExecContext(ctx, "PRAGMA busy_timeout=100")
	}
	if err == nil {
		_, err = lock.ExecContext(ctx, "BEGIN IMMEDIATE")
	}
	if err != nil {
		if lock != nil {
			_ = lock.Close()
		}
		return nil, fmt.Errorf("debug root is locked by another process: %w", err)
	}
	if pending != nil {
		if err := upgradeOwnerPolicy(root, pending.saved, pending.want); err != nil {
			_, _ = lock.Exec("ROLLBACK")
			_ = lock.Close()
			return nil, err
		}
	}
	w := &Workspace{Root: root, AllowFullAccess: full, lock: lock}
	w.budget, err = openBudget(root, allowModels, maxRequests)
	if err == nil && source != "" {
		w.sourceDB, err = sqlite.OpenExisting(context.Background(), filepath.Join(source, "data", "sciaide.db"), true)
		if err == nil {
			w.source = sqlite.NewModelProfileRepository(w.sourceDB)
		}
	}
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

func (w *Workspace) Gate(ctx context.Context) error { return w.budget.take(ctx) }

// Recovery lookup is fixed, read-only and scoped to this root/project/chat.
// UI message projections are capped and cannot prove an old initial Run is
// absent. Never infer absence from the last 200 visible messages.
func (w *Workspace) initialChatRun(ctx context.Context, c Case) (string, error) {
	db, err := sqlite.OpenExisting(ctx, filepath.Join(w.Root, "data", "sciaide.db"), true)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var runID string
	err = db.QueryRowContext(ctx, `SELECT r.id FROM runs r JOIN conversations v ON v.id=r.conversation_id
		WHERE v.project_id=? AND r.conversation_id=? AND r.user_message_id=?`, c.ProjectID, c.ConversationID, c.InitialClientMessageID).Scan(&runID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return runID, err
}

// Source settings stay read-only and in memory; only a redacted FK stub is
// stored locally, never the URL, custom headers or credential references.
func (w *Workspace) ProfileOverlay(local modelprofile.Repository) modelprofile.Repository {
	if w.source == nil {
		return local
	}
	return &readOnlyProfiles{local: local, source: w.source}
}
func (w *Workspace) Close() error {
	w.once.Do(func() {
		if w.budget != nil {
			w.budget.mu.Lock()
			w.budget.state.Enabled = false
			w.budget.mu.Unlock()
		}
		if w.sourceDB != nil {
			_ = w.sourceDB.Close()
		}
		if w.lock != nil {
			_, _ = w.lock.Exec("ROLLBACK")
			_ = w.lock.Close()
		}
	})
	return nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	base := abs
	tail := []string{}
	for {
		_, err = os.Lstat(base)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(base)
		if parent == base {
			return "", err
		}
		tail = append(tail, filepath.Base(base))
		base = parent
	}
	base, err = resolveExistingPath(base)
	if err != nil {
		return "", err
	}
	for i := len(tail) - 1; i >= 0; i-- {
		base = filepath.Join(base, tail[i])
	}
	return filepath.Clean(base), nil
}
func within(a, b string) bool {
	if runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	r, e := filepath.Rel(a, b)
	return e == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}
func overlaps(a, b string) bool { return within(a, b) || within(b, a) }

func prepareRoot(root, source string, full, upgrade bool) (string, *pendingPolicyUpgrade, error) {
	if strings.TrimSpace(root) == "" {
		return "", nil, fmt.Errorf("an explicit isolated --root is required")
	}
	abs, err := canonicalPath(root)
	if err != nil {
		return "", nil, err
	}
	formal, err := appdirs.Resolve("SciAide")
	if err != nil {
		return "", nil, err
	}
	protected, err := canonicalPath(formal.Root)
	if err != nil {
		return "", nil, err
	}
	if overlaps(abs, protected) {
		return "", nil, fmt.Errorf("debug root must not overlap the formal application data")
	}
	sourceID := "none"
	for _, other := range []string{source, os.Getenv("SCIAIDE_HOME")} {
		if other != "" {
			p, e := canonicalPath(other)
			if e != nil {
				return "", nil, e
			}
			if overlaps(abs, p) {
				return "", nil, fmt.Errorf("debug root must not overlap application/source data")
			}
		}
	}
	if source != "" {
		p, e := canonicalPath(source)
		if e != nil {
			return "", nil, e
		}
		sourceID = p
		if runtime.GOOS == "windows" {
			sourceID = strings.ToLower(p)
		}
	}
	want := debugOwner{"sciaide-research-debug", 1, fmt.Sprintf("%x", sha256.Sum256([]byte(sourceID))), full}
	entries, err := os.ReadDir(abs)
	if err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}
	var pending *pendingPolicyUpgrade
	if len(entries) > 0 {
		data, e := os.ReadFile(filepath.Join(abs, ownerMarker))
		var saved debugOwner
		if e != nil || json.Unmarshal(data, &saved) != nil {
			return "", nil, fmt.Errorf("root is not debug-owned with the same source and permission policy")
		}
		needsUpgrade := saved.Kind == want.Kind && saved.Version == want.Version && saved.SourceSHA256 == want.SourceSHA256 && !saved.FullAccess && want.FullAccess && upgrade
		if saved != want && !needsUpgrade {
			return "", nil, fmt.Errorf("root is not debug-owned with the same source and permission policy")
		}
		// Validate descendants before bootstrap can follow a data/cache/DB
		// junction or symlink outside the isolated workspace.
		if err = filepath.WalkDir(abs, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			resolved, e := canonicalPath(path)
			if e != nil {
				return e
			}
			if !within(abs, resolved) {
				return fmt.Errorf("debug root contains an external link; refusing to open it")
			}
			// Hard links do not change the canonical path. Conservatively reject
			// multi-link files without enumerating any protected source data.
			if !entry.IsDir() {
				multiple, e := hasMultipleLinks(path)
				if e != nil {
					return e
				}
				if multiple {
					return fmt.Errorf("debug root contains a multi-link file; refusing to open it")
				}
			}
			return nil
		}); err != nil {
			return "", nil, err
		}
		if needsUpgrade {
			pending = &pendingPolicyUpgrade{saved: saved, want: want}
		}
	} else {
		if err = os.MkdirAll(abs, 0700); err != nil {
			return "", nil, err
		}
		data, _ := json.Marshal(want)
		marker := filepath.Join(abs, ownerMarker)
		f, e := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(e) {
			// Another initializer may have won after ReadDir. Never overwrite
			// its source/permission binding, even if our lock acquisition fails.
			current, readErr := os.ReadFile(marker)
			var saved debugOwner
			if readErr != nil || json.Unmarshal(current, &saved) != nil || saved != want {
				return "", nil, fmt.Errorf("concurrent debug root initialization has a different or incomplete policy")
			}
		} else if e != nil {
			return "", nil, e
		} else {
			_, err = f.Write(data)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return "", nil, err
		}
	}
	return abs, pending, nil
}

func upgradeOwnerPolicy(root string, saved, want debugOwner) error {
	if err := appendPolicyUpgradeEvent(root, policyUpgradeEvent{
		Kind: "sciaide-research-debug-policy-upgrade", Status: "requested", SourceSHA256: want.SourceSHA256,
		FromFull: saved.FullAccess, ToFull: want.FullAccess, At: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("record full-access policy upgrade request: %w", err)
	}
	if err := writeOwnerMarker(root, want); err != nil {
		_ = appendPolicyUpgradeEvent(root, policyUpgradeEvent{
			Kind: "sciaide-research-debug-policy-upgrade", Status: "failed", SourceSHA256: want.SourceSHA256,
			FromFull: saved.FullAccess, ToFull: want.FullAccess, At: time.Now().UTC().Format(time.RFC3339Nano),
		})
		return fmt.Errorf("apply full-access policy upgrade: %w", err)
	}
	if err := appendPolicyUpgradeEvent(root, policyUpgradeEvent{
		Kind: "sciaide-research-debug-policy-upgrade", Status: "committed", SourceSHA256: want.SourceSHA256,
		FromFull: saved.FullAccess, ToFull: want.FullAccess, At: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		// Do not leave an un-audited policy change behind.
		_ = writeOwnerMarker(root, saved)
		return fmt.Errorf("commit full-access policy upgrade audit: %w", err)
	}
	return nil
}

func writeOwnerMarker(root string, owner debugOwner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	return atomicJSON(filepath.Join(root, ownerMarker), data)
}

func appendPolicyUpgradeEvent(root string, event policyUpgradeEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, policyUpgradeAudit), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
