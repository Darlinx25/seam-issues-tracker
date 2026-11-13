package ui

// Tests for the anonymous report created by the public QR form
// (POST /s/{code}/report).
//
// The channel is the only way into the module without an account, so these
// tests pin the contract the ROADMAP states and nothing else: the issue is
// created with channel "qr" and no reported_by/assignee, the asset and its
// category come from the code (never from the request), the contact block and
// the template answers land in form_data, the SLA is computed with the frozen
// category, and a rejected submission creates nothing. The POST redirects to
// the public tracking page of the token it just stored, never to an id-based
// confirmation.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// qrReportLiveCode serves the OQL lookup of a live code pointing at asset 5.
// The row carries the category frozen when the QR was printed.
func qrReportLiveCode(w http.ResponseWriter, r *http.Request) {
	qrTestRows(w, []map[string]any{{
		"id": float64(1), "code": "AAAAAAAAAAAAAAAA", "asset_id": float64(5),
		"asset_category": "critico", "active": true,
	}})
}

// qrReportAsset serves the asset the live code points at.
func qrReportAsset(criticality string, typeID int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"id": 5, "name": "Pump 5", "type_name": "Pump",
			"status": "active", "location": "Plant A", "criticality": criticality,
		}
		if typeID > 0 {
			doc["type_id"] = float64(typeID)
		}
		qrTestJSON(w, doc)
	}
}

// qrReportFSM serves the lazy bootstrap of the issue definition and the machine
// creation every report goes through.
func qrReportFSM(routes map[string]http.HandlerFunc) {
	routes["GET /api/v2/fsm/def"] = func(w http.ResponseWriter, r *http.Request) {
		qrTestJSON(w, map[string]any{"definitions": []map[string]any{
			{"id": float64(1), "name": "seam_issue_lifecycle"},
		}})
	}
	routes["POST /api/v2/fsm/machine"] = func(w http.ResponseWriter, r *http.Request) {
		qrTestJSON(w, map[string]any{"id": float64(1), "state": "reported"})
	}
}

// assertIssueTrackRedirect checks the POST redirects to the tracking page of
// the token that was just stored, not to a code-addressed confirmation.
func assertIssueTrackRedirect(t *testing.T, rec *httptest.ResponseRecorder, created map[string]any) {
	t.Helper()
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	token, _ := created["track_token"].(string)
	if !validIssueTrackingToken(token) {
		t.Fatalf("the created issue must carry a valid track_token, got %q", token)
	}
	assertRedirectsTo(t, rec, issueTrackLinkPrefix+token+"?new=1")
}

// qrReportAssertDeadline checks one SLA deadline is roughly now + want: the
// stored value is a formatted timestamp, so an exact comparison would only make
// the test flaky.
func qrReportAssertDeadline(t *testing.T, field, raw string, want time.Duration) {
	t.Helper()
	if raw == "" {
		t.Fatalf("%s was not stored", field)
	}
	got, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("%s is not an RFC3339 timestamp: %q", field, raw)
	}
	if d := time.Until(got); d < want-time.Minute || d > want+time.Minute {
		t.Errorf("%s must be about %v from now, got %v (%s)", field, want, d, raw)
	}
}

// qrReportForm is a complete, valid anonymous report.
func qrReportForm() url.Values {
	return url.Values{
		"contact_name":     {"Carolina Mendez"},
		"contact_document": {"4.512.873-6"},
		"contact_phone":    {"099 123 456"},
		"contact_email":    {"carolina@correo.com"},
		"title":            {"Oil leak"},
		"description":      {"Oil under the pump, burnt smell."},
	}
}

// TestIssueQRReport_CreatesAnonymousIssue is the happy path: the issue is
// created for the asset of the code, with channel "qr", no reporter and no
// assignee, the frozen category, the code defaults and the contact block.
func TestIssueQRReport_CreatesAnonymousIssue(t *testing.T) {
	var created map[string]any
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportLiveCode,
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 31)
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", qrReportForm())

	assertIssueTrackRedirect(t, rec, created)
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["channel"] != "qr" {
		t.Errorf("the QR channel must be forced, got %v", created["channel"])
	}
	if _, ok := created["reported_by"]; ok {
		t.Errorf("an anonymous report must not carry reported_by, got %v", created["reported_by"])
	}
	if _, ok := created["assignee"]; ok {
		t.Errorf("an anonymous report must not be auto-assigned, got %v", created["assignee"])
	}
	if created["asset_id"] != 5.0 {
		t.Errorf("the asset must come from the code, want 5, got %v", created["asset_id"])
	}
	if created["asset_category"] != "critico" {
		t.Errorf("the category must be the one frozen into the code, got %v", created["asset_category"])
	}
	if created["priority"] != "medium" || created["severity"] != "minor" {
		t.Errorf("the QR form keeps the in-app defaults, got priority=%v severity=%v",
			created["priority"], created["severity"])
	}
	if created["state"] != "reported" || created["machine_id"] != 1.0 {
		t.Errorf("the issue must start in the state of its machine, got state=%v machine=%v",
			created["state"], created["machine_id"])
	}
	if _, ok := created["template_id"]; ok {
		t.Errorf("an asset without a template must not store template_id, got %v", created["template_id"])
	}

	fd, ok := created["form_data"].(map[string]any)
	if !ok {
		t.Fatalf("expected form_data on the created issue, got %v", created["form_data"])
	}
	for field, want := range map[string]string{
		"contact_name":     "Carolina Mendez",
		"contact_document": "4.512.873-6",
		"contact_phone":    "099 123 456",
		"contact_email":    "carolina@correo.com",
	} {
		if fd[field] != want {
			t.Errorf("form_data.%s must be %q, got %v", field, want, fd[field])
		}
	}

	// The category is frozen at "critico", so the fixed table gives 240 min for
	// the first response and 1440 min for the resolution.
	qrReportAssertDeadline(t, "sla_first_response_deadline", toString(created["sla_first_response_deadline"]), 240*time.Minute)
	qrReportAssertDeadline(t, "sla_resolution_deadline", toString(created["sla_resolution_deadline"]), 1440*time.Minute)
}

