package daraja

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAuthenticate_HappyPath(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.URL.Path != "/oauth/v1/generate" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("grant_type"); got != "client_credentials" {
			t.Errorf("grant_type=%q", got)
		}
		// Decode the Basic auth header to confirm credentials made
		// the trip intact.
		const prefix = "Basic "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) {
			t.Fatalf("expected Basic auth, got %q", auth)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, prefix))
		if err != nil {
			t.Fatalf("decode auth: %v", err)
		}
		if string(decoded) != "ck:cs" {
			t.Errorf("creds round-trip: want ck:cs, got %q", decoded)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "dummy-token",
			"expires_in":   "3599",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, Sandbox)
	tok, err := c.Authenticate(context.Background(), "ck", "cs")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if tok.AccessToken != "dummy-token" {
		t.Errorf("token: %q", tok.AccessToken)
	}
	if time.Until(tok.ExpiresAt) < 50*time.Minute {
		t.Errorf("expires_in not applied: ExpiresAt=%s", tok.ExpiresAt)
	}
	if seen.Load() != 1 {
		t.Errorf("expected exactly 1 request, got %d", seen.Load())
	}
}

func TestAuthenticate_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorCode":"401.002","errorMessage":"bad credentials"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, Sandbox)
	_, err := c.Authenticate(context.Background(), "ck", "cs")
	if err == nil {
		t.Fatal("expected error from 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestAuthenticate_EmptyCredentialsRejected(t *testing.T) {
	c := NewClient("http://unused", Sandbox)
	if _, err := c.Authenticate(context.Background(), "", "x"); err == nil {
		t.Error("empty consumer key should error")
	}
	if _, err := c.Authenticate(context.Background(), "x", ""); err == nil {
		t.Error("empty consumer secret should error")
	}
}

func TestAuthenticateForPaybill_CacheHit(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok",
			"expires_in":   "3599",
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, Sandbox)
	key := CacheKey{PaybillID: uuid.New(), KeyID: "kms-test"}

	// First call hits Daraja.
	if _, err := c.AuthenticateForPaybill(context.Background(), key, "ck", "cs"); err != nil {
		t.Fatal(err)
	}
	// Second call within the safety window should be served from
	// cache — no second request.
	if _, err := c.AuthenticateForPaybill(context.Background(), key, "ck", "cs"); err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 1 {
		t.Errorf("expected cache hit, but Daraja was called %d times", seen.Load())
	}

	// Invalidate forces a refetch — exercises the credential-
	// rotation hook.
	c.Invalidate(key)
	if _, err := c.AuthenticateForPaybill(context.Background(), key, "ck", "cs"); err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 2 {
		t.Errorf("expected refetch after Invalidate, got %d", seen.Load())
	}
}

func TestAuthenticateForPaybill_DifferentKeyIDsIsolate(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": "3599"})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, Sandbox)
	pb := uuid.New()
	k1 := CacheKey{PaybillID: pb, KeyID: "kms-001"}
	k2 := CacheKey{PaybillID: pb, KeyID: "kms-002"}
	_, _ = c.AuthenticateForPaybill(context.Background(), k1, "ck", "cs")
	_, _ = c.AuthenticateForPaybill(context.Background(), k2, "ck", "cs")
	if seen.Load() != 2 {
		t.Errorf("different key ids should miss the cache independently; got %d hits", seen.Load())
	}
}

func TestAuthenticate_TimeoutSurfacedAsError(t *testing.T) {
	// httptest server that never responds — exercises the request
	// timeout path. We use a context deadline (rather than
	// http.Client.Timeout) because the request must be torn down
	// cleanly so srv.Close()'s "wait for in-flight requests" guard
	// doesn't deadlock on a hung handler.
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang
	}))
	// LIFO order: close(hang) must run BEFORE srv.Close() so the
	// handler returns and srv.Close()'s in-flight drain completes.
	defer srv.Close()
	defer close(hang)

	c := NewClient(srv.URL, Sandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Authenticate(ctx, "ck", "cs")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("unexpected error shape: %v", err)
	}
}
