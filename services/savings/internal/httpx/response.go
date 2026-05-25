// Uniform JSON response/error envelopes.
//   Success: {"data": ...}
//   Error:   {"error": {"code": "...", "message": "...", "details": {...}}}
// Copied from services/member. Extract to a shared module when convenient.

package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

type ErrorCode string

const (
	CodeInvalidRequest ErrorCode = "invalid_request"
	CodeUnauthorized   ErrorCode = "unauthorized"
	CodeForbidden      ErrorCode = "forbidden"
	CodeNotFound       ErrorCode = "not_found"
	CodeConflict       ErrorCode = "conflict"
	CodeInternal       ErrorCode = "internal_error"
	// CodeGLPostFailed — the in-tx posting_outbox INSERT failed
	// (disk full / constraint violation / etc.). Mapped to 502
	// because the upstream the caller cares about (the
	// accounting service via the outbox) is the dependency that
	// effectively failed.
	CodeGLPostFailed ErrorCode = "gl_post_failed"
)

// ErrGLPostFailed wraps the detail from the underlying posting
// failure (typically an outbox INSERT error) into the standard
// 502 + gl_post_failed envelope.
func ErrGLPostFailed(detail string) *APIError {
	if detail == "" {
		detail = "could not record the GL post — please retry"
	}
	return E(http.StatusBadGateway, CodeGLPostFailed, detail)
}

type APIError struct {
	Status  int       `json:"-"`
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Details any       `json:"details,omitempty"`
}

func (e *APIError) Error() string { return string(e.Code) + ": " + e.Message }

func E(status int, code ErrorCode, msg string) *APIError {
	return &APIError{Status: status, Code: code, Message: msg}
}

func ErrBadRequest(msg string) *APIError { return E(http.StatusBadRequest, CodeInvalidRequest, msg) }
func ErrUnauthorized(msg string) *APIError {
	if msg == "" {
		msg = "authentication required"
	}
	return E(http.StatusUnauthorized, CodeUnauthorized, msg)
}
func ErrForbidden(msg string) *APIError {
	if msg == "" {
		msg = "permission denied"
	}
	return E(http.StatusForbidden, CodeForbidden, msg)
}
func ErrNotFound(msg string) *APIError {
	if msg == "" {
		msg = "resource not found"
	}
	return E(http.StatusNotFound, CodeNotFound, msg)
}
func ErrConflict(msg string) *APIError {
	return E(http.StatusConflict, CodeConflict, msg)
}
func ErrInternal() *APIError {
	return E(http.StatusInternalServerError, CodeInternal, "an unexpected error occurred")
}

func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write json", "err", err)
	}
}

func OK(w http.ResponseWriter, data any) {
	WriteJSON(w, http.StatusOK, map[string]any{"data": data})
}

func Created(w http.ResponseWriter, data any) {
	WriteJSON(w, http.StatusCreated, map[string]any{"data": data})
}

func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func WriteErr(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		WriteJSON(w, apiErr.Status, map[string]any{"error": apiErr})
		return
	}
	slog.Error("unhandled error", "method", r.Method, "path", r.URL.Path, "err", err)
	internal := ErrInternal()
	WriteJSON(w, internal.Status, map[string]any{"error": internal})
}

func DecodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return ErrBadRequest("invalid JSON body: " + err.Error())
	}
	return nil
}
