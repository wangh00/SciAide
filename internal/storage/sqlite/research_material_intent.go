package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/research"
)

func (r *ResearchRepository) RecordMaterialIntent(ctx context.Context, p, c, task, sha string, kind research.ImportKind) error {
	if len(sha) != 64 || (kind != research.ImportFullText && kind != research.ImportMetadataAbstract) {
		return fmt.Errorf("invalid research material intent")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE research_candidate_task_imports SET pending_sha256=?,pending_kind=? WHERE project_id=? AND candidate_id=? AND research_task_id=?`, sha, kind, p, c, task)
	return expectOne(result, err, "record task material intent")
}

// Completing an intent uses exact task+candidate+SHA identity and preserves old
// bibliography rows. Missing bytes keep the intent until an explicit retry.
func (r *ResearchRepository) RecoverMaterialIntent(ctx context.Context, p, c, task string) (bool, error) {
	var sha string
	var kind research.ImportKind
	if err := r.db.QueryRowContext(ctx, `SELECT pending_sha256,pending_kind FROM research_candidate_task_imports WHERE project_id=? AND candidate_id=? AND research_task_id=?`, p, c, task).Scan(&sha, &kind); err != nil {
		return false, err
	}
	if sha == "" {
		return false, nil
	}
	if len(sha) != 64 || (kind != research.ImportFullText && kind != research.ImportMetadataAbstract) {
		return false, fmt.Errorf("invalid saved material intent")
	}
	a, found, err := NewAttachmentRepository(r.db).FindByHashInScope(ctx, p, sha, attachment.ScopeTask, task)
	if err != nil || !found {
		return false, err
	}
	if a.SourceKind != attachment.SourceResearchImport || a.Status != attachment.StatusReady || !strings.EqualFold(a.SHA256, sha) {
		return false, fmt.Errorf("pending task material is not ready")
	}
	_, err = r.UpdateCandidateTaskImport(ctx, research.ImportStateCommand{ProjectID: p, CandidateID: c, ResearchTaskID: task, Status: research.ImportImported, Kind: kind, AttachmentID: a.ID, At: time.Now().UTC()})
	return err == nil, err
}
