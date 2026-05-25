// Package postingcheck is a Go static analyzer that catches future
// regressions in the GL posting pipeline.
//
// Three rules, all scoped to functions in services/*/internal/handler/
// (so executor/store internals + the dispatcher binary aren't affected):
//
//   1. posting_required
//      A function that calls *.PostTxnTx (Deposits/Shares/Loans store
//      writes) must also call *.PostTx (the outbox-inserting variant)
//      in the same body — that's the in-tx GL post that keeps the
//      subledger and GL in lock-step.
//
//      Exemption: functions whose name starts with "Execute" are the
//      executor convention — they're wrapped by HTTP handlers which
//      provide the post. Flagging executors would be a false positive.
//
//   2. posting_raw_sql
//      A tx.Exec(...) call whose SQL string literal mutates
//      deposit_transactions / share_transactions / loan_transactions
//      directly. The store types own those tables; raw SQL in a
//      handler is the sign of someone routing around the wiring.
//
//   3. posting_post_http
//      A call to *.Posting.Post( (note: NOT .PostTx() ) from inside
//      a handler. .Post() is the HTTP path the dispatcher binary uses
//      to replay outbox rows — it shouldn't appear in handler code.
//      Warning rather than hard fail since legitimate edge-cases may
//      exist; the diagnostic asks for human review.

package postingcheck

