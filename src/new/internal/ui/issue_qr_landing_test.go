package ui

// Tests for the public landing of the QR links (/s/{code}).
//
// The landing is the only anonymous surface of the module and it is the report
// form itself, so these tests pin what a scanner must see (the asset facts, the
// category frozen into the code, the contact block and the problem
// description), what the form must post to, and the two rules the card demands:
// no authenticated detour and no dead code that claims the code stopped working
// when it did not.

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	mi "github.com/ha1tch/minty"
)

// qrReportTestData is a fully populated form payload, so a missing field is a
// rendering bug and not a fixture gap.
func qrReportTestData() issueQRReportPageData {
	return issueQRReportPageData{
		Code:            "AAAAAAAAAAAAAAAA",
		AssetName:       "Pump 7",
		AssetType:       "Centrifugal pump",
		AssetLocation:   "Plant A",
		AssetCategory:   "critico",
		ContactName:     "Carolina",
		ContactDocument: "4.512.873-6",
		ContactPhone:    "099 123 456",
		ContactEmail:    "carolina@correo.com",
		Title:           "Oil leak",
		Description:     "Oil under the pump.",
	}
}

// renderQRReportPage renders the form with the raw locale keys, the same way
// the test handlers run (the translator is nil there).
func renderQRReportPage(t *testing.T, data issueQRReportPageData) string {
	t.Helper()
	page := issueQRReportPage(data, func(key string, args ...any) string { return key })
	var buf bytes.Buffer
	b := &mi.Builder{}
	if err := page(b).Render(&buf); err != nil {
		t.Fatalf("could not render the report form: %v", err)
	}
	return buf.String()
}

// TestIssueQRReportPage_ShowsAssetAndForm covers the card's content: the asset
// the code belongs to, the category frozen into the code, the channel badge and
// every field of the anonymous report.
func TestIssueQRReportPage_ShowsAssetAndForm(t *testing.T) {
	body := renderQRReportPage(t, qrReportTestData())

	for _, want := range []string{
		// Asset card
		"Pump 7",
		"Centrifugal pump",
		"Plant A",
		"issues.asset.category.critico",
		"issues.channel.qr",
		// The form itself
		`action="/s/AAAAAAAAAAAAAAAA/report"`,
		`method="POST"`,
		`name="contact_name"`,
		`name="contact_document"`,
		`name="contact_phone"`,
		`name="contact_email"`,
		`name="title"`,
		`name="description"`,
		"qr.report.contact_title",
		"qr.report.problem_title",
		"qr.report.submit",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the report form must show %q, body did not contain it", want)
		}
	}
	if !strings.Contains(body, `type="email"`) {
		t.Error("the email field must be an email input, so the browser checks the shape first")
	}
	if strings.Count(body, "required") < 6 {
		t.Errorf("every field of the anonymous form is required, body carried %d required attributes", strings.Count(body, "required"))
	}
}

// TestIssueQRReportPage_IsAnonymous pins the point of the channel: nothing on
// the page sends the scanner through a login or back to the in-app form.
func TestIssueQRReportPage_IsAnonymous(t *testing.T) {
	body := renderQRReportPage(t, qrReportTestData())

	for _, unwanted := range []string{
		"/issues/new",
		"/login",
		`href="#report"`,
		"qr.sign_in_for_details",
		"qr.view_full_details",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the anonymous form must not carry %q", unwanted)
		}
	}
	if !strings.Contains(body, "qr.report.anonymous_note") {
		t.Error("the page must say that no sign-in is needed")
	}
}

// TestIssueQRReportPage_PrefillsValuesAndErrors covers a rejected submission:
// the answers survive and each error lands under its own field.
func TestIssueQRReportPage_PrefillsValuesAndErrors(t *testing.T) {
	data := qrReportTestData()
	data.Title = "Oil leak"
	data.ContactEmail = "broken"
	data.Errors = map[string]string{
		"contact_email": "error.invalid_email",
		"description":   "qr.report.error.description_required",
	}
	body := renderQRReportPage(t, data)

	if !strings.Contains(body, `value="Oil leak"`) || !strings.Contains(body, `value="broken"`) {
		t.Error("a rejected report must keep what the scanner typed")
	}
	if !strings.Contains(body, "error.invalid_email") {
		t.Error("the email error must be rendered next to the field")
	}
	if !strings.Contains(body, "qr.report.error.description_required") {
		t.Error("the description error must be rendered next to the field")
	}
}

// TestIssueQRReportPage_RendersTemplateFields covers the server-rendered
// template block: PublicLayout carries no HTMX, so the fields of the template
// assigned to the asset's type arrive inside the same form.
func TestIssueQRReportPage_RendersTemplateFields(t *testing.T) {
	data := qrReportTestData()
	data.TemplateFields = func(b *mi.Builder) mi.Node {
		return b.Input(mi.Name("serial_no"), mi.ID("field-serial_no"), mi.Type("text"))
	}
	body := renderQRReportPage(t, data)

	if !strings.Contains(body, `name="serial_no"`) {
		t.Error("the template fields must be part of the anonymous form")
	}
}

