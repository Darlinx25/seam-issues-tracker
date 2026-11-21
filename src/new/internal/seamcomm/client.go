// Copyright (c) 2026 haitch <h@ual.li>
// Proprietary and confidential. All rights reserved.

// Package client is a Go client for the Seam Comms outbound API. It depends on
// the standard library only.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// clientVersion identifies this vendored client in User-Agent headers. The
// upstream service keeps its version in a separate module, which cannot be
// imported here without adding a dependency on an unpublished module.
const clientVersion = "0.3.0-vendored"

// ErrIdempotencyKeyRequired is returned by Send when no key was given. A key is
// what makes a retry safe, so it is never optional.
var ErrIdempotencyKeyRequired = errors.New("client: an idempotency key is required to send")

// NewIdempotencyKey returns a random key. Prefer a key derived from the cause
// of the notification (for example rule id plus event id) when there is one: a
// retried cause then cannot send twice even if the process restarted.
func NewIdempotencyKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("client: no randomness available: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Error is an error response from the API.
type Error struct {
	Status     int
	Type       string
	Title      string
	Code       string
	Detail     string
	RequestID  string
	Errors     []FieldError
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "seam-comm: %d %s", e.Status, e.Code)
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	for _, f := range e.Errors {
		fmt.Fprintf(&b, "; %s %s", f.Path, f.Code)
	}
	if e.RequestID != "" {
		b.WriteString(" (request " + e.RequestID + ")")
	}
	return b.String()
}

// Temporary reports whether repeating the same request may succeed.
func (e *Error) Temporary() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == http.StatusBadGateway ||
		e.Status == http.StatusServiceUnavailable || e.Status == http.StatusGatewayTimeout
}

// IsCode reports whether err is an API error with the given code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// IsFeatureUnavailable reports whether the deployment does not provide
// something the request used.
func IsFeatureUnavailable(err error) bool { return IsCode(err, "feature_unavailable") }

// IsNotFound reports a 404.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Client talks to one Seam Comms deployment as one tenant.
type Client struct {
	base       string
	key        string
	http       *http.Client
	maxRetries int
	backoff    time.Duration
	userAgent  string
}

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithRetry sets how often a request is repeated after a network error or a
// 429, 502, 503 or 504 response, and the first wait. Sending is retried only
// because it carries an idempotency key. Zero disables retries.
func WithRetry(max int, base time.Duration) Option {
	return func(c *Client) { c.maxRetries, c.backoff = max, base }
}

// New returns a client. baseURL is the service root, for example
// http://localhost:8090.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		base:       strings.TrimRight(baseURL, "/"),
		key:        apiKey,
		http:       &http.Client{Timeout: 30 * time.Second},
		maxRetries: 2,
		backoff:    200 * time.Millisecond,
		userAgent:  "seam-comm-client/" + clientVersion,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

const maxResponseBytes = 10 << 20

type problemBody struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Status    int          `json:"status"`
	Code      string       `json:"code"`
	Detail    string       `json:"detail"`
	RequestID string       `json:"request_id"`
	Errors    []FieldError `json:"errors"`
}

func parseError(resp *http.Response, body []byte) *Error {
	e := &Error{Status: resp.StatusCode, RequestID: resp.Header.Get("X-Request-Id")}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if s, err := strconv.Atoi(ra); err == nil && s >= 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
	}
	var p problemBody
	if json.Unmarshal(body, &p) == nil && p.Code != "" {
		e.Type, e.Title, e.Code, e.Detail, e.Errors = p.Type, p.Title, p.Code, p.Detail, p.Errors
		if p.RequestID != "" {
			e.RequestID = p.RequestID
		}
		return e
	}
	// Not a problem document: a proxy or an older server.
	e.Code = "http_" + strconv.Itoa(resp.StatusCode)
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	e.Detail = snippet
	return e
}

