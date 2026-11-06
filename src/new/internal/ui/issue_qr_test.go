package ui

// Tests for the per-asset QR codes (card "Generar y gestionar cÃ³digos QR por
// activo"). The handlers are exercised end to end against a mocked xolu, so the
// assertions cover the contract that matters: one active code per asset, the
// category copied at generation time, revocation never deleting a row, and the
// asset permission gating every write.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/ha1tch/seam-ui/internal/auth"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// â”€â”€ Helpers â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// qrTestJSON writes a JSON document, the shape xolu.Get expects.
func qrTestJSON(w http.ResponseWriter, doc map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(doc)
}

// qrTestRows writes an OQL result with the given rows.
func qrTestRows(w http.ResponseWriter, rows []map[string]any) {
	if rows == nil {
		rows = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "completed",
		"data":   rows,
		"stats":  map[string]any{"rows_returned": len(rows)},
	})
}

// qrTestQuery decodes the OQL request body. The handlers build queries by
// concatenation, so tests switch on the query text instead of the URL.
func qrTestQuery(t *testing.T, r *http.Request) string {
	t.Helper()
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("could not decode the OQL request: %v", err)
	}
	return body.Query
}

// qrTestDecodeBody decodes a JSON request body into a map.
func qrTestDecodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		t.Fatalf("could not decode the request body: %v", err)
	}
	return doc
}

// qrTestRequest attaches the claims the auth middleware would set, so the
// handlers can resolve the role the same way they do in production.
func qrTestRequest(t *testing.T, method, target, role string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	return req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 1, TenantID: 1, Role: role}))
}

// qrTestPost runs a handler as the given role.
func qrTestPost(t *testing.T, h http.HandlerFunc, target, role string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, qrTestRequest(t, http.MethodPost, target, role))
	return rec
}

// qrTestGet runs a handler as the given role.
func qrTestGet(t *testing.T, h http.HandlerFunc, target, role string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, qrTestRequest(t, http.MethodGet, target, role))
	return rec
}

// qrTestStoredRow builds the OQL row the list query would return for a code
// that was just written, so the card rendered after a mutation shows it.
func qrTestStoredRow(id int64, created map[string]any, active bool, revokedAt string) map[string]any {
	row := map[string]any{
		"id":         float64(id),
		"code":       created["code"],
		"asset_id":   created["asset_id"],
		"active":     active,
		"created_at": created["created_at"],
		"created_by": created["created_by"],
	}
	if _, ok := created["asset_category"]; ok {
		row["asset_category"] = created["asset_category"]
	}
	if revokedAt != "" {
		row["revoked_at"] = revokedAt
	}
	return row
}

// â”€â”€ Generation â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// TestIssueQRCodeGenerate_CopiesAssetCategoryAndStoresCode covers the happy
// path: the code is stored as the asset's active one, the category is copied
// from the asset ("Alta" in xolu -> "critico"), and the card offers
// regeneration instead of generation.
func TestIssueQRCodeGenerate_CopiesAssetCategoryAndStoresCode(t *testing.T) {
	var created map[string]any
	createdCode := ""

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5, "name": "Pump", "criticality": "Alta"})
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			createdCode = toString(created["code"])
			oqlCreated(w, 42)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if strings.Contains(q, "SELECT *") && strings.Contains(q, "FROM issue_qr_codes") && createdCode != "" {
				qrTestRows(w, []map[string]any{qrTestStoredRow(42, created, true, "")})
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestPost(t, h.AssetQRCodeGenerate, "/assets/5/qr/generate", auth.RoleAdmin)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for the card fragment, got %d", rec.Code)
	}
	if created == nil {
		t.Fatal("no code was created")
	}
	if got := created["asset_category"]; got != "critico" {
		t.Errorf("the category must be copied from the asset: want critico, got %v (criticality Alta)", got)
	}
	if created["active"] != true {
		t.Errorf("a new code must be active, got %v", created["active"])
	}
	if got := toInt64(created["created_by"]); got != 1 {
		t.Errorf("created_by must be the operator, want 1, got %d", got)
	}
	if !validIssueQRCode(createdCode) {
		t.Errorf("the code must be 16 base32 chars, got %q", createdCode)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/s/"+createdCode) {
		t.Errorf("the card must show the /s/{code} link, body did not contain it")
	}
	if !strings.Contains(body, "issue_qr.regenerate") || !strings.Contains(body, "issue_qr.revoke") {
		t.Errorf("an active code must offer regenerate and revoke")
	}
	if strings.Contains(body, "issue_qr.generate") {
		t.Errorf("an asset with a live code must not offer plain generation")
	}
}

