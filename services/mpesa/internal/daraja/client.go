// Daraja API client.
//
// Phase 1 implements just the OAuth `Authenticate` round-trip + the
// per-paybill token cache. The actual C2B / B2C / reversal calls are
// stubbed in signer.go; phases 2–5 fill in the body of those calls.
//
// Why a per-paybill cache: each paybill carries its own consumer
// key/secret, and Daraja issues a token bound to that pair. Caching
// at the (paybill_id, key_id) granularity means we don't burn the
// OAuth quota on every confirmation event, and a credential rotation
// (which changes key_id) cleanly invalidates the cached token without
// us having to track which token corresponded to which secret.

package daraja

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Environment string

const (
	Sandbox    Environment = "sandbox"
	Production Environment = "production"
)

// Token mirrors Daraja's OAuth response. ExpiresAt is computed at
// receive time (server clock); we reserve a 60-second safety margin
// when deciding cache freshness so a request that would race against
// the boundary still has time to land before the token's real expiry.
type Token struct {
	AccessToken string
	ExpiresAt   time.Time
}

// CacheKey is what the in-memory token cache is keyed on. It carries
// the paybill id (different paybills almost certainly have different
// credentials) AND the key id stamped into the credential ciphertext
// (so a credential rotation cleanly invalidates the prior token —
// the new key id won't hit the same cache slot).
type CacheKey struct {
	PaybillID uuid.UUID
	KeyID     string
}

// Client wraps an http.Client with the Daraja base URL + a token
// cache. Construct one per service-instance; the in-memory cache is
// process-local. Distributed cache lives in the dispatcher worker
// (phase 4), not here.
type Client struct {
	BaseURL     string
	Environment Environment

	httpClient *http.Client

	mu    sync.Mutex
	cache map[CacheKey]Token
}

// NewClient builds a Client. The transport pins the Safaricom roots
// when PinnedRootCertsPEM is non-empty (loaded by LoadProductionPins
// from internal/daraja/certs/production.pem); otherwise it falls back
// to the system pool so sandbox traffic still works.
//
// Phase 6: the caller (cmd/server, cmd/reconciler, cmd/b2c-dispatcher)
// is responsible for invoking LoadProductionPins before constructing
// the Client when env != sandbox. The constructor stays decoupled
// from MPESA_FORCE_SANDBOX so unit tests can spin up a Client
// against any base URL without pulling in the env-config layer.
func NewClient(baseURL string, env Environment) *Client {
	tr := &http.Transport{
		ResponseHeaderTimeout: 12 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	if pool := pinnedPool(); pool != nil {
		// Populate TLSClientConfig only when we have a real pin —
		// passing a nil-everywhere config inadvertently silences
		// Go's default cipher selection in some versions.
		tr.TLSClientConfig = newTLSConfig(pool)
	}
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		Environment: env,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   15 * time.Second,
		},
		cache: map[CacheKey]Token{},
	}
}

// Authenticate exchanges a consumer key/secret for a Daraja OAuth
// token. The result is NOT cached automatically — callers that want
// caching go through AuthenticateForPaybill. Authenticate is the
// lowest-level primitive so tests + the /test-auth endpoint can
// exercise the round-trip without polluting the cache.
func (c *Client) Authenticate(ctx context.Context, consumerKey, consumerSecret string) (Token, error) {
	if consumerKey == "" || consumerSecret == "" {
		return Token{}, errors.New("daraja: empty consumer key or secret")
	}
	endpoint := c.BaseURL + "/oauth/v1/generate?" + url.Values{
		"grant_type": {"client_credentials"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Token{}, fmt.Errorf("build oauth request: %w", err)
	}
	// Daraja accepts the consumer credentials via HTTP Basic.
	credPair := consumerKey + ":" + consumerSecret
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credPair)))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("oauth request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("daraja oauth: status %d body %s", resp.StatusCode, string(body))
	}
	var raw struct {
		AccessToken string `json:"access_token"`
		// Daraja returns expires_in as a string-encoded number in
		// some environments and as a real int in others; accept both.
		ExpiresIn json.Number `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Token{}, fmt.Errorf("decode oauth: %w (body=%s)", err, string(body))
	}
	if raw.AccessToken == "" {
		return Token{}, fmt.Errorf("daraja oauth: empty access_token (body=%s)", string(body))
	}
	expiresIn, err := raw.ExpiresIn.Int64()
	if err != nil || expiresIn <= 0 {
		// Daraja's published TTL is always 3599s; fall back to that
		// when the response is missing or unparseable so we still
		// cache something useful rather than skip the cache.
		expiresIn = 3599
	}
	return Token{
		AccessToken: raw.AccessToken,
		ExpiresAt:   time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, nil
}

// AuthenticateForPaybill returns a cached token when one is still
// fresh, or fetches + caches a new one. Cache freshness applies a
// 60-second safety margin to avoid races against the boundary.
func (c *Client) AuthenticateForPaybill(
	ctx context.Context,
	key CacheKey,
	consumerKey, consumerSecret string,
) (Token, error) {
	c.mu.Lock()
	if tok, ok := c.cache[key]; ok && time.Until(tok.ExpiresAt) > 60*time.Second {
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	tok, err := c.Authenticate(ctx, consumerKey, consumerSecret)
	if err != nil {
		return Token{}, err
	}
	c.mu.Lock()
	c.cache[key] = tok
	c.mu.Unlock()
	return tok, nil
}

// Invalidate drops a cache entry. Called by the credentials handler
// after a rotation so the next request fetches a fresh token instead
// of using one bound to the old secret.
func (c *Client) Invalidate(key CacheKey) {
	c.mu.Lock()
	delete(c.cache, key)
	c.mu.Unlock()
}

// pinnedPool returns nil when PinnedRootCertsPEM is empty so tests +
// the sandbox can use the system root store. Once the pin list is
// populated (phase 6) every request goes through the pinned pool.
func pinnedPool() *x509.CertPool {
	if len(PinnedRootCertsPEM) == 0 {
		return nil
	}
	pool := x509.NewCertPool()
	for _, pem := range PinnedRootCertsPEM {
		_ = pool.AppendCertsFromPEM(pem)
	}
	return pool
}