func retryable(err error, status int) bool {
	if err != nil {
		return true
	}
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends a request and decodes a 2xx JSON answer into out. retry says whether
// a repeat is safe. It returns the final response headers.
func (c *Client) do(ctx context.Context, method, path string, headers map[string]string, body []byte, retry bool, out any) (http.Header, int, error) {
	attempts := 1
	if retry {
		attempts += c.maxRetries
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.http.Do(req)
		var raw []byte
		status := 0
		var hdr http.Header
		if err == nil {
			raw, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
			status, hdr = resp.StatusCode, resp.Header
		}
		if err == nil && status >= 200 && status < 300 {
			if out != nil && len(raw) > 0 {
				if err := json.Unmarshal(raw, out); err != nil {
					return hdr, status, fmt.Errorf("seam-comm: unreadable response: %w", err)
				}
			}
			return hdr, status, nil
		}

		var wait time.Duration
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			lastErr = err
		} else {
			e := parseError(&http.Response{StatusCode: status, Header: hdr}, raw)
			lastErr, wait = e, e.RetryAfter
		}
		if !retryable(err, status) || i == attempts-1 {
			break
		}
		if wait == 0 {
			wait = c.backoff << i
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, 0, err
		}
	}
	return nil, 0, lastErr
}

// Send queues a notification. The idempotency key makes repeating the call
// safe: the same key with the same request returns the original answer with
// Replayed set.
func (c *Client) Send(ctx context.Context, idempotencyKey string, req Request) (*Accepted, error) {
	if idempotencyKey == "" {
		return nil, ErrIdempotencyKeyRequired
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out Accepted
	hdr, _, err := c.do(ctx, http.MethodPost, "/v1/notifications", map[string]string{"Idempotency-Key": idempotencyKey}, body, true, &out)
	if err != nil {
		return nil, err
	}
	out.Location = hdr.Get("Location")
	out.Replayed = hdr.Get("Idempotent-Replayed") == "true"
	return &out, nil
}

// Validate checks a request and returns the delivery plan without sending.
func (c *Client) Validate(ctx context.Context, req Request) (*ValidationResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var out ValidationResult
	if _, _, err := c.do(ctx, http.MethodPost, "/v1/notifications:validate", nil, body, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get returns a notification and its deliveries.
func (c *Client) Get(ctx context.Context, id string) (*Notification, error) {
	var out Notification
	if _, _, err := c.do(ctx, http.MethodGet, "/v1/notifications/"+url.PathEscape(id), nil, nil, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// List returns one page of notifications, newest first.
func (c *Client) List(ctx context.Context, o ListOptions) (*Page, error) {
	q := url.Values{}
	if len(o.Status) > 0 {
		q.Set("status", strings.Join(o.Status, ","))
	}
	for k, v := range map[string]string{
		"category": o.Category, "severity": o.Severity, "channel": o.Channel, "delivery_status": o.DeliveryStatus,
		"reference_type": o.ReferenceType, "reference_id": o.ReferenceID, "cursor": o.Cursor,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if !o.CreatedAfter.IsZero() {
		q.Set("created_after", o.CreatedAfter.UTC().Format(time.RFC3339))
	}
	if !o.CreatedBefore.IsZero() {
		q.Set("created_before", o.CreatedBefore.UTC().Format(time.RFC3339))
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	path := "/v1/notifications"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out Page
	if _, _, err := c.do(ctx, http.MethodGet, path, nil, nil, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Channels returns channel capabilities and limits.
func (c *Client) Channels(ctx context.Context) (*Channels, error) {
	var out Channels
	if _, _, err := c.do(ctx, http.MethodGet, "/v1/channels", nil, nil, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Categories returns the category registry.
func (c *Client) Categories(ctx context.Context) ([]Category, error) {
	var out struct {
		Categories []Category `json:"categories"`
	}
	if _, _, err := c.do(ctx, http.MethodGet, "/v1/categories", nil, nil, true, &out); err != nil {
		return nil, err
	}
	return out.Categories, nil
}

// WaitTerminal polls until the notification reaches a final status or ctx ends.
// It is meant for tests and scripts; production code should not block on
// delivery.
func (c *Client) WaitTerminal(ctx context.Context, id string, every time.Duration) (*Notification, error) {
	if every <= 0 {
		every = time.Second
	}
	for {
		n, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if n.Terminal() {
			return n, nil
		}
		if err := sleep(ctx, every); err != nil {
			return n, err
		}
	}
}
