// Testdata for R-OPEN-2 (opening_no_raw_insert). Mirrors the
// member/internal/store/application_store.go case study — a store
// outside the sanctioned writers (savings/store + finance/executor)
// must not write subledger transaction tables directly.

package store

import "context"

type fakeTx struct{}

func (fakeTx) Exec(_ context.Context, _ string, _ ...any) error { return nil }

// ─── Rule 6 positive: raw INSERT INTO deposit_transactions in
//     a non-handler, non-sanctioned package ───
func BadInsertDepositTransaction(ctx context.Context, tx fakeTx) error {
	return tx.Exec(ctx, `INSERT INTO deposit_transactions (id, amount) VALUES ($1, $2)`, 1, 2) // want "opening_no_raw_insert:"
}

// ─── Rule 6 positive: raw INSERT INTO share_transactions ───
func BadInsertShareTransaction(ctx context.Context, tx fakeTx) error {
	return tx.Exec(ctx, `INSERT INTO share_transactions (id, amount) VALUES ($1, $2)`, 1, 2) // want "opening_no_raw_insert:"
}

// ─── Rule 6 negative: SQL unrelated to subledger tables ───
func FineUnrelatedSQL(ctx context.Context, tx fakeTx) error {
	return tx.Exec(ctx, `UPDATE applications SET status = 'approved' WHERE id = $1`, 1)
}