import (
	"go/ast"
	"regexp"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

var Analyzer = &analysis.Analyzer{
	Name:     "postingcheck",
	Doc:      "checks that handler funcs that mutate subledger transactions also post to the GL outbox",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

// handlerPathRE matches files in any service's internal/handler/
// directory. Test files (*_test.go) are excluded — those legitimately
// poke at internals to verify behaviour.
var handlerPathRE = regexp.MustCompile(`/services/[^/]+/internal/handler/[^/]+\.go$`)

// rawSQLRE matches INSERT INTO / UPDATE on the three subledger tables.
// Case-insensitive, leading-whitespace tolerant.
var rawSQLRE = regexp.MustCompile(`(?i)\b(insert\s+into|update)\s+(deposit_transactions|share_transactions|loan_transactions)\b`)

// postHelperRE matches the repo's helper-function convention: methods
// named `post*Tx` that internally call Posting.PostTx (or write to the
// outbox). Calling one of these from a handler counts as posting
// evidence — same effect as calling PostTx directly.
//
// Examples: postDepositToGLTx, postShareAdjustmentToGLTx,
// postBatchedRunGLTx, postFeeLineTx, postRepaymentToGLTx.
var postHelperRE = regexp.MustCompile(`^post[A-Z].*Tx$`)

// perLineRE matches the per-line writer convention used by batched-JE
// runs (interest, dividend). These functions legitimately write
// per-member subledger rows without a per-line JE — the batched GL
// post happens in the parent Post() handler. Exempt by name.
//
// Examples: postDivLine, postLine.
var perLineRE = regexp.MustCompile(`^post[A-Z]?\w*Line$`)

// ignoreCommentRE matches a `// postingcheck:ignore <reason>` comment
// directly above a function. Use for deferred / acknowledged gaps:
// the comment is mandatory so the human-readable rationale is in the
// code rather than buried in a PR description.
var ignoreCommentRE = regexp.MustCompile(`postingcheck:ignore`)

func run(pass *analysis.Pass) (interface{}, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fn := n.(*ast.FuncDecl)
		if fn.Body == nil {
			return
		}

		// Path filter — only handler/ files, not tests.
		pos := pass.Fset.Position(fn.Pos())
		if !handlerPathRE.MatchString(pos.Filename) {
			return
		}
		if strings.HasSuffix(pos.Filename, "_test.go") {
			return
		}

		// Per-line writer exemption — batched JE happens in the parent
		// Post() handler. Same intent as the Execute* convention but
		// for the run-based pattern (interest_runs, dividend_runs).
		if perLineRE.MatchString(fn.Name.Name) {
			return
		}

		// Acknowledged-gap suppression: `// postingcheck:ignore <reason>`
		// above the function decl. We require the comment because the
		// rationale belongs in the code — analyzer-suppressed code is
		// the most fragile kind.
		if fn.Doc != nil && ignoreCommentRE.MatchString(fn.Doc.Text()) {
			return
		}

		// Walk the function body once and collect every call we care
		// about. Cheaper than three separate AST walks per function.
		var (
			callsPostTxnTx        bool
			postTxnTxPos          ast.Node
			callsPostTx           bool
			callsPostingPostHTTP  bool
			postingPostHTTPPos    ast.Node
			rawSQLPos             ast.Node
			rawSQLMatch           string
		)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := sel.Sel.Name
			switch {
			case name == "PostTxnTx":
				callsPostTxnTx = true
				if postTxnTxPos == nil {
					postTxnTxPos = call
				}
			case name == "PostTx":
				// Trust the method name regardless of receiver. The
				// PostingClient.PostTx is the only PostTx call we expect
				// in handler code; member's accounting.Client.PostTx
				// follows the same shape.
				callsPostTx = true
			case postHelperRE.MatchString(name):
				// Repo convention: methods named post*Tx are GL-posting
				// helpers (postDepositToGLTx, postShareAdjustmentToGLTx,
				// postFeeLineTx, postBatchedRunGLTx). Calling one
				// counts as posting evidence — keeps the analyzer in
				// sync with how handlers actually compose their post.
				callsPostTx = true
			case name == "Post":
				// Distinguish *.Posting.Post( from any other *.Post(.
				// Receiver must be a SelectorExpr ending in "Posting"
				// (e.g. h.Posting). HTTP-dispatcher style.
				if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "Posting" {
					callsPostingPostHTTP = true
					if postingPostHTTPPos == nil {
						postingPostHTTPPos = call
					}
				}
			case name == "Exec":
				// tx.Exec with raw subledger SQL. Look at the second
				// arg (the query string) for the regex match. pgx
				// signature: tx.Exec(ctx, sql, args...).
				if len(call.Args) < 2 {
					return true
				}
				lit, ok := call.Args[1].(*ast.BasicLit)
				if !ok {
					return true
				}
				if m := rawSQLRE.FindString(lit.Value); m != "" {
					if rawSQLPos == nil {
						rawSQLPos = call
						rawSQLMatch = strings.ToLower(strings.TrimSpace(m))
					}
				}
			}
			return true
		})

		// Rule 1 — posting_required. Skip executor convention.
		if callsPostTxnTx && !callsPostTx && !strings.HasPrefix(fn.Name.Name, "Execute") {
			pass.Reportf(postTxnTxPos.Pos(),
				"posting_required: %s calls *.PostTxnTx (subledger write) without a matching *.PostTx (in-tx outbox post). "+
					"Wire the GL post inside the same WithTenantTx — see services/savings/internal/handler/deposit.go::postDepositToGLTx for the canonical pattern.",
				fn.Name.Name)
		}

		// Rule 2 — posting_raw_sql. Anything in a handler shouldn't
		// write subledger transactions via raw SQL. Use the store.
		if rawSQLPos != nil {
			pass.Reportf(rawSQLPos.Pos(),
				"posting_raw_sql: %s issues raw SQL %q against a subledger transactions table. "+
					"Route writes through the matching Store.PostTxnTx so the GL outbox post stays paired with the business write.",
				fn.Name.Name, rawSQLMatch)
		}

		// Rule 3 — posting_post_http. Warning: handler shouldn't call
		// the HTTP path; .PostTx writes to the outbox and the
		// dispatcher handles delivery.
		if callsPostingPostHTTP {
			pass.Reportf(postingPostHTTPPos.Pos(),
				"posting_post_http: %s calls Posting.Post (HTTP path). "+
					"Handlers should use Posting.PostTx (outbox-insert, in-tx). "+
					".Post is the dispatcher's path. If this is intentional, add a // postingcheck:ok comment above the call and explain why.",
				fn.Name.Name)
		}
	})
	return nil, nil
}