// TestIssueQRReport_IgnoresAssetInTheRequest pins the trust boundary: the form
// cannot choose the asset, the code does.
func TestIssueQRReport_IgnoresAssetInTheRequest(t *testing.T) {
	var created map[string]any
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportLiveCode,
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 31)
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	form := qrReportForm()
	form.Set("asset_id", "99")
	form.Set("asset_category", "normal")
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", form)

	assertIssueTrackRedirect(t, rec, created)
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["asset_id"] != 5.0 {
		t.Errorf("a posted asset_id must be ignored, want 5, got %v", created["asset_id"])
	}
	if created["asset_category"] != "critico" {
		t.Errorf("a posted asset_category must be ignored, want critico, got %v", created["asset_category"])
	}
}

// TestIssueQRReport_RequiresTheContactBlock covers the mandatory contact: a
// report without it is re-rendered with one error per field and creates
// nothing.
func TestIssueQRReport_RequiresTheContactBlock(t *testing.T) {
	created := false
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportLiveCode,
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = true
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", url.Values{
		"title":       {"Oil leak"},
		"description": {"Oil under the pump."},
	})

	if rec.Code != http.StatusOK {
		t.Errorf("a rejected report must re-render the form, got %d", rec.Code)
	}
	if created {
		t.Error("a report without contact details must not be created")
	}
	body := rec.Body.String()
	if strings.Count(body, "qr.report.error.contact_required") != 4 {
		t.Errorf("each of the four contact fields must carry its error, body carried %d",
			strings.Count(body, "qr.report.error.contact_required"))
	}
	if !strings.Contains(body, `name="title"`) || !strings.Contains(body, `action="/s/AAAAAAAAAAAAAAAA/report"`) {
		t.Error("the re-rendered form must keep every field and its action")
	}
}

// TestIssueQRReport_RejectsMalformedEmail covers the only contact field with a
// format rule: a typo must not be accepted, since the address is the way back
// to the reporter.
func TestIssueQRReport_RejectsMalformedEmail(t *testing.T) {
	created := false
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportLiveCode,
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = true
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	form := qrReportForm()
	form.Set("contact_email", "carolina.correo.com")
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", form)

	if created {
		t.Error("a malformed email must block the report")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "error.invalid_email") {
		t.Error("the email field must carry the invalid-format error")
	}
	if !strings.Contains(body, `value="carolina.correo.com"`) {
		t.Error("the rejected address must be kept so the scanner can fix it")
	}
}

// TestIssueQRReport_RequiresTitleAndDescription covers the two fields the
// mockup marks with an asterisk: neither is optional.
func TestIssueQRReport_RequiresTitleAndDescription(t *testing.T) {
	created := false
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportLiveCode,
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = true
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", url.Values{
		"contact_name":     {"Carolina Mendez"},
		"contact_document": {"4.512.873-6"},
		"contact_phone":    {"099 123 456"},
		"contact_email":    {"carolina@correo.com"},
	})

	if created {
		t.Error("a report without title and description must not be created")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "issues.error.title_required") {
		t.Error("the title must be required")
	}
	if !strings.Contains(body, "qr.report.error.description_required") {
		t.Error("the description must be required")
	}
}

