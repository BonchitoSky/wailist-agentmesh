package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/agentmesh/backend/internal/models"
)

// Workflow shares: the frozen, sanitised snapshots behind a share link.
//
// Split into its own file rather than added to store.go, which is already
// long, the same way device_tokens.go and the geofence work were kept
// separate.

const workflowShareColumns = `token, workflow_id, user_id, name, description, graph, node_count, edge_count, import_count, expires_at, revoked_at, created_at`

// MaxActiveWorkflowShares bounds how many live links one account can hold at
// once. Each link pins its own copy of a graph, so this is a storage bound as
// much as an abuse one -- and somebody who genuinely needs a
// hundred-and-first link almost certainly wants to revoke some of the first
// hundred.
const MaxActiveWorkflowShares = 100

var (
	// ErrShareQuotaExceeded is returned when MaxActiveWorkflowShares live
	// shares already exist for the user. Caller-fault, so the handler maps it
	// to 409 rather than letting a 500 report it as our problem -- the same
	// shape as ErrVariableQuotaExceeded in store.go.
	ErrShareQuotaExceeded = errors.New("share link limit reached")
	// ErrShareNotFound separates "no such token" from a driver failure, so
	// the handler can 404 without inspecting pgx's error text.
	ErrShareNotFound = errors.New("share not found")
)

// scanWorkflowShareRow scans one row shaped like workflowShareColumns,
// handling the graph JSON unmarshal every caller needs identically -- the
// same split scanWorkflowRow uses for workflows.
func scanWorkflowShareRow(row rowScanner) (models.WorkflowShare, error) {
	var s models.WorkflowShare
	var graphJSON []byte
	if err := row.Scan(
		&s.Token, &s.WorkflowID, &s.UserID, &s.Name, &s.Description, &graphJSON,
		&s.NodeCount, &s.EdgeCount, &s.ImportCount,
		&s.ExpiresAt, &s.RevokedAt, &s.CreatedAt,
	); err != nil {
		return models.WorkflowShare{}, err
	}
	// Unlike unmarshalGraph's silent best-effort on workflows -- where a
	// half-read graph still lets the rest of the row render -- a share whose
	// graph will not parse is useless to both of its callers (serve it,
	// import it), and handing back an empty graph as though it were the
	// shared workflow would be worse than saying so.
	if err := json.Unmarshal(graphJSON, &s.Graph); err != nil {
		return models.WorkflowShare{}, fmt.Errorf("share %s: graph: %w", s.Token, err)
	}
	return s, nil
}

