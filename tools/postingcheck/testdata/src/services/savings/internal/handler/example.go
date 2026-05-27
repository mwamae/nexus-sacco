// Testdata for the postingcheck analyzer. Comments with the
// analysistest marker on a flagged line declare the expected
// diagnostic. Stub types stand in for the real interfaces — the
// analyzer inspects AST shape, not type identity.

package handler

import "context"

type fakeStore struct{}
type fakeTx struct{}
type fakePosting struct{}

func (fakeStore) PostTxnTx(ctx context.Context, _ fakeTx, _ any) error { return nil }
func (fakePosting) PostTx(ctx context.Context, _ fakeTx, _ any) error  { return nil }
func (fakePosting) Post(ctx context.Context, _ any) error              { return nil }
func (fakeTx) Exec(ctx context.Context, sql string, _ ...any) error    { return nil }

type FakeHandler struct {
	Deposits fakeStore
	Posting  fakePosting
}

// ─── Rule 1 positive: PostTxnTx without PostTx, handler-level fn ───
func (h *FakeHandler) BadDeposit(ctx context.Context, tx fakeTx) error {
	return h.Deposits.PostTxnTx(ctx, tx, nil) // want "posting_required:"
}

// ─── Rule 1 negative: PostTxnTx WITH matching PostTx ───
func (h *FakeHandler) GoodDeposit(ctx context.Context, tx fakeTx) error {
	if err := h.Deposits.PostTxnTx(ctx, tx, nil); err != nil {
		return err
	}
	return h.Posting.PostTx(ctx, tx, nil)
}

// ─── Rule 1 exempt: Execute* prefix is the executor convention ───
func (h *FakeHandler) ExecuteDepositTx(ctx context.Context, tx fakeTx) error {
	// Executors legitimately write subledger without posting — the
	// HTTP handler that wraps them provides the post. Must NOT flag.
	return h.Deposits.PostTxnTx(ctx, tx, nil)
}

// ─── Rule 2 positive: raw SQL on subledger table ───
func (h *FakeHandler) BadRawSQL(ctx context.Context, tx fakeTx) error {
	return tx.Exec(ctx, "INSERT INTO deposit_transactions (id, amount) VALUES ($1, $2)", 1, 2) // want "posting_raw_sql:"
}

// ─── Rule 2 negative: tx.Exec on unrelated table ───
func (h *FakeHandler) FineRawSQL(ctx context.Context, tx fakeTx) error {
	return tx.Exec(ctx, "UPDATE deposit_accounts SET current_balance = $1", 100)
}

// ─── Rule 3 positive: Posting.Post (HTTP) called from handler ───
func (h *FakeHandler) BadPostHTTP(ctx context.Context, _ fakeTx) error {
	return h.Posting.Post(ctx, nil) // want "posting_post_http:"
}

// ─── Rule 3 negative: Posting.PostTx (outbox) is the right path ───
func (h *FakeHandler) FinePostTx(ctx context.Context, tx fakeTx) error {
	return h.Posting.PostTx(ctx, tx, nil)
}

// ─── Rule 1 negative: helper-via-convention counts as posting ───
//
// Handler calls Shares.PostTxnTx then a `post...Tx` helper. The
// helper internally calls Posting.PostTx; the analyzer recognises
// the name convention so the handler isn't double-bookkeeping the
// post.
func (h *FakeHandler) postFakeAdjustToGLTx(ctx context.Context, tx fakeTx) error {
	return h.Posting.PostTx(ctx, tx, nil)
}
func (h *FakeHandler) FineAdjustViaHelper(ctx context.Context, tx fakeTx) error {
	if err := h.Deposits.PostTxnTx(ctx, tx, nil); err != nil {
		return err
	}
	return h.postFakeAdjustToGLTx(ctx, tx)
}

// ─── Rule 1 exempt: per-line writer (batched JE in parent) ───
//
// Per-line executors used by interest/dividend runs legitimately
// write subledger rows without a per-line JE — the parent Post()
// emits one batched JE for the whole run.
func (h *FakeHandler) postFineLine(ctx context.Context, tx fakeTx) error {
	return h.Deposits.PostTxnTx(ctx, tx, nil)
}

// ─── Rule 1 exempt: // postingcheck:ignore <reason> annotation ───
//
// Acknowledged-gap suppression. Rationale must be in the comment.
//
// postingcheck:ignore deposit reversal GL post is its own follow-up PR
func (h *FakeHandler) DeferredGapDeposit(ctx context.Context, tx fakeTx) error {
	return h.Deposits.PostTxnTx(ctx, tx, nil)
}

// ─── Rule 4 positive: composite literal sets DryRun=true ───
//
// The hazard the rule prevents: someone constructs a Client{} with
// DryRun=true in a handler or store "just for now" and every
// money event downstream is silently dropped.
type FakeClient struct {
	DryRun bool
}

func BadDryRunComposite() *FakeClient {
	return &FakeClient{DryRun: true} // want "posting_dryrun_in_prod:"
}

// ─── Rule 4 positive: assignment sets DryRun=true ───
func BadDryRunAssign(c *FakeClient) {
	c.DryRun = true // want "posting_dryrun_in_prod:"
}

// ─── Rule 4 negative: DryRun: false is fine ───
func FineDryRunFalse() *FakeClient {
	return &FakeClient{DryRun: false}
}
