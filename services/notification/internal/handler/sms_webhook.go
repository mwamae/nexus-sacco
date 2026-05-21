// Africa's Talking delivery-report webhook. Now that the AT account
// is owned by the platform (Stage 9) the webhook secret comes from
// platform_sms_config; the tenant_id in the URL path still tells us
// which delivery row to update under the right RLS context.

package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/store"
)

type SMSWebhookHandler struct {
	DB          *db.Pool
	Notifs      *store.NotificationStore
	PlatformSMS *store.PlatformSMSStore // for webhook secret verification (future)
	Logger      *slog.Logger
}

// ATDeliveryReport accepts AT's form-encoded delivery-report POST:
//   id           = messageId from the send response
//   status       = Success | Sent | Buffered | Rejected | Failed | Delivered
//   phoneNumber  = recipient
//   networkCode  = MNO code
//   failureReason= (when Failed/Rejected)
func (h *SMSWebhookHandler) ATDeliveryReport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid tenant_id"))
		return
	}
	if err := r.ParseForm(); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("parse form: "+err.Error()))
		return
	}
	msgID := r.PostFormValue("id")
	status := r.PostFormValue("status")
	failureReason := r.PostFormValue("failureReason")
	if msgID == "" || status == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("id and status are required"))
		return
	}

	applyErr := h.DB.WithTenantTx(r.Context(), tenantID, func(tx pgx.Tx) error {
		deliveryID, _, err := h.Notifs.FindByProviderMessageIDTx(r.Context(), tx, domain.ChannelSMS, msgID)
		if errors.Is(err, store.ErrNotFound) {
			h.Logger.Warn("at delivery report: unknown message id",
				"tenant", tenantID, "id", msgID, "status", status)
			return nil
		}
		if err != nil {
			return err
		}
		switch status {
		case "Success", "Sent", "Delivered":
			return h.Notifs.MarkDeliveredTx(r.Context(), tx, deliveryID)
		case "Buffered":
			return nil
		case "Rejected", "Failed":
			reason := failureReason
			if reason == "" {
				reason = "AT reported " + status
			}
			return h.Notifs.MarkFailedFinalTx(r.Context(), tx, deliveryID, reason)
		default:
			h.Logger.Warn("at delivery report: unknown status",
				"tenant", tenantID, "id", msgID, "status", status)
			return nil
		}
	})
	if applyErr != nil {
		h.Logger.Warn("at delivery report: apply failed", "tenant", tenantID, "id", msgID, "err", applyErr)
	}
	// Always 200 — AT retries on non-2xx and we don't want that.
	w.WriteHeader(http.StatusOK)
}