// TestQRScanLanding_RendersReportForm drives the real handler: a live code
// resolves to its asset and the public page is the anonymous form.
func TestQRScanLanding_RendersReportForm(t *testing.T) {
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
			if strings.Contains(q, "FROM issue_qr_codes") {
				qrTestRows(w, []map[string]any{{
					"id": float64(1), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
					"asset_category": "critico", "active": true,
				}})
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/AAAAAAAAAAAAAAAA", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("a live code must render the form, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Pump 5", "Pump", "Plant A", "issues.asset.category.critico",
		`action="/s/AAAAAAAAAAAAAAAA/report"`,
		`name="contact_email"`, `name="description"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the landing must show %q, body did not contain it", want)
		}
	}
	for _, unwanted := range []string{"/issues/new", "/login", "qr.report_issue"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the anonymous landing must not carry %q", unwanted)
		}
	}
}

// TestQRScanLanding_CategoryComesFromTheCode is the point of the card: the
// category is the one frozen into the code, not the asset's current criticality.
func TestQRScanLanding_CategoryComesFromTheCode(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"id": 5, "name": "Pump 5", "type_name": "Pump", "status": "active",
				"location": "Plant A", "criticality": "Baja",
			})
		},
		"POST /api/v1/events": func(w http.ResponseWriter, r *http.Request) {
			oqlCreated(w, 1)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(1), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
				"asset_category": "critico", "active": true,
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	body := qrTestGet(t, h.QRScan, "/s/AAAAAAAAAAAAAAAA", "").Body.String()

	if !strings.Contains(body, "issues.asset.category.critico") {
		t.Error("the landing must show the category frozen into the code")
	}
	if strings.Contains(body, "issues.asset.category.normal") {
		t.Error("the landing must not recompute the category from the asset")
	}
}

// TestQRScanLanding_RendersTemplateFields covers the server-rendered template
// on the real path: the fields of the template assigned to the asset's type are
// already in the page, with no HTMX round trip behind them.
func TestQRScanLanding_RendersTemplateFields(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{
				"id": 5, "name": "Pump 5", "type_name": "Pump",
				"location": "Plant A", "criticality": "Alta", "type_id": float64(3),
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
					"id": float64(1), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
					"asset_category": "critico", "active": true,
				}})
			case strings.Contains(q, "issue_template_assignments"):
				qrTestRows(w, []map[string]any{{"template_key": "pump_failure"}})
			case strings.Contains(q, "FROM issue_templates"):
				qrTestRows(w, []map[string]any{{
					"id": float64(5), "key": "pump_failure", "name": "Pump Failure",
					"version": float64(1), "is_current": float64(1),
					"fields": []any{map[string]any{"key": "serial_no", "label": "Serial No", "type": "text"}},
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
		t.Fatalf("a live code must render the form, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="serial_no"`) {
		t.Error("the template assigned to the asset type must render into the anonymous form")
	}
}

// TestQRScanLanding_RevokedCodeExplainsItself pins the behaviour this release
// shipped: a revoked code is not a broken link, so it answers 410 with the
// reason instead of the generic not-found page the earlier plan wanted.
func TestQRScanLanding_RevokedCodeExplainsItself(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(2), "code": "BBBBBBBBBBBBBBBB", "asset_id": float64(6),
				"asset_category": "normal", "active": false, "revoked_at": "2026-01-02T10:00:00Z",
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/BBBBBBBBBBBBBBBB", "")

	if rec.Code != http.StatusGone {
		t.Fatalf("a revoked code must answer 410, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "qr.revoked_title") {
		t.Error("the page must explain that the code was revoked")
	}
	if strings.Contains(body, "qr.not_found_title") {
		t.Error("a revoked code must not look like a code that never existed")
	}
}

// TestQRScanLanding_InactiveCodeExplainsItself covers the second shape of a
// revoked code: a row whose active flag comes back as a string (a hand-written
// or seeded row) must be treated as inactive, not as live.
func TestQRScanLanding_InactiveCodeExplainsItself(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(3), "code": "CCCCCCCCCCCCCCCC", "asset_id": float64(8),
				"asset_category": "normal", "active": "false",
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/CCCCCCCCCCCCCCCC", "")

	if rec.Code != http.StatusGone {
		t.Fatalf("an inactive code must answer 410, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qr.revoked_title") {
		t.Error("the page must explain that the code was revoked")
	}
}

// TestQRScanLanding_UnknownCodeReturnsNotFound covers the code nobody ever
// issued: the generic page, no table details and no asset lookup.
func TestQRScanLanding_UnknownCodeReturnsNotFound(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/ZZZZZZZZZZZZZZZZ", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown code must answer 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qr.not_found_title") {
		t.Error("the page must answer with the generic not-found message")
	}
}

// TestQRScanLanding_MalformedCodeReturnsNotFound covers the invalid code: it
// can never live in issue_qr_codes, so it must not even be looked up there and
// must answer with the same generic page.
func TestQRScanLanding_MalformedCodeReturnsNotFound(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/not-a-code", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("a malformed code must answer 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qr.not_found_title") {
		t.Error("the page must answer with the generic not-found message")
	}
}

// TestQRScanLanding_DeletedAssetReturnsNotFound covers a live code whose asset
// was removed: the link is real, the asset is gone.
func TestQRScanLanding_DeletedAssetReturnsNotFound(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{{
				"id": float64(1), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
				"asset_category": "normal", "active": true,
			}})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := qrTestGet(t, h.QRScan, "/s/AAAAAAAAAAAAAAAA", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("a code whose asset is gone must not render, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qr.asset_not_found_title") {
		t.Error("the page must say the asset is unavailable")
	}
}

// TestQRScanLanding_LegacyCodeStillWorks keeps the fallback honest: codes in
// the old qr_codes table keep opening the form after the new table is consulted
// first, and they fall back to the asset for the category.
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
	if !strings.Contains(body, `action="/s/QR-LEGACY1/report"`) {
		t.Error("a legacy code must offer the anonymous form too")
	}
}
