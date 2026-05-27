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
	"go/token"
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
			callsPostTxnTx       bool
			postTxnTxPos         ast.Node
			callsPostTx          bool
			callsPostingPostHTTP bool
			postingPostHTTPPos   ast.Node
			rawSQLPos            ast.Node
			rawSQLMatch          string
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

	// Rule 4 — posting_dryrun_in_prod. ANY non-test file under
	// services/*/ that sets DryRun=true is flagged. The hazard the
	// rule prevents is the resurrected silent-no-op: someone in a
	// handler/store writes `client.DryRun = true` (or
	// `&posting.Client{DryRun: true, ...}`) "just for now" and
	// every money event downstream is dropped. Tests legitimately
	// set DryRun=true via struct literals; _test.go files are the
	// only exemption.
	insp.Preorder([]ast.Node{(*ast.KeyValueExpr)(nil), (*ast.AssignStmt)(nil)}, func(n ast.Node) {
		pos := pass.Fset.Position(n.Pos())
		if !servicesPathRE.MatchString(pos.Filename) {
			return
		}
		if strings.HasSuffix(pos.Filename, "_test.go") {
			return
		}
		switch x := n.(type) {
		case *ast.KeyValueExpr:
			// Composite literal field: `DryRun: true` inside a Client{}.
			key, ok := x.Key.(*ast.Ident)
			if !ok || key.Name != "DryRun" {
				return
			}
			if !isTrueLit(x.Value) {
				return
			}
			pass.Reportf(x.Pos(),
				"posting_dryrun_in_prod: DryRun: true in a non-test file. "+
					"DryRun is a test-only escape — see services/savings/internal/posting/client.go. "+
					"Setting it in production code silently drops every money event. "+
					"If this is a test fixture, move it to a _test.go file.")
		case *ast.AssignStmt:
			// Assignment: `client.DryRun = true`. Look for a SelectorExpr
			// LHS named DryRun and a true RHS.
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return
			}
			sel, ok := x.Lhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "DryRun" {
				return
			}
			if !isTrueLit(x.Rhs[0]) {
				return
			}
			pass.Reportf(x.Pos(),
				"posting_dryrun_in_prod: %s.DryRun = true in a non-test file. "+
					"DryRun is a test-only escape. Setting it here silently drops every money event downstream.",
				exprName(sel.X))
		}
	})

	// Rule 5 — R-OPEN-1 (opening_required). A handler function
	// whose body references a struct field named Opening<X> (the
	// repo convention for opening-deposit / opening-share /
	// opening-contribution amounts) must go through at least one of:
	//   • Approvals.QueueTx        (queues a maker-checker gate)
	//   • receiptops.WriteTx       (writes a receipt header + line)
	//   • postingops.Post*Tx       (writes the in-tx GL outbox)
	//   • finance/executor.PostOpeningDepositTx (the cross-module
	//     sanctioned helper)
	// Skipping ALL of those is the bug — the original Open handler
	// observed OpeningDeposit then wrote a deposit_transactions row
	// without any of the three. R-OPEN-1 catches the shape so the
	// regression can't return.
	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fn := n.(*ast.FuncDecl)
		if fn.Body == nil {
			return
		}
		pos := pass.Fset.Position(fn.Pos())
		if !handlerPathRE.MatchString(pos.Filename) {
			return
		}
		if strings.HasSuffix(pos.Filename, "_test.go") {
			return
		}
		// Acknowledged-gap suppression — same convention Rule 1 uses.
		// `// postingcheck:ignore <reason>` above the function decl
		// silences this rule. The application Create handler is the
		// canonical case: it persists OpeningShareAmount /
		// OpeningBosaAmount on the application row; the actual
		// money post happens in activateIndividualTx via
		// finance/executor.PostOpeningDepositTx (which R-OPEN-2
		// covers).
		if fn.Doc != nil && ignoreCommentRE.MatchString(fn.Doc.Text()) {
			return
		}
		// The function-body comments (above the field-observing line)
		// also satisfy the ignore — covers cases where the rationale
		// is local to the observing statement rather than function-
		// wide. Walk the function's CommentMap once.
		if hasInlineIgnore(pass, fn) {
			return
		}
		var (
			observesOpening bool
			openingFieldPos ast.Node
			satisfied       bool
		)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				// Field access whose selector matches Opening<X>.
				if openingFieldRE.MatchString(x.Sel.Name) {
					observesOpening = true
					if openingFieldPos == nil {
						openingFieldPos = x
					}
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := sel.Sel.Name
				// receiptops.WriteTx — the receipt writer
				// postingops.Post*Tx — the GL outbox writers
				// PostOpeningDepositTx — the cross-module sanctioned helper
				// QueueTx — Approvals.QueueTx (queue the maker-checker gate)
				// executeDepositInlineTx — savings's handler-level seam
				if name == "WriteTx" || name == "QueueTx" || name == "PostOpeningDepositTx" ||
					name == "executeDepositInlineTx" || openingPostHelperRE.MatchString(name) {
					satisfied = true
				}
			}
			return true
		})
		if observesOpening && !satisfied {
			pass.Reportf(openingFieldPos.Pos(),
				"opening_required: %s observes an Opening* input but doesn't call any of {Approvals.QueueTx, receiptops.WriteTx, postingops.Post*Tx, executor.PostOpeningDepositTx}. "+
					"Opening-money inputs MUST go through approval / receipt / GL — pick one. "+
					"See services/savings/internal/handler/deposit.go::Open for the canonical composition.",
				fn.Name.Name)
		}
	})

	// Rule 6 — R-OPEN-2 (no raw subledger INSERTs outside sanctioned
	// writers). The store layer is the only place that owns the
	// subledger transaction tables; everywhere else must route
	// through CreateAccountTx / PostTxnTx / finance/executor.
	// Sanctioned writers:
	//   services/savings/internal/store/
	//   services/finance/executor/
	// The pre-fix application_store.go was the case study — it
	// INSERTed directly into deposit_transactions, bypassing every
	// shared primitive.
	insp.Preorder([]ast.Node{(*ast.BasicLit)(nil)}, func(n ast.Node) {
		lit := n.(*ast.BasicLit)
		if lit.Kind.String() != "STRING" {
			return
		}
		pos := pass.Fset.Position(lit.Pos())
		if !servicesPathRE.MatchString(pos.Filename) {
			return
		}
		if strings.HasSuffix(pos.Filename, "_test.go") {
			return
		}
		if sanctionedWriterPathRE.MatchString(pos.Filename) {
			return
		}
		// handler/ files are covered by Rule 2 (posting_raw_sql),
		// which emits a more specific "use the store" diagnostic.
		// Skip them here to keep the two rules complementary.
		if handlerPathRE.MatchString(pos.Filename) {
			return
		}
		if !subledgerInsertRE.MatchString(lit.Value) {
			return
		}
		pass.Reportf(lit.Pos(),
			"opening_no_raw_insert: SQL %q writes a subledger transaction table from outside the sanctioned writers. "+
				"Route through services/savings/internal/store/ or services/finance/executor/ — those are the only packages that own these tables. "+
				"See finance/executor/PostOpeningDepositTx for the opening-deposit path; services/savings/internal/store/deposit_store.go::PostTxnTx for the standard-deposit path.",
			truncSQL(lit.Value))
	})

	return nil, nil
}

