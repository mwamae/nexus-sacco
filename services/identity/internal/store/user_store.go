// User store — tenant-scoped. Most reads/writes go through the
// db.WithTenantTx helper, which sets app.tenant_id for RLS.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/identity/internal/domain"
)

type UserStore struct {
	pool *pgxpool.Pool
}

func NewUserStore(pool *pgxpool.Pool) *UserStore {
	return &UserStore{pool: pool}
}

type CreateUserInput struct {
	TenantID        uuid.UUID
	Email           string
	Phone           string
	FullName        string
	PasswordHash    string // may be "" for pending users created via invite
	Status          domain.UserStatus
	IsPlatformAdmin bool
}

// CreateTx inserts inside an existing transaction so callers can pair
// it with role assignment atomically.
func (s *UserStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateUserInput) (*domain.User, error) {
	var u domain.User
	err := tx.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, phone, full_name, password_hash, status, is_platform_admin)
		VALUES ($1, $2, NULLIF($3,''), $4, NULLIF($5,''), $6, $7)
		RETURNING id, tenant_id, email, COALESCE(phone,''), full_name, status,
		          is_platform_admin, email_verified_at, mfa_enabled, COALESCE(mfa_method,''),
		          last_login_at, created_at, updated_at
	`, in.TenantID, in.Email, in.Phone, in.FullName, in.PasswordHash, in.Status, in.IsPlatformAdmin).
		Scan(&u.ID, &u.TenantID, &u.Email, &u.Phone, &u.FullName, &u.Status,
			&u.IsPlatformAdmin, &u.EmailVerifiedAt, &u.MFAEnabled, &u.MFAMethod,
			&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return &u, nil
}

// UpdateProfileTx updates editable identity fields. Pass empty phone to clear.
func (s *UserStore) UpdateProfileTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, fullName, phone string) error {
	_, err := tx.Exec(ctx, `
		UPDATE users SET full_name = $2, phone = NULLIF($3,'')
		WHERE id = $1
	`, id, fullName, phone)
	return err
}

// SetStatusTx changes a user's lifecycle status. Suspending also revokes
// all active sessions (callers do that via the session store).
func (s *UserStore) SetStatusTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.UserStatus) error {
	_, err := tx.Exec(ctx, `UPDATE users SET status = $2 WHERE id = $1`, id, status)
	return err
}

// ActivateWithPasswordTx flips a pending user to active and sets their
// password hash. Used when accepting an invite.
func (s *UserStore) ActivateWithPasswordTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, hash string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE users
		SET password_hash = $2, status = 'active', email_verified_at = COALESCE(email_verified_at, now()),
		    failed_login_count = 0, locked_until = NULL
		WHERE id = $1 AND status = 'pending'
	`, id, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ByEmailTx looks up a user by email within the active tenant context.
// Use inside a tenant-scoped transaction.
func (s *UserStore) ByEmailTx(ctx context.Context, tx pgx.Tx, email string) (*domain.User, error) {
	return scanUser(tx.QueryRow(ctx, selectUser+` WHERE email = $1`, email))
}

func (s *UserStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.User, error) {
	return scanUser(tx.QueryRow(ctx, selectUser+` WHERE id = $1`, id))
}

// PasswordHashByEmailTx returns the stored hash plus minimal identity
// — used by the login path before we want to leak whether the user exists.
func (s *UserStore) PasswordHashByEmailTx(ctx context.Context, tx pgx.Tx, email string) (uuid.UUID, string, error) {
	var id uuid.UUID
	var hash string
	err := tx.QueryRow(ctx, `SELECT id, password_hash FROM users WHERE email = $1 AND status = 'active'`, email).
		Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrNotFound
	}
	return id, hash, err
}

func (s *UserStore) RecordLoginTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE users SET last_login_at = now(), failed_login_count = 0
		WHERE id = $1
	`, userID)
	return err
}

func (s *UserStore) RecordFailedLoginTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	// Lock after 10 consecutive failures for 15 minutes.
	_, err := tx.Exec(ctx, `
		UPDATE users
		SET failed_login_count = failed_login_count + 1,
		    locked_until = CASE
		      WHEN failed_login_count + 1 >= 10 THEN now() + interval '15 minutes'
		      ELSE locked_until
		    END
		WHERE id = $1
	`, userID)
	return err
}

// MFAInfoTx returns whether MFA is enabled and, if so, the chosen method.
// Used by login to decide whether to issue tokens or a challenge.
func (s *UserStore) MFAInfoTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (bool, string, error) {
	var enabled bool
	var method *string
	err := tx.QueryRow(ctx, `SELECT mfa_enabled, mfa_method FROM users WHERE id = $1`, userID).
		Scan(&enabled, &method)
	if err != nil {
		return false, "", err
	}
	m := ""
	if method != nil {
		m = *method
	}
	return enabled, m, nil
}

func (s *UserStore) SetMFAEnabledTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, enabled bool, method string) error {
	if enabled {
		_, err := tx.Exec(ctx, `UPDATE users SET mfa_enabled = true, mfa_method = $2 WHERE id = $1`, userID, method)
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE users SET mfa_enabled = false, mfa_method = NULL, mfa_secret = NULL WHERE id = $1`, userID)
	return err
}

// PasswordHashByIDTx returns the stored hash for a given user — used by
// the MFA disable flow that requires password reconfirmation.
func (s *UserStore) PasswordHashByIDTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (string, error) {
	var hash string
	err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// UpdatePasswordHashTx writes a new password hash and clears any lockout
// state. Callers are expected to revoke active refresh tokens separately.
func (s *UserStore) UpdatePasswordHashTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, hash string) error {
	_, err := tx.Exec(ctx, `
		UPDATE users
		SET password_hash = $2,
		    failed_login_count = 0,
		    locked_until = NULL
		WHERE id = $1
	`, userID, hash)
	return err
}

func (s *UserStore) IsLockedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (bool, error) {
	var lockedUntil *time.Time
	err := tx.QueryRow(ctx, `SELECT locked_until FROM users WHERE id = $1`, userID).Scan(&lockedUntil)
	if err != nil {
		return false, err
	}
	return lockedUntil != nil && lockedUntil.After(time.Now()), nil
}

type ListUsersResult struct {
	Users []*domain.User
	Total int
}

func (s *UserStore) ListTx(ctx context.Context, tx pgx.Tx, limit, offset int) (*ListUsersResult, error) {
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, selectUser+` ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &ListUsersResult{Total: total}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out.Users = append(out.Users, u)
	}
	return out, rows.Err()
}

// ─────────── Helpers ───────────

const selectUser = `
SELECT id, tenant_id, email, COALESCE(phone,''), full_name, status,
       is_platform_admin, email_verified_at, mfa_enabled, COALESCE(mfa_method,''),
       last_login_at, created_at, updated_at
FROM users`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (*domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.TenantID, &u.Email, &u.Phone, &u.FullName, &u.Status,
		&u.IsPlatformAdmin, &u.EmailVerifiedAt, &u.MFAEnabled, &u.MFAMethod,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// PoolForBackgroundWork exposes the raw pool for the rare path where
// we need it (the auth service holds its own pool and wraps transactions).
func (s *UserStore) PoolForBackgroundWork() *pgxpool.Pool { return s.pool }
