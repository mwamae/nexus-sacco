// Phase-1 follow-up — hybrid internal/external comments store.
//
// One row per comment. External comments fire an SMS to the member via
// the notifier client (handler-side); inbound member replies via the
// public /m/c/{token} link route through the public reply endpoint and
// land here with author_member_id set.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/domain"
)

type LoanCommentsStore struct {
	pool *pgxpool.Pool
}

func NewLoanCommentsStore(pool *pgxpool.Pool) *LoanCommentsStore {
	return &LoanCommentsStore{pool: pool}
}

var (
	ErrCommentNotFound    = errors.New("loan comment not found")
	ErrCommentForbidden   = errors.New("only the author may edit or delete a comment")
	ErrCommentBadTarget   = errors.New("comment must reference either application_id or loan_id, not both")
	ErrCommentTemplateBad = errors.New("template not found or visibility mismatch")
)

// ─────────── helpers ───────────

const loanCommentCols = `
	id, tenant_id, application_id, loan_id, parent_id,
	visibility, body, attachment_paths,
	author_user_id, author_member_id,
	posted_at, edited_at,
	COALESCE(edit_history::text, '')::bytea,
	pinned, member_read_at, reply_token, is_deleted
`

// Same column list, qualified to the `c` alias for JOIN queries (the
// users + counterparty_directory tables both expose an `id` column;
// without the prefix Postgres errors with ambiguous reference 42702).
const loanCommentColsAliased = `
	c.id, c.tenant_id, c.application_id, c.loan_id, c.parent_id,
	c.visibility, c.body, c.attachment_paths,
	c.author_user_id, c.author_member_id,
	c.posted_at, c.edited_at,
	COALESCE(c.edit_history::text, '')::bytea,
	c.pinned, c.member_read_at, c.reply_token, c.is_deleted
`

func scanComment(row pgx.Row) (*domain.LoanComment, error) {
	var c domain.LoanComment
	var attachmentJSON []byte
	var editJSON []byte
	err := row.Scan(
		&c.ID, &c.TenantID, &c.ApplicationID, &c.LoanID, &c.ParentID,
		&c.Visibility, &c.Body, &attachmentJSON,
		&c.AuthorUserID, &c.AuthorMemberID,
		&c.PostedAt, &c.EditedAt,
		&editJSON,
		&c.Pinned, &c.MemberReadAt, &c.ReplyToken, &c.IsDeleted,
	)
	if err != nil {
		return nil, err
	}
	if len(attachmentJSON) > 0 {
		_ = json.Unmarshal(attachmentJSON, &c.AttachmentPaths)
	}
	if len(editJSON) > 0 {
		c.EditHistory = editJSON
	}
	return &c, nil
}

// ─────────── Post ───────────

type PostCommentInput struct {
	ApplicationID *uuid.UUID
	LoanID        *uuid.UUID
	ParentID      *uuid.UUID
	Visibility    string // 'internal' | 'external'
	Body          string
	Attachments   []string
	AuthorUserID  *uuid.UUID
	AuthorMemberID *uuid.UUID
	ReplyToken    *uuid.UUID // pre-allocated by caller for external comments
}

