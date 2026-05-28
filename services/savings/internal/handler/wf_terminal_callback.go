// POST /internal/v1/workflow-terminal-action — the endpoint the
// workflow service's callback-dispatcher POSTs to when a
// savings-owned wf_instance reaches a terminal state.
//
// Replaces the per-pending-approval ResolveFromWorkflow path for
// cash kinds: those instances DON'T queue a pending_approvals shim
// row anymore, so there's no approval_id in the URL. The dispatcher
// posts the full instance JSON instead and we route on process_kind
// to the registered Go callback in wf_callbacks/.
//
// Auth: X-Internal-Token header matched against WorkflowInternalToken.
// Falls back to a User-Agent prefix check when the token isn't
// configured (dev convenience; prod always sets the token).
//
// Idempotency: handled by the executor — wf_callbacks contracts
// require that calling the same callback for the same instance_id
// twice produces the same outcome. The dispatcher will redeliver
// after a transient 5xx, so the callback must be a no-op (or replay
// to the same end-state) on the second call.

package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/handler/wf_callbacks"
	"github.com/nexussacco/savings/internal/httpx"
)

// WorkflowTerminalCallbackHandler is the small struct that owns
// the dispatch endpoint. The registry is built once at boot in
// cmd/server/main.go and passed in here.
type WorkflowTerminalCallbackHandler struct {
	DB                    *db.Pool
	Registry              *wf_callbacks.Registry
	WorkflowInternalToken string
}

// callbackEnvelope is the shape the workflow callback-dispatcher
// posts. Mirrors the body the dispatcher builds at
// services/workflow/cmd/callback-dispatcher/main.go::postOnce.
type callbackEnvelope struct {
	TenantID    string             `json:"tenant_id"`
	Instance    wf_callbacks.Instance `json:"instance"`
	Event       string             `json:"event"`
	DeliveredAt string             `json:"delivered_at"`
}

// Handle decodes the dispatcher's POST and routes to the registered
// callback. Status codes mirror what the dispatcher reads:
//
//   200 OK            → callback succeeded; dispatcher marks delivered
//   400 Bad Request   → permanent parse error; dispatcher will retry
//                       then DLQ (12 attempts of the same bad payload
//                       is the operator's signal something upstream
//                       is wrong)
//   401 Unauthorized  → bad internal token; dispatcher retries (token
//                       may have rotated; persistent failure DLQs)
//   404 Not Found     → no registered callback for the process_kind;
//                       dispatcher will retry then DLQ — kind needs
//                       to be registered before instances of it can
//                       terminate
//   500 Internal      → callback returned an error; dispatcher retries
//                       with exponential backoff
func (h *WorkflowTerminalCallbackHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// Trust gate — match WorkflowInternalToken, else fall back to
	// the User-Agent prefix the workflow service always sends. Mirrors
	// the auth shape of the existing ResolveFromWorkflow endpoint so
	// the two stay consistent.
	expected := h.WorkflowInternalToken
	got := r.Header.Get("X-Internal-Token")
	if expected != "" {
		if got != expected {
			httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
			return
		}
	} else if !strings.HasPrefix(r.Header.Get("User-Agent"), "nexus-workflow") {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("workflow callback expected"))
		return
	}

	var env callbackEnvelope
	if err := json.NewDecoder(r.Body).Decode(&env); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("decode envelope: "+err.Error()))
		return
	}
	inst := env.Instance
	if inst.ID.String() == "" || inst.TenantID.String() == "" || inst.ProcessKind == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("instance.id, instance.tenant_id, instance.process_kind are required"))
		return
	}

	cb, ok := h.Registry.Lookup(inst.ProcessKind)
	if !ok {
		// 404 = "I'll never know how to handle this kind". The
		// dispatcher retries-then-DLQs so an operator notices —
		// the right fix is to register a callback for the kind
		// (see services/savings/internal/handler/wf_callbacks/).
		httpx.WriteErr(w, r, httpx.ErrNotFound("no callback registered for process_kind "+inst.ProcessKind))
		return
	}

	// Run inside WithTenantTx so the callback sees the correct
	// app.tenant_id GUC for RLS. Any non-nil return rolls back the
	// tx and surfaces 500 to the dispatcher.
	err := h.DB.WithTenantTx(r.Context(), inst.TenantID, func(tx pgx.Tx) error {
		return cb(r.Context(), tx, inst)
	})
	if err != nil {
		// 500 with the error message in the body — the dispatcher
		// stores this verbatim in callback_last_error so the operator
		// triaging a DLQ row sees the savings-side failure reason.
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{
		"instance_id": inst.ID,
		"status":      "delivered",
	})
}