// TestIssueQRCodeGenerate_RefusedWhenOneIsActive enforces one live code per
// asset: a second generation is refused and the card points at regeneration.
func TestIssueQRCodeGenerate_RefusedWhenOneIsActive(t *testing.T) {
	writeAttempted := false

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5, "criticality": "Baja"})
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			writeAttempted = true
			oqlCreated(w, 43)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(11), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
				"asset_category": "normal", "active": true, "created_at": "2026-01-01T00:00:00Z",
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestPost(t, h.AssetQRCodeGenerate, "/assets/5/qr/generate", auth.RoleAdmin)

	if rec.Code != http.StatusOK {
		t.Fatalf("the refusal must re-render the card, got %d", rec.Code)
	}
	if writeAttempted {
		t.Error("a second code must not be created while one is active")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "issue_qr.error.already_active") {
		t.Errorf("the card must explain that an active code already exists")
	}
	if !strings.Contains(body, "/s/AAAAAAAAAAAAAAAA") {
		t.Errorf("the existing code must still be shown")
	}
}

// TestIssueQRCodeGenerate_UniqueCode enforces the uniqueness requirement: a
// candidate that already exists in either table is discarded and a new one is
// drawn, so two assets can never share a printed link.
func TestIssueQRCodeGenerate_UniqueCode(t *testing.T) {
	existenceChecks := 0
	var created map[string]any

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5, "criticality": "Media"})
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 44)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if strings.Contains(q, "SELECT TOP 1 id") {
				existenceChecks++
				if existenceChecks == 1 {
					// Simulate the (practically impossible) collision: the
					// first candidate is already taken by qr_codes.
					qrTestRows(w, []map[string]any{{"id": float64(1)}})
					return
				}
				qrTestRows(w, nil)
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestPost(t, h.AssetQRCodeGenerate, "/assets/5/qr/generate", auth.RoleAdmin)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if created == nil {
		t.Fatal("the retry must end up creating a code")
	}
	if existenceChecks < 2 {
		t.Errorf("a colliding code must be discarded: want at least 2 uniqueness checks, got %d", existenceChecks)
	}
	if got := created["asset_category"]; got != "normal" {
		t.Errorf("Media must map to normal, got %v", got)
	}
}

// â”€â”€ Regeneration and revocation â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// TestIssueQRCodeRegenerate_RevokesThenCreates checks the replacement order:
// the old row is deactivated (never deleted) and a fresh code takes its place,
// so a printed QR of the old code stops working.
func TestIssueQRCodeRegenerate_RevokesThenCreates(t *testing.T) {
	revoked := false
	var patched map[string]any
	patchedID := int64(0)
	var created map[string]any
	createdCode := ""
	createCount := 0

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5, "criticality": "Alta"})
		},
		"PATCH /api/v1/issue_qr_codes/": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			parts := strings.Split(r.URL.Path, "/")
			patchedID, _ = strconv.ParseInt(parts[len(parts)-1], 10, 64)
			revoked = true
			w.WriteHeader(http.StatusOK)
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			createCount++
			created = qrTestDecodeBody(t, r)
			createdCode = toString(created["code"])
			oqlCreated(w, 45)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if strings.Contains(q, "SELECT *") && strings.Contains(q, "FROM issue_qr_codes") {
				if revoked && createdCode != "" {
					qrTestRows(w, []map[string]any{qrTestStoredRow(45, created, true, "")})
					return
				}
				qrTestRows(w, []map[string]any{{
					"id": float64(11), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
					"asset_category": "critico", "active": true, "created_at": "2026-01-01T00:00:00Z",
				}})
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestPost(t, h.AssetQRCodeRegenerate, "/assets/5/qr/regenerate", auth.RoleAdmin)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if patched == nil || patchedID != 11 {
		t.Fatalf("the old code must be revoked first, patched=%v id=%d", patched, patchedID)
	}
	if patched["active"] != false {
		t.Errorf("revoking must set active=false, got %v", patched["active"])
	}
	if toString(patched["revoked_at"]) == "" {
		t.Errorf("revoking must stamp revoked_at")
	}
	if createCount != 1 || created == nil {
		t.Fatalf("exactly one replacement code must be created, got %d", createCount)
	}
	if createdCode == "AAAAAAAAAAAAAAAA" {
		t.Error("the replacement code must be new")
	}
	if !strings.Contains(rec.Body.String(), "/s/"+createdCode) {
		t.Error("the card must show the new link")
	}
}

