// Server-Sent Events endpoint for real-time notification push.
//
// Wire format:
//
//   event: ready             (sent once on connect)
//   data: {"unread": 7}
//
//   event: notification      (each time the bus publishes)
//   data: {<FeedItem JSON>}
//
//   :heartbeat               (every 25s; keeps proxies from idling out)
//
// Auth is via the standard JWT middleware — clients pass the token as
// ?token=<jwt> on the URL since EventSource can't set headers.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/bus"
	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/store"
)

type SSEHandler struct {
	DB     *db.Pool
	Notifs *store.NotificationStore
	Bus    *bus.Bus
	Logger *slog.Logger
}

func (h *SSEHandler) Stream(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	if userID == uuid.Nil {
		http.Error(w, "user identity required", http.StatusUnauthorized)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering for nginx (no-op elsewhere). Critical
	// for SSE to actually stream rather than buffer.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Initial "ready" event so the client knows the stream is up
	// before any notifications arrive. Includes the current unread
	// count to seed the bell badge without an extra round-trip.
	unread := h.unreadCount(r.Context(), tid, userID)
	writeSSE(w, "ready", map[string]any{"unread": unread})
	flusher.Flush()

	sub, unsubscribe := h.Bus.Subscribe(bus.Key{TenantID: tid, UserID: userID})
	defer unsubscribe()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	h.Logger.Debug("sse: client connected", "user", userID, "tenant", tid)
	defer h.Logger.Debug("sse: client disconnected", "user", userID, "tenant", tid)

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(":heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case item := <-sub:
			if item == nil {
				return // bus closed our channel
			}
			writeSSE(w, "notification", item)
			flusher.Flush()
		}
	}
}

func (h *SSEHandler) unreadCount(ctx context.Context, tid, userID uuid.UUID) int {
	var n int
	_ = h.DB.WithTenantTx(ctx, tid, func(tx pgx.Tx) error {
		var err error
		n, err = h.Notifs.UnreadCountForUserTx(ctx, tx, userID)
		return err
	})
	return n
}

func writeSSE(w http.ResponseWriter, event string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}
