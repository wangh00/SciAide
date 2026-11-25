package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/wangh00/SciAide/internal/app/workflow"
)

// Remap only successful tools' validated identity slots, never scientific text
// or failed/malformed audit arguments. Run before generic result rewriting.
func rewriteWorkingBasisIdentities(ctx context.Context, tx *sql.Tx, maps *archiveIDMaps) error {
	rows, err := tx.QueryContext(ctx, `SELECT tc.id,tc.tool_name,tc.arguments_json,COALESCE(tr.structured_json,''),COALESCE(tr.model_context_text,'') FROM tool_calls tc
 JOIN tool_results tr ON tr.tool_call_id=tc.id WHERE tc.workflow_run_id IS NULL AND tc.status='completed' AND tr.status='success'
 AND tc.tool_name IN ('builtin.research.state.update','builtin.research.discovery.read') ORDER BY tc.rowid`)
	if err != nil {
		return err
	}
	type update struct{ id, args, model string }
	updates := []update{}
	for rows.Next() {
		var id, name, args, result, model string
		if err := rows.Scan(&id, &name, &args, &result, &model); err != nil {
			rows.Close()
			return err
		}
		var payload map[string]json.RawMessage
		if name == workflow.ResearchStateUpdateTool {
			command, err := workflow.ParseResearchWorkingStateUpdate(json.RawMessage(args))
			if err != nil || len(command.Basis) == 0 {
				continue
			}
			for field, ids := range command.Basis {
				for i, old := range ids {
					command.Basis[field][i] = maps.get("tool_calls", old)
				}
			}
			if json.Unmarshal([]byte(args), &payload) != nil {
				continue
			}
			payload["basis"], _ = json.Marshal(command.Basis)
		} else {
			page, err := workflow.ParseResearchWorkingAbstractPage(json.RawMessage(result))
			if err != nil || workflow.ValidateResearchWorkingAbstractSelector(json.RawMessage(args), page) != nil {
				continue
			}
			deliveredID, provided, err := workflow.ParseResearchWorkingAbstractReceipt(json.RawMessage(model))
			if err != nil || maps.get("tool_calls", deliveredID) != id || provided != page {
				continue
			}
			if json.Unmarshal([]byte(args), &payload) != nil {
				continue
			}
			payload["queryId"], _ = json.Marshal(maps.get("research_queries", page.QueryID))
			payload["candidateId"], _ = json.Marshal(maps.get("research_candidates", page.CandidateID))
			var envelope, structured map[string]json.RawMessage
			_ = json.Unmarshal([]byte(model), &envelope)
			_ = json.Unmarshal(envelope["structured"], &structured)
			envelope["toolCallId"], _ = json.Marshal(id)
			structured["queryId"] = payload["queryId"]
			structured["candidateId"] = payload["candidateId"]
			envelope["structured"], _ = json.Marshal(structured)
			encoded, _ := json.Marshal(envelope)
			model = string(encoded)
		}
		encoded, _ := json.Marshal(payload)
		updates = append(updates, update{id: id, args: string(encoded), model: model})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET arguments_json=? WHERE id=?`, item.args, item.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_results SET model_context_text=? WHERE tool_call_id=?`, item.model, item.id); err != nil {
			return err
		}
	}
	return nil
}