// TestIssueQRCodeRevoke_DeactivatesWithoutReplacing checks that revoking leaves
// the asset with no live code: the row is kept for the record and the card
// offers generating a new one.
func TestIssueQRCodeRevoke_DeactivatesWithoutReplacing(t *testing.T) {
	revoked := false
	patched := map[string]any{}
	patchedID := int64(0)
	createCount := 0

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5, "criticality": "Media"})
		},
		"PATCH /api/v1/issue_qr_codes/": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			parts := strings.Split(r.URL.Path, "/")
			patchedID, _ = strconv.ParseInt(parts[len(parts)-1], 10, 64)
			revoked = true
			w.WriteHeader(http.StatusOK)
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			createCount++
			oqlCreated(w, 46)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			row := map[string]any{
				"id": float64(11), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
				"asset_category": "normal", "active": !revoked,
				"created_at": "2026-01-01T00:00:00Z",
			}
			if revoked {
				row["revoked_at"] = "2026-02-02T00:00:00Z"
			}
			qrTestRows(w, []map[string]any{row})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestPost(t, h.AssetQRCodeRevoke, "/assets/5/qr/revoke", auth.RoleAdmin)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if patchedID != 11 || patched["active"] != false {
		t.Fatalf("the live code must be deactivated, got id=%d patch=%v", patchedID, patched)
	}
	if createCount != 0 {
		t.Error("revoking must not create a replacement code")
	}
	body := rec.Body.String()
	if strings.Contains(body, "/s/AAAAAAAAAAAAAAAA") {
		t.Error("a revoked link must no longer be offered")
	}
	if !strings.Contains(body, "issue_qr.revoked_note") {
		t.Errorf("the card must say the previous code was revoked")
	}
	if !strings.Contains(body, "issue_qr.generate") {
		t.Errorf("an asset without a live code must offer generation")
	}
}

// TestIssueQRCodeRevoke_RefusedWhenNoneIsActive keeps the mutation honest: with
// no live code there is nothing to revoke.
func TestIssueQRCodeRevoke_RefusedWhenNoneIsActive(t *testing.T) {
	patchAttempted := false

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
		"PATCH /api/v1/issue_qr_codes/": func(w http.ResponseWriter, r *http.Request) {
			patchAttempted = true
			w.WriteHeader(http.StatusOK)
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestPost(t, h.AssetQRCodeRevoke, "/assets/5/qr/revoke", auth.RoleAdmin)

	if patchAttempted {
		t.Error("nothing must be revoked when the asset has no live code")
	}
	if !strings.Contains(rec.Body.String(), "issue_qr.error.none_active") {
		t.Errorf("the card must say there is no active code")
	}
}

// â”€â”€ Permissions â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// TestIssueQRCodeGenerate_ForbiddenForViewer covers the write gate: viewers
// may read the asset but may not mint or revoke its codes.
func TestIssueQRCodeGenerate_ForbiddenForViewer(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	for _, tc := range []struct {
		name   string
		handle http.HandlerFunc
		target string
	}{
		{"generate", h.AssetQRCodeGenerate, "/assets/5/qr/generate"},
		{"regenerate", h.AssetQRCodeRegenerate, "/assets/5/qr/regenerate"},
		{"revoke", h.AssetQRCodeRevoke, "/assets/5/qr/revoke"},
	} {
		rec := qrTestPost(t, tc.handle, tc.target, auth.RoleViewer)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: a viewer must get 403, got %d", tc.name, rec.Code)
		}
	}
}

// TestAssetQRCodeCard_ViewerSeesReadOnlyCard covers the read gate: a viewer
// gets the card with the link but no management buttons.
func TestAssetQRCodeCard_ViewerSeesReadOnlyCard(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(11), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
				"asset_category": "normal", "active": true, "created_at": "2026-01-01T00:00:00Z",
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.AssetQRCodeCard, "/assets/5/qr/card", auth.RoleViewer)

	if rec.Code != http.StatusOK {
		t.Fatalf("a viewer must be able to read the card, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/s/AAAAAAAAAAAAAAAA") {
		t.Errorf("the card must show the link to a viewer")
	}
	if !strings.Contains(body, "issue_qr.copy") {
		t.Errorf("copying the link is a read action and must be offered")
	}
	if strings.Contains(body, "issue_qr.regenerate") || strings.Contains(body, "issue_qr.revoke") {
		t.Errorf("a viewer must not be offered regenerate or revoke")
	}
}