func (s *LoanCommentsStore) PostTx(ctx context.Context, tx pgx.Tx, in PostCommentInput) (*domain.LoanComment, error) {
	if (in.ApplicationID == nil) == (in.LoanID == nil) {
		return nil, ErrCommentBadTarget
	}
	if (in.AuthorUserID == nil) == (in.AuthorMemberID == nil) {
		return nil, errors.New("comment must have exactly one of author_user_id, author_member_id")
	}
	if in.Visibility != "internal" && in.Visibility != "external" {
		return nil, errors.New("visibility must be internal | external")
	}
	attachmentJSON, _ := json.Marshal(append([]string{}, in.Attachments...))
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_comments (
		  tenant_id, application_id, loan_id, parent_id,
		  visibility, body, attachment_paths,
		  author_user_id, author_member_id,
		  reply_token
		) VALUES (
		  current_tenant_id(), $1, $2, $3,
		  $4, $5, $6::jsonb,
		  $7, $8,
		  $9
		)
		RETURNING `+loanCommentCols,
		in.ApplicationID, in.LoanID, in.ParentID,
		in.Visibility, in.Body, string(attachmentJSON),
		in.AuthorUserID, in.AuthorMemberID,
		in.ReplyToken,
	)
	return scanComment(row)
}

// ─────────── Read ───────────

func (s *LoanCommentsStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanComment, error) {
	row := tx.QueryRow(ctx, `SELECT `+loanCommentCols+` FROM loan_comments WHERE id = $1`, id)
	c, err := scanComment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCommentNotFound
	}
	return c, err
}

// ListByApplicationTx returns the full thread (pinned first, then
// chronological). When includeExternal is false, only internal rows are
// returned. Author display names are joined from the user table when
// possible; missing names render as empty (caller can substitute).
func (s *LoanCommentsStore) ListByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID, includeExternal bool) ([]domain.LoanComment, error) {
	return s.listWhereTx(ctx, tx, `c.application_id = $1`, appID, includeExternal)
}

func (s *LoanCommentsStore) ListByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, includeExternal bool) ([]domain.LoanComment, error) {
	return s.listWhereTx(ctx, tx, `c.loan_id = $1`, loanID, includeExternal)
}

func (s *LoanCommentsStore) listWhereTx(ctx context.Context, tx pgx.Tx, where string, arg uuid.UUID, includeExternal bool) ([]domain.LoanComment, error) {
	q := `
		SELECT ` + loanCommentColsAliased + `,
		       COALESCE(cd.full_name, u.full_name, '') AS author_name
		  FROM loan_comments c
		  LEFT JOIN users u ON u.id = c.author_user_id
		  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = c.author_member_id
		 WHERE ` + where
	if !includeExternal {
		q += ` AND visibility = 'internal'`
	}
	q += ` ORDER BY pinned DESC, posted_at ASC`
	rows, err := tx.Query(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanComment
	for rows.Next() {
		var c domain.LoanComment
		var attachmentJSON, editJSON []byte
		var authorName string
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.ApplicationID, &c.LoanID, &c.ParentID,
			&c.Visibility, &c.Body, &attachmentJSON,
			&c.AuthorUserID, &c.AuthorMemberID,
			&c.PostedAt, &c.EditedAt, &editJSON,
			&c.Pinned, &c.MemberReadAt, &c.ReplyToken, &c.IsDeleted,
			&authorName,
		); err != nil {
			return nil, err
		}
		if len(attachmentJSON) > 0 {
			_ = json.Unmarshal(attachmentJSON, &c.AttachmentPaths)
		}
		if len(editJSON) > 0 {
			c.EditHistory = editJSON
		}
		c.AuthorName = authorName
		out = append(out, c)
	}
	return out, rows.Err()
}

// ─────────── Mutations ───────────

// EditTx — only the author may edit. Records the prior body + edited_at
// into edit_history JSONB, stamps a new edited_at.
func (s *LoanCommentsStore) EditTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, authorUserID uuid.UUID, newBody string) (*domain.LoanComment, error) {
	cur, err := s.GetTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if cur.AuthorUserID == nil || *cur.AuthorUserID != authorUserID {
		return nil, ErrCommentForbidden
	}
	if cur.IsDeleted {
		return nil, errors.New("cannot edit a deleted comment")
	}
	prior := map[string]any{
		"body":     cur.Body,
		"edited_at": time.Now().UTC().Format(time.RFC3339),
	}
	priorJSON, _ := json.Marshal(prior)
	row := tx.QueryRow(ctx, `
		UPDATE loan_comments SET
		  body         = $2,
		  edited_at    = now(),
		  edit_history = COALESCE(edit_history, '[]'::jsonb) || $3::jsonb
		 WHERE id = $1
		 RETURNING `+loanCommentCols,
		id, newBody, string(priorJSON),
	)
	return scanComment(row)
}

func (s *LoanCommentsStore) PinTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, pinned bool) (*domain.LoanComment, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_comments SET pinned = $2 WHERE id = $1
		 RETURNING `+loanCommentCols, id, pinned)
	c, err := scanComment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCommentNotFound
	}
	return c, err
}

// SoftDeleteTx — only the author may delete. The row stays for threading
// + audit; the body becomes "[deleted]" + is_deleted = true.
func (s *LoanCommentsStore) SoftDeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, authorUserID uuid.UUID) error {
	cur, err := s.GetTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if cur.AuthorUserID == nil || *cur.AuthorUserID != authorUserID {
		return ErrCommentForbidden
	}
	prior := map[string]any{"body": cur.Body, "deleted_at": time.Now().UTC().Format(time.RFC3339)}
	priorJSON, _ := json.Marshal(prior)
	_, err = tx.Exec(ctx, `
		UPDATE loan_comments SET
		  body         = '[deleted]',
		  is_deleted   = true,
		  edited_at    = now(),
		  edit_history = COALESCE(edit_history, '[]'::jsonb) || $2::jsonb
		 WHERE id = $1
	`, id, string(priorJSON))
	return err
}