// TestIssueQRReport_DeadCodesCreateNothing covers the dead-code shapes: a
// revoked code explains itself (410, the behaviour this release shipped) and
// the rest answer like a missing code (404). None of them reaches the create
// call.
func TestIssueQRReport_DeadCodesCreateNothing(t *testing.T) {
	cases := []struct {
		name   string
		code   string
		routes map[string]http.HandlerFunc
		status int
		want   string
	}{
		{
			name: "revoked",
			code: "BBBBBBBBBBBBBBBB",
			routes: map[string]http.HandlerFunc{
				"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
					qrTestRows(w, []map[string]any{{
						"id": float64(2), "code": "BBBBBBBBBBBBBBBB", "asset_id": float64(6),
						"asset_category": "normal", "active": false,
					}})
				},
			},
			status: http.StatusGone,
			want:   "qr.revoked_title",
		},
		{
			name:   "unknown",
			code:   "ZZZZZZZZZZZZZZZZ",
			routes: map[string]http.HandlerFunc{},
			status: http.StatusNotFound,
			want:   "qr.not_found_title",
		},
		{
			name: "deleted asset",
			code: "AAAAAAAAAAAAAAAA",
			routes: map[string]http.HandlerFunc{
				"POST /api/v1/oql/query": qrReportLiveCode,
			},
			status: http.StatusNotFound,
			want:   "qr.asset_not_found_title",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			created := false
			tc.routes["POST /api/v1/issues"] = func(w http.ResponseWriter, r *http.Request) {
				created = true
			}
			server := mockOLU(t, tc.routes)
			defer server.Close()

			h := newHandler(t, server)
			rec := postForm(t, h.IssueQRReport, "/s/"+tc.code+"/report", qrReportForm())

			if rec.Code != tc.status {
				t.Fatalf("a dead code must answer %d, got %d", tc.status, rec.Code)
			}
			if created {
				t.Error("a dead code must never create an issue")
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("the page must answer with %q", tc.want)
			}
		})
	}
}

// qrReportTemplateRow is a template with one optional and one required field
// plus a short SLA table, so a report can prove both halves are used.
func qrReportTemplateRow(required bool) map[string]any {
	return map[string]any{
		"id": float64(5), "key": "pump_failure", "name": "Pump Failure",
		"description": "Pump-specific fields", "version": float64(1), "is_current": float64(1),
		"fields": []any{
			map[string]any{"key": "serial_no", "label": "Serial No", "type": "text", "required": required},
		},
		"sla_config": map[string]any{
			"first_response": map[string]any{"medium": float64(30)},
			"resolution":     map[string]any{"medium": float64(60)},
		},
	}
}

// qrReportTemplateOQL answers the two queries the template resolution issues.
func qrReportTemplateOQL(t *testing.T, row map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := qrTestQuery(t, r)
		switch {
		case strings.Contains(q, "FROM issue_qr_codes"):
			qrReportLiveCode(w, r)
		case strings.Contains(q, "issue_template_assignments"):
			qrTestRows(w, []map[string]any{{"template_key": "pump_failure"}})
		case strings.Contains(q, "FROM issue_templates"):
			qrTestRows(w, []map[string]any{row})
		default:
			qrTestRows(w, nil)
		}
	}
}

// TestIssueQRReport_StoresTemplateAnswersAndUsesItsSLA covers an asset whose
// type has a template: the answers land in form_data next to the contact block,
// template_id is stored, and the template's SLA wins over the fixed table.
func TestIssueQRReport_StoresTemplateAnswersAndUsesItsSLA(t *testing.T) {
	var created map[string]any
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Alta", 3),
		"POST /api/v1/oql/query": qrReportTemplateOQL(t, qrReportTemplateRow(false)),
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 31)
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	form := qrReportForm()
	form.Set("serial_no", "SN-123")
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", form)

	assertIssueTrackRedirect(t, rec, created)
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["template_id"] != 5.0 {
		t.Errorf("expected template_id=5 (current version), got %v", created["template_id"])
	}
	fd, ok := created["form_data"].(map[string]any)
	if !ok {
		t.Fatalf("expected form_data on the created issue, got %v", created["form_data"])
	}
	if fd["serial_no"] != "SN-123" {
		t.Errorf("the template answer must be stored, got %v", fd["serial_no"])
	}
	if fd["contact_email"] != "carolina@correo.com" {
		t.Errorf("the contact block must sit next to the template answers, got %v", fd["contact_email"])
	}
	qrReportAssertDeadline(t, "sla_first_response_deadline", toString(created["sla_first_response_deadline"]), 30*time.Minute)
	qrReportAssertDeadline(t, "sla_resolution_deadline", toString(created["sla_resolution_deadline"]), 60*time.Minute)
}

// TestIssueQRReport_MissingRequiredTemplateField covers the template's own
// required field: it blocks the report and re-renders the field with its error.
func TestIssueQRReport_MissingRequiredTemplateField(t *testing.T) {
	created := false
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Alta", 3),
		"POST /api/v1/oql/query": qrReportTemplateOQL(t, qrReportTemplateRow(true)),
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = true
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", qrReportForm())

	if created {
		t.Error("a missing required template field must block the report")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "serial_no") {
		t.Error("the template field block must be re-rendered with the resolved template")
	}
	if !strings.Contains(body, "field.required") {
		t.Error("the template field must carry its required error")
	}
}