// servicesPathRE matches every file under services/<svc>/ (any
// subdirectory). Broader than handlerPathRE because the DryRun rule
// applies to handlers, stores, cmd binaries, executors — everywhere.
var servicesPathRE = regexp.MustCompile(`/services/[^/]+/.+\.go$`)

// openingFieldRE matches money-moving struct field selectors. Tight
// closed list — `OpeningBalance` on a bank statement or till session
// is metadata, not a money-moving handler input, so flagging it would
// false-positive on the accounting handlers. Add new field names
// here as new opening-money flows land.
var openingFieldRE = regexp.MustCompile(`^(OpeningDeposit|OpeningShareAmount|OpeningBosaAmount|OpeningContribution)$`)

// openingPostHelperRE matches the repo's helper convention for
// opening-deposit posting (postOpeningDepositToGLTx, etc.) and the
// other post*ToGLTx helpers a handler might compose with the
// opening flow.
var openingPostHelperRE = regexp.MustCompile(`^post[A-Z].*Tx$|^Post[A-Z].*Tx$`)

// subledgerInsertRE matches an SQL string that writes (INSERT or
// UPDATE) to one of the three subledger transaction tables.
// Case-insensitive; allows leading whitespace/newlines from
// multi-line SQL literals.
var subledgerInsertRE = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+(deposit_transactions|share_transactions|loan_transactions)\b`)

// sanctionedWriterPathRE matches the two packages that legitimately
// write subledger transaction tables.
var sanctionedWriterPathRE = regexp.MustCompile(`/services/(savings/internal/store|finance/executor)/`)

// hasInlineIgnore returns true when any comment positioned inside
// the function body carries the postingcheck:ignore marker. Comments
// aren't part of the AST visited by ast.Inspect; we walk every
// CommentGroup in every file the pass owns and check position.
func hasInlineIgnore(pass *analysis.Pass, fn *ast.FuncDecl) bool {
	if fn.Body == nil {
		return false
	}
	bodyStart := fn.Body.Lbrace
	bodyEnd := fn.Body.Rbrace
	for _, file := range pass.Files {
		for _, cg := range file.Comments {
			if cg.Pos() < bodyStart || cg.End() > bodyEnd {
				continue
			}
			if ignoreCommentRE.MatchString(cg.Text()) {
				return true
			}
		}
	}
	return false
}

// (token import previously needed; kept for compatibility if hooks
// later want positional checks beyond what pass.Files provides.)
var _ token.Pos

// truncSQL keeps the diagnostic legible — long multi-line INSERT
// statements would otherwise scroll the analyzer output off-screen.
func truncSQL(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "`\""))
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}

// isTrueLit reports whether the expression is the Go literal `true`.
func isTrueLit(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "true"
}

// exprName renders a SelectorExpr's receiver as a string for the
// diagnostic. Best-effort — falls back to "?".
func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprName(v.X) + "." + v.Sel.Name
	}
	return "?"
}
