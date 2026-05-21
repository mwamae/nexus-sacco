// Tiny helpers shared by handlers that fire notifications.
// Keeping these here so the notifier-fan-out code in each handler
// reads cleanly without inline nil/zero gymnastics.

package handler

import "github.com/google/uuid"

// nonZeroUUID returns nil if u is the zero UUID, otherwise a pointer.
// Used to keep `initiated_by` NULL for system-driven actions while
// passing the actor ID for user-driven ones.
func nonZeroUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

// derefString returns "" for a nil string pointer; convenient for
// payload maps where we want the empty string rather than the literal
// JSON null when the field is unset upstream.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