// MarkMemberReadByTokenTx — when the member opens /m/c/{token}, every
// external comment in the thread the token belongs to gets a
// member_read_at stamp. Returns count flipped.
func (s *LoanCommentsStore) MarkMemberReadByTokenTx(ctx context.Context, tx pgx.Tx, token uuid.UUID) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE loan_comments SET member_read_at = now()
		 WHERE visibility = 'external' AND member_read_at IS NULL
		   AND (
		     application_id IN (
		       SELECT application_id FROM loan_comments WHERE reply_token = $1
		     )
		     OR loan_id IN (
		       SELECT loan_id FROM loan_comments WHERE reply_token = $1
		     )
		   )
	`, token)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ─────────── Search ───────────

// SearchTx — ILIKE on body. Scoped by exactly one of application_id or
// loan_id. Returns at most 100 rows ordered by posted_at DESC.
func (s *LoanCommentsStore) SearchTx(ctx context.Context, tx pgx.Tx, applicationID, loanID *uuid.UUID, q string) ([]domain.LoanComment, error) {
	if (applicationID == nil) == (loanID == nil) {
		return nil, ErrCommentBadTarget
	}
	if len(q) == 0 {
		return nil, errors.New("q is required")
	}
	col := "c.application_id"
	var arg uuid.UUID
	if applicationID != nil {
		arg = *applicationID
	} else {
		col = "c.loan_id"
		arg = *loanID
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s,
		       COALESCE(cd.full_name, u.full_name, '') AS author_name
		  FROM loan_comments c
		  LEFT JOIN users u ON u.id = c.author_user_id
		  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = c.author_member_id
		 WHERE %s = $1 AND c.body ILIKE $2
		 ORDER BY c.posted_at DESC
		 LIMIT 100
	`, loanCommentColsAliased, col), arg, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanComment
	for rows.Next() {
		var c domain.LoanComment
		var attachmentJSON, editJSON []byte
		var authorName string
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.ApplicationID, &c.LoanID, &c.ParentID,
			&c.Visibility, &c.Body, &attachmentJSON,
			&c.AuthorUserID, &c.AuthorMemberID,
			&c.PostedAt, &c.EditedAt, &editJSON,
			&c.Pinned, &c.MemberReadAt, &c.ReplyToken, &c.IsDeleted,
			&authorName,
		); err != nil {
			return nil, err
		}
		if len(attachmentJSON) > 0 {
			_ = json.Unmarshal(attachmentJSON, &c.AttachmentPaths)
		}
		if len(editJSON) > 0 {
			c.EditHistory = editJSON
		}
		c.AuthorName = authorName
		out = append(out, c)
	}
	return out, rows.Err()
}

// ─────────── Templates ───────────

func (s *LoanCommentsStore) ListTemplatesTx(ctx context.Context, tx pgx.Tx) ([]domain.LoanCommentTemplate, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, label, visibility, body, is_active, created_at
		  FROM loan_comment_templates
		 WHERE is_active = true
		 ORDER BY visibility, label
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanCommentTemplate
	for rows.Next() {
		var t domain.LoanCommentTemplate
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Label, &t.Visibility, &t.Body, &t.IsActive, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *LoanCommentsStore) GetTemplateTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanCommentTemplate, error) {
	var t domain.LoanCommentTemplate
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, label, visibility, body, is_active, created_at
		  FROM loan_comment_templates WHERE id = $1
	`, id).Scan(&t.ID, &t.TenantID, &t.Label, &t.Visibility, &t.Body, &t.IsActive, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCommentTemplateBad
	}
	return &t, err
}

// ─────────── Public-route token bridge ───────────

// FindTenantByToken — SECURITY DEFINER bridge for the public reply
// route. Returns the comment_id + tenant_id so the handler can open
// a tenant-scoped tx before any other reads.
func (s *LoanCommentsStore) FindTenantByToken(ctx context.Context, token uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	var commentID, tenantID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT comment_id, tenant_id FROM find_comment_token_tenant($1)
	`, token).Scan(&commentID, &tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, ErrCommentNotFound
	}
	return commentID, tenantID, err
}