// CreateWorkflowShare writes one snapshot and returns the stored row.
//
// The quota count and the insert share a transaction so two share clicks
// racing each other cannot both read 99 and both insert. The count is of LIVE
// shares only -- revoked and expired rows are kept for the sharer's own
// listing (see migration 000041) and must not consume the allowance, or
// revoking a link would not free one up.
// reuseIfUnchanged asks for an existing live link back instead of a new one
// when the snapshot is identical; the returned bool says whether that is what
// happened, so the handler can answer 200 rather than 201.
//
// The lookup happens INSIDE this transaction, behind an advisory lock on the
// workflow, rather than in a separate query the handler runs first. Opening
// the Share dialog fires one request, but React's development double-invoke
// fires two milliseconds apart -- and both found nothing, so both inserted,
// leaving a workflow with two links to the same graph the first time it was
// ever shared. A check that is not in the same transaction as the insert it
// guards is not a check.
func (s *Store) CreateWorkflowShare(ctx context.Context, share models.WorkflowShare, reuseIfUnchanged bool) (models.WorkflowShare, bool, error) {
	graphJSON, err := json.Marshal(share.Graph)
	if err != nil {
		return models.WorkflowShare{}, false, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return models.WorkflowShare{}, false, err
	}
	defer tx.Rollback(ctx)

	if reuseIfUnchanged {
		// Serialises concurrent shares of the SAME workflow and nothing else.
		// Held to the end of the transaction and released by commit or
		// rollback, so there is nothing to unlock by hand.
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`, share.WorkflowID); err != nil {
			return models.WorkflowShare{}, false, err
		}
		// Reuse only for never-expiring links: a fresh expiry instant would
		// never equal a stored one, so an expiring link always gets its own
		// row rather than being quietly answered with a permanent one.
		//
		// The graph compared here is the already-sanitised one, which is the
		// honest question -- "would a new link hold anything different?" --
		// rather than "has the workflow row been touched", which moves for
		// edits a share never carries. `graph = $5::jsonb` is a semantic
		// compare: Postgres normalises jsonb, so key order does not matter.
		// name and description are compared because they are snapshotted
		// beside the graph, so a rename really does change what is seen.
		if share.ExpiresAt == nil {
			row := tx.QueryRow(ctx, `
				SELECT `+workflowShareColumns+`
				  FROM workflow_shares
				 WHERE workflow_id = $1
				   AND user_id = $2
				   AND revoked_at IS NULL
				   AND expires_at IS NULL
				   AND name = $3
				   AND description = $4
				   AND graph = $5::jsonb
				 ORDER BY created_at DESC
				 LIMIT 1
			`, share.WorkflowID, share.UserID, share.Name, share.Description, string(graphJSON))
			existing, err := scanWorkflowShareRow(row)
			switch {
			case err == nil:
				if err := tx.Commit(ctx); err != nil {
					return models.WorkflowShare{}, false, err
				}
				return existing, true, nil
			case errors.Is(err, pgx.ErrNoRows):
				// Nothing to reuse; fall through and insert.
			default:
				return models.WorkflowShare{}, false, err
			}
		}
	}

	// Counted after the reuse check, so handing back a link somebody already
	// has can never fail for being at the limit.
	var live int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM workflow_shares
		 WHERE user_id = $1
		   AND revoked_at IS NULL
		   AND (expires_at IS NULL OR expires_at > NOW())
	`, share.UserID).Scan(&live); err != nil {
		return models.WorkflowShare{}, false, err
	}
	if live >= MaxActiveWorkflowShares {
		return models.WorkflowShare{}, false, fmt.Errorf("%w: %d links, limit %d", ErrShareQuotaExceeded, live, MaxActiveWorkflowShares)
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO workflow_shares
			(token, workflow_id, user_id, name, description, graph, node_count, edge_count, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)
		RETURNING `+workflowShareColumns,
		share.Token, share.WorkflowID, share.UserID, share.Name, share.Description,
		string(graphJSON), share.NodeCount, share.EdgeCount, share.ExpiresAt,
	)
	out, err := scanWorkflowShareRow(row)
	if err != nil {
		return models.WorkflowShare{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.WorkflowShare{}, false, err
	}
	return out, false, nil
}

// GetWorkflowShare reads one share by token, live or not.
//
// Liveness is deliberately NOT filtered here. The public read and the import
// path both want to 404 a revoked or expired link, but the sharer's own
// listing wants to show it -- so the row comes back and
// models.WorkflowShare.Live decides, in one place, for every caller.
func (s *Store) GetWorkflowShare(ctx context.Context, token string) (models.WorkflowShare, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+workflowShareColumns+` FROM workflow_shares WHERE token = $1`, token)
	share, err := scanWorkflowShareRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.WorkflowShare{}, ErrShareNotFound
		}
		return models.WorkflowShare{}, err
	}
	return share, nil
}

// ListWorkflowShares returns every share a user has made of one workflow,
// newest first, including revoked and expired ones -- the sharer's own
// "Manage links" view is the one place those still matter.
//
// The user_id predicate is not redundant with workflow_id. Ownership of the
// workflow is checked by the handler, but scoping here too means a future
// caller that forgets cannot list somebody else's links.
func (s *Store) ListWorkflowShares(ctx context.Context, workflowID, userID string) ([]models.WorkflowShare, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+workflowShareColumns+`
		  FROM workflow_shares
		 WHERE workflow_id = $1 AND user_id = $2
		 ORDER BY created_at DESC
	`, workflowID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Non-nil empty slice, not nil: this is serialised straight to JSON and
	// a workflow with no links should render as [] rather than null.
	out := []models.WorkflowShare{}
	for rows.Next() {
		share, err := scanWorkflowShareRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, share)
	}
	return out, rows.Err()
}

// RevokeWorkflowShare kills a link, and reports via the bool whether THIS
// call performed a genuine revocation rather than a no-op against a row some
// other call already closed -- the same distinction MarkTendrilLeaseReleased
// draws, and for the same reason: a second click on the same button should
// not be reported as a fresh revocation.
//
// Scoped to user_id, so learning a token is not enough to revoke somebody
// else's link.
func (s *Store) RevokeWorkflowShare(ctx context.Context, token, userID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_shares
		   SET revoked_at = NOW()
		 WHERE token = $1 AND user_id = $2 AND revoked_at IS NULL
	`, token, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RecordWorkflowShareImport bumps the counter the sharer sees.
//
// Best-effort by contract: the import it counts has already committed, and
// failing that import because a statistic did not increment would trade a
// real outcome for a cosmetic one. The caller logs and carries on.
func (s *Store) RecordWorkflowShareImport(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workflow_shares SET import_count = import_count + 1 WHERE token = $1`, token)
	return err
}

// CreateWorkflowWithGraph inserts a workflow that already has its nodes and
// edges, in one statement.
//
// It lives in this file rather than beside createWorkflowRow because import
// is the only thing that needs it, and it exists because import is the one
// path where the usual create-then-update dance is actively wrong: a PUT that
// failed after the POST left the importer holding an empty workflow, and the
// frontend's rollback-on-failure could itself fail and orphan the row. One
// INSERT cannot half-happen.
//
// status is 'draft' and nothing else is set, which is what makes an imported
// workflow land unscheduled, unfenced and undeployed: the schedule, geofence
// and deploy columns are this row's own state, never the sharer's.
func (s *Store) CreateWorkflowWithGraph(ctx context.Context, name, userID string, graph models.WorkflowGraph) (models.Workflow, error) {
	graphJSON, err := json.Marshal(graph)
	if err != nil {
		return models.Workflow{}, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO workflows (id, user_id, name, status, graph, is_system)
		VALUES ($1, $2, $3, 'draft', $4::jsonb, false)
		RETURNING `+workflowColumns,
		uuid.New().String(), userID, name, string(graphJSON),
	)
	return scanWorkflowRow(row)
}

// SweepExpiredWorkflowShares deletes expired rows. Nothing schedules it, and
// nothing should have to: expiry is enforced on read, so an expired link is
// already unreachable. This exists for an operator who wants the storage
// back, and as the thing a future cron would call rather than reinventing.
func (s *Store) SweepExpiredWorkflowShares(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM workflow_shares
		 WHERE expires_at IS NOT NULL AND expires_at < $1
	`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