// â”€â”€ Public link resolution â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// TestResolveScanCode covers the three shapes a scanned code can have: a live
// new code, a revoked new code (explained, not 404) and a legacy qr_codes row.
func TestResolveScanCode(t *testing.T) {
	live := "AAAAAAAAAAAAAAAA"
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			switch {
			case strings.Contains(q, "FROM issue_qr_codes"):
				if strings.Contains(q, "WHERE code = '"+live+"'") {
					qrTestRows(w, []map[string]any{{
						"id": float64(1), "code": live, "asset_id": float64(5), "active": true,
					}})
					return
				}
				if strings.Contains(q, "'BBBBBBBBBBBBBBBB'") {
					qrTestRows(w, []map[string]any{{
						"id": float64(2), "code": "BBBBBBBBBBBBBBBB", "asset_id": float64(6), "active": false,
					}})
					return
				}
				qrTestRows(w, nil)
			case strings.Contains(q, "FROM qr_codes"):
				if strings.Contains(q, "'QR-LEGACY1'") {
					qrTestRows(w, []map[string]any{{"id": float64(3), "code": "QR-LEGACY1", "asset_id": float64(7)}})
					return
				}
				qrTestRows(w, nil)
			default:
				qrTestRows(w, nil)
			}
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	ctx := context.Background()

	if id, found, revoked := h.resolveScanCode(ctx, live); id != 5 || !found || revoked {
		t.Errorf("a live code must resolve to its asset: id=%d found=%v revoked=%v", id, found, revoked)
	}
	if _, found, revoked := h.resolveScanCode(ctx, "BBBBBBBBBBBBBBBB"); !found || !revoked {
		t.Errorf("a revoked code must be reported as revoked: found=%v revoked=%v", found, revoked)
	}
	if id, found, revoked := h.resolveScanCode(ctx, "QR-LEGACY1"); id != 7 || !found || revoked {
		t.Errorf("a legacy qr_codes row must keep working: id=%d found=%v revoked=%v", id, found, revoked)
	}
	if _, found, _ := h.resolveScanCode(ctx, "ZZZZZZZZZZZZZZZZ"); found {
		t.Error("an unknown code must not be found")
	}
}

// TestIssueQRCodeCard_CarriesCSRFToken covers a real failure seen in the app:
// the layout's csrf-token meta tag is empty on the asset detail page, so htmx
// had no token to attach and every mutation from the card came back 403, which
// the front end reports as "An error occurred. Please try again." The fragment
// therefore carries the token itself: the generate form as a hidden field and
// the regenerate/revoke handlers as an explicit header.
func TestIssueQRCodeCard_CarriesCSRFToken(t *testing.T) {
	const activeCode = "AAAAAAAAAAAAAAAA"
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{})
		},
	})
	defer server.Close()

	// The handler needs a real CSRF manager: the token is the thing under test.
	cfg := Config{AppName: "Test", Version: "0.0.0"}
	h := NewUIHandler(xolu.New(server.URL), cfg, nil, nil, nil, testCSRFManager(), nil, nil, nil)

	rec := qrTestGet(t, h.AssetQRCodeCard, "/assets/5/qr/card", auth.RoleAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()

	token := ""
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CSRFCookieName {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("the card must issue a CSRF cookie so the token below is real")
	}
	if !strings.Contains(body, `name="`+auth.CSRFFormField+`"`) {
		t.Fatalf("the generate form must carry the CSRF field, got:\n%s", body)
	}
	if !strings.Contains(body, `value="`+token+`"`) {
		t.Errorf("the CSRF field must hold the session token")
	}

	// With a live code the card offers regenerate and revoke, which post
	// through htmx.ajax and therefore need the token in the request options.
	serverWithCode := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(11), "code": activeCode, "asset_id": float64(5),
				"asset_category": "normal", "active": true, "created_at": "2026-01-01T00:00:00Z",
			}})
		},
	})
	defer serverWithCode.Close()

	h2 := NewUIHandler(xolu.New(serverWithCode.URL), cfg, nil, nil, nil, testCSRFManager(), nil, nil, nil)
	rec2 := qrTestGet(t, h2.AssetQRCodeCard, "/assets/5/qr/card", auth.RoleAdmin)
	if rec2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec2.Code)
	}
	body2 := rec2.Body.String()
	// The markup is HTML-escaped, so the header shows up as
	// headers:{&#39;X-CSRF-Token&#39;:&#39;...&#39;}
	if !strings.Contains(body2, "headers:{") {
		t.Fatalf("regenerate and revoke must pass htmx.ajax options with headers, got:\n%s", body2)
	}
	if !strings.Contains(body2, auth.CSRFHeaderName) {
		t.Errorf("regenerate and revoke must send the CSRF token as a header, got:\n%s", body2)
	}
	if strings.Contains(body2, auth.CSRFHeaderName+"&#39;:&#39;&#39;") {
		t.Error("the header must never be rendered empty")
	}
}

// TestIssueQRCodeProperties pins the code format the link depends on: 16
// characters, uppercase base32 only, and stable across calls.
func TestIssueQRCodeProperties(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		code, err := newIssueQRCode()
		if err != nil {
			t.Fatalf("generating a code failed: %v", err)
		}
		if len(code) != 16 {
			t.Fatalf("want 16 chars, got %d (%q)", len(code), code)
		}
		if !validIssueQRCode(code) {
			t.Fatalf("generated code is not valid base32: %q", code)
		}
		if seen[code] {
			t.Fatalf("code %q was generated twice", code)
		}
		seen[code] = true
	}
	if validIssueQRCode("QR-LEGACY1") {
		t.Error("a legacy style code must not be mistaken for a new one")
	}
}
