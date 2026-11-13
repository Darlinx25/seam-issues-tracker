package ui

// Tests for the public tracking page (/track/{token}) and for the two moments
// that write the expiry clock: resolve/reject starts it, close must not extend
// it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ha1tch/seam-ui/internal/auth"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// issueTrackTestToken is a valid 26-char base32 token.
const issueTrackTestToken = "ABCDEFGHIJKLMNOPQRSTUVWXY2"

func TestIssueTrackingToken_IsOpaqueAndValid(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		token, err := newIssueTrackingToken()
		if err != nil {
			t.Fatalf("could not generate a token: %v", err)
		}
		if len(token) != issueTrackTokenLength {
			t.Fatalf("token length = %d, want %d", len(token), issueTrackTokenLength)
		}
		if !validIssueTrackingToken(token) {
			t.Fatalf("generated token %q is not valid", token)
		}
		if seen[token] {
			t.Fatalf("generated a duplicate token %q", token)
		}
		seen[token] = true
	}

	for _, bad := range []string{
		"",
		"SHORT",
		"ABCDEFGHIJKLMNOPQRSTUVWXY",
		"ABCDEFGHIJKLMNOPQRSTUVWXY22",
		"ABCDEFGHIJKLMNOPQRSTUVWXY0",
		"abcdefghijklmnopqrstuvwxy2",
	} {
		if validIssueTrackingToken(bad) {
			t.Errorf("%q must not be a valid token", bad)
		}
	}
}

func TestIssueTrackExpiry_StartsOnClosingStates(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	want := now.Add(issueTrackExpiryAfter).Format(time.RFC3339)

	for _, state := range []string{"resolved", "closed", "rejected", "duplicate"} {
		if got := issueTrackExpiry(state, now); got != want {
			t.Errorf("%s: expiry = %q, want %q", state, got, want)
		}
	}
	for _, state := range []string{"reported", "triaged", "in_progress", "on_hold", ""} {
		if got := issueTrackExpiry(state, now); got != "" {
			t.Errorf("%s: expiry = %q, want empty", state, got)
		}
	}
}

func TestIssueTrackExpired(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

	if issueTrackExpired("", now) {
		t.Error("an issue without deadline must not be expired")
	}
	if !issueTrackExpired(now.Add(-time.Second).Format(time.RFC3339), now) {
		t.Error("a past deadline must be expired")
	}
	if issueTrackExpired(now.Add(time.Second).Format(time.RFC3339), now) {
		t.Error("a future deadline must not be expired")
	}
	if issueTrackExpired("not-a-date", now) {
		t.Error("a malformed deadline must fail open, not closed")
	}
}

func TestIssueTrackProgressIndex(t *testing.T) {
	cases := map[string]int{
		"reported":    0,
		"triaged":     1,
		"in_progress": 2,
		"on_hold":     2,
		"resolved":    3,
		"closed":      4,
		"rejected":    -1,
		"duplicate":   -1,
		"":            0,
	}
	for state, want := range cases {
		if got := issueTrackProgressIndex(state); got != want {
			t.Errorf("%q: index = %d, want %d", state, got, want)
		}
	}
}

func TestIssueTrackingPublicLink(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"", "/track/" + issueTrackTestToken},
		{"https://seam.example", "https://seam.example/track/" + issueTrackTestToken},
		{"https://seam.example/", "https://seam.example/track/" + issueTrackTestToken},
		{"https://seam.example/s", "https://seam.example/track/" + issueTrackTestToken},
		{"https://seam.example/s/", "https://seam.example/track/" + issueTrackTestToken},
	}
	for _, tc := range cases {
		if got := issueTrackingPublicLink(tc.base, issueTrackTestToken); got != tc.want {
			t.Errorf("base %q: link = %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestBuildIssueTrackActivity_FiltersUnknownInputs(t *testing.T) {
	triage := "triage"
	unknown := "custom"
	entries := []xolu.HistoryEntry{
		{To: "reported", At: "2026-11-10T09:40:00Z", Note: "machine created"},
		{Input: &triage, At: "2026-11-10T09:52:00Z"},
		{Input: &unknown, At: "2026-11-10T10:00:00Z"},
	}

	got := buildIssueTrackActivity(entries)
	if len(got) != 2 {
		t.Fatalf("activity length = %d, want 2", len(got))
	}
	if got[0].Action != "created" || got[1].Action != "triage" {
		t.Errorf("activity = %#v, want created + triage", got)
	}
}

func TestIssueTrackCanonicalID_ReadsTheLastMerge(t *testing.T) {
	entries := []xolu.HistoryEntry{
		{Payload: json.RawMessage(`{"canonical_id":42}`)},
		{Payload: json.RawMessage(`{"canonical_id":77}`)},
		{Payload: json.RawMessage(`not json`)},
	}
	if got := issueTrackCanonicalID(entries); got != 77 {
		t.Errorf("canonical id = %d, want 77", got)
	}
	if got := issueTrackCanonicalID(nil); got != 0 {
		t.Errorf("canonical id of empty history = %d, want 0", got)
	}
}

func TestIssueTrack_RendersProgressActivityAndLink(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if strings.Contains(q, "FROM issues") {
				qrTestRows(w, []map[string]any{{
					"id":          float64(77),
					"title":       "Oil leak",
					"state":       "in_progress",
					"machine_id":  float64(7),
					"track_token": issueTrackTestToken,
					"created_at":  "2026-11-10T09:40:00Z",
				}})
				return
			}
			qrTestRows(w, nil)
		},
		"GET /api/v2/fsm/machine/7/history": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"machine": float64(7),
				"entries": []map[string]any{
					{"to": "reported", "at": "2026-11-10T09:40:00Z", "note": "machine created"},
					{"from": "reported", "to": "in_progress", "input": "start", "at": "2026-11-10T10:05:00Z"},
				},
			})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("GET", "/track/"+issueTrackTestToken+"?new=1", nil)
	req.SetPathValue("token", issueTrackTestToken)
	rec := httptest.NewRecorder()
	h.IssueTrack(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("tracking page must render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"qr.track.new_title",
		"#77",
		"qr.track.step.in_progress",
		"qr.track.activity.start",
		issueTrackTestToken,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tracking page must contain %q", want)
		}
	}
}

