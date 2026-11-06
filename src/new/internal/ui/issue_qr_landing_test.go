package ui

// Tests for the public landing of the QR links (/s/{code}).
//
// The landing is the only public surface of the QR card, so these tests pin
// what a scanner must and must not see: the asset facts (including the asset
// category), an honest read-only notice instead of the dead action buttons the
// base page shipped, and a full-detail link that respects authentication.

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	mi "github.com/ha1tch/minty"
)

// qrLandingTestData is a fully populated scan payload, so a missing field is a
// rendering bug and not a fixture gap.
func qrLandingTestData() QRScanData {
	return QRScanData{
		Code:           "AAAAAAAAAAAAAAAA",
		AssetID:        7,
		AssetName:      "Pump 7",
		AssetType:      "Centrifugal pump",
		Status:         "active",
		Location:       "Plant A",
		LastSeen:       "2026-01-01 10:00",
		AssignedTo:     "Ana",
		RecentActivity: []string{"qr_scan - 2026-01-01 10:00"},
	}
}

// renderQRLanding renders the page with the raw locale keys, the same way the
// test handlers run (the translator is nil there).
func renderQRLanding(t *testing.T, data QRScanData, category string, canViewDetails bool) string {
	t.Helper()
	page := issueQRScanPage(data, category, canViewDetails, func(key string, args ...any) string { return key })
	var buf bytes.Buffer
	b := &mi.Builder{}
	if err := page(b).Render(&buf); err != nil {
		t.Fatalf("could not render the landing page: %v", err)
	}
	return buf.String()
}

// TestIssueQRScanPage_ShowsFactsAndNoDeadActions covers the content of the
// read-only landing: the asset facts, the category, and no sign of the two
// buttons that used to do nothing.
func TestIssueQRScanPage_ShowsFactsAndNoDeadActions(t *testing.T) {
	body := renderQRLanding(t, qrLandingTestData(), "critico", true)

	for _, want := range []string{
		"Pump 7",
		"Centrifugal pump",
		"Plant A",
		"AAAAAAAAAAAAAAAA",
		"issues.asset.category",
		"issues.asset.category.critico",
		"qr.read_only_note",
		`href="/assets/7"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the landing must show %q, body did not contain it", want)
		}
	}

	for _, dead := range []string{"qr.check_in", "qr.report_issue", "check_circle", "report_problem"} {
		if strings.Contains(body, dead) {
			t.Errorf("the dead action button %q must not be rendered", dead)
		}
	}
}

// TestIssueQRScanPage_AnonymousGetsSignInLink covers the broken detail link:
// the asset page is authenticated, so an anonymous scanner gets a sign-in link
// that returns to the asset instead of a link that bounces to the login form.
func TestIssueQRScanPage_AnonymousGetsSignInLink(t *testing.T) {
	body := renderQRLanding(t, qrLandingTestData(), "normal", false)

	if !strings.Contains(body, `href="/login?next=/assets/7"`) {
		t.Error("an anonymous scanner must get a sign-in link back to the asset")
	}
	if !strings.Contains(body, "qr.sign_in_for_details") {
		t.Error("the sign-in link must be labelled for the scanner")
	}
	if strings.Contains(body, `href="/assets/7"`) {
		t.Error("an anonymous scanner must not be sent to the authenticated asset page")
	}
}

// TestQRScanLanding_RendersReadOnlyForAnonymous drives the real handler: a live
// new-style code resolves to its asset and the public page is the read-only
// landing, with the category resolved from the stored criticality ("Alta").
func TestQRScanLanding_RendersReadOnlyForAnonymous(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"id": 5, "name": "Pump 5", "type_name": "Pump",
				"status": "active", "location": "Plant A", "criticality": "Alta",
			})
		},
		"POST /api/v1/events": func(w http.ResponseWriter, r *http.Request) {
			oqlCreated(w, 1)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			switch {
			case strings.Contains(q, "FROM issue_qr_codes"):
				qrTestRows(w, []map[string]any{{
					"id": float64(1), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5), "active": true,
				}})
			default:
				qrTestRows(w, nil)
			}
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/AAAAAAAAAAAAAAAA", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("a live code must render the landing, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Pump 5") {
		t.Error("the landing must show the asset name")
	}
	if !strings.Contains(body, "qr.read_only_note") {
		t.Error("the landing must explain that the page is read-only")
	}
	if !strings.Contains(body, "issues.asset.category.critico") {
		t.Error("Alta must show up as the critical asset category")
	}
	if !strings.Contains(body, `href="/login?next=/assets/5"`) {
		t.Error("the anonymous landing must offer the sign-in link")
	}
	if strings.Contains(body, "qr.check_in") || strings.Contains(body, "qr.report_issue") {
		t.Error("the dead action buttons must not reach the public page")
	}
}

// TestQRScanLanding_RevokedCodeReturnsGone wires the revoked-code contract
// through the handler: the page explains the code was revoked instead of
// looking like a broken link (410, not 404).
func TestQRScanLanding_RevokedCodeReturnsGone(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(2), "code": "BBBBBBBBBBBBBBBB", "asset_id": float64(6), "active": false,
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/BBBBBBBBBBBBBBBB", "")

	if rec.Code != http.StatusGone {
		t.Fatalf("a revoked code must answer 410, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qr.revoked_title") {
		t.Error("the revoked page must explain what happened")
	}
}

// TestQRScanLanding_LegacyCodeStillWorks keeps the fallback honest: codes in
// the old qr_codes table keep opening the landing after the new table is
// consulted first.
func TestQRScanLanding_LegacyCodeStillWorks(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/9": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 9, "name": "Legacy asset", "criticality": "Baja"})
		},
		"POST /api/v1/events": func(w http.ResponseWriter, r *http.Request) {
			oqlCreated(w, 1)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if strings.Contains(q, "FROM qr_codes") {
				qrTestRows(w, []map[string]any{{"id": float64(3), "code": "QR-LEGACY1", "asset_id": float64(9)}})
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/QR-LEGACY1", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("a legacy code must keep working, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Legacy asset") {
		t.Error("the legacy landing must show the asset name")
	}
	if !strings.Contains(body, "issues.asset.category.normal") {
		t.Error("Baja must show up as the normal asset category")
	}
}