func TestIssueTrack_ExpiredLinkReturnsGone(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id":               float64(77),
				"title":            "Oil leak",
				"state":            "closed",
				"track_token":      issueTrackTestToken,
				"track_expires_at": "2026-01-01T00:00:00Z",
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("GET", "/track/"+issueTrackTestToken, nil)
	req.SetPathValue("token", issueTrackTestToken)
	rec := httptest.NewRecorder()
	h.IssueTrack(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("expired link must answer 410, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qr.track.error.expired_title") {
		t.Error("the expired page must explain the expiry")
	}
}

func TestIssueTrack_MalformedTokenReturnsNotFound(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("GET", "/track/short", nil)
	req.SetPathValue("token", "short")
	rec := httptest.NewRecorder()
	h.IssueTrack(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("malformed token must answer 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qr.track.error.not_found_title") {
		t.Error("the not-found page must be the tracking one")
	}
}

func TestIssueTrack_UnknownTokenReturnsNotFound(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("GET", "/track/"+issueTrackTestToken, nil)
	req.SetPathValue("token", issueTrackTestToken)
	rec := httptest.NewRecorder()
	h.IssueTrack(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token must answer 404, got %d", rec.Code)
	}
}

func TestIssueDetail_ShowsPublicTrackingLink(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"id":          float64(1),
				"title":       "Oil leak",
				"state":       "in_progress",
				"track_token": issueTrackTestToken,
			})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("GET", "/issues/1", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("detail must render, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "issues.tracking.title") {
		t.Error("the detail must show the public tracking card")
	}
	if !strings.Contains(body, issueTrackLinkPrefix+issueTrackTestToken) {
		t.Error("the detail must show the tracking URL")
	}
}

func TestIssueTransition_ResolveStartsTrackingClock(t *testing.T) {
	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"id":          float64(1),
				"title":       "Oil leak",
				"state":       "in_progress",
				"machine_id":  float64(7),
				"track_token": issueTrackTestToken,
			})
		},
		"POST /api/v2/fsm/machine/7/walk": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"previous": "in_progress",
				"current":  "resolved",
				"terminal": false,
			})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			qrTestJSON(w, map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("POST", "/issues/1/resolve", nil)
	req.SetPathValue("id", "1")
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: "operator"}))
	rec := httptest.NewRecorder()
	h.IssueResolve(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if patched["state"] != "resolved" {
		t.Fatalf("state = %v, want resolved", patched["state"])
	}
	qrReportAssertDeadline(t, "track_expires_at", toString(patched["track_expires_at"]), issueTrackExpiryAfter)
}

func TestIssueTransition_CloseDoesNotExtendTrackingClock(t *testing.T) {
	const original = "2026-11-13T09:40:00Z"

	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"id":               float64(1),
				"title":            "Oil leak",
				"state":            "resolved",
				"machine_id":       float64(7),
				"track_token":      issueTrackTestToken,
				"track_expires_at": original,
			})
		},
		"POST /api/v2/fsm/machine/7/walk": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"previous": "resolved",
				"current":  "closed",
				"terminal": true,
			})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			qrTestJSON(w, map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("POST", "/issues/1/close", nil)
	req.SetPathValue("id", "1")
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: "operator"}))
	rec := httptest.NewRecorder()
	h.IssueClose(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if patched["state"] != "closed" {
		t.Fatalf("state = %v, want closed", patched["state"])
	}
	if _, ok := patched["track_expires_at"]; ok {
		t.Errorf("close must not rewrite track_expires_at, got %v", patched["track_expires_at"])
	}
}

func TestIssueReject_StartsTrackingClock(t *testing.T) {
	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"id":          float64(1),
				"title":       "Oil leak",
				"state":       "reported",
				"machine_id":  float64(7),
				"track_token": issueTrackTestToken,
			})
		},
		"POST /api/v2/fsm/machine/7/walk": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"previous": "reported",
				"current":  "rejected",
				"terminal": true,
			})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			qrTestJSON(w, map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	req := httptest.NewRequest("POST", "/issues/1/reject",
		strings.NewReader(url.Values{"motivo": {"Not a real fault"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: "operator"}))
	rec := httptest.NewRecorder()
	h.IssueReject(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if patched["state"] != "rejected" {
		t.Fatalf("state = %v, want rejected", patched["state"])
	}
	if patched["reject_reason"] != "Not a real fault" {
		t.Errorf("reject_reason = %v, want the motivo", patched["reject_reason"])
	}
	qrReportAssertDeadline(t, "track_expires_at", toString(patched["track_expires_at"]), issueTrackExpiryAfter)
}
