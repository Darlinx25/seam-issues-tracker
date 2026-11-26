package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func qrReportOQLWithContact(t *testing.T, ask, required *bool) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(qrTestQuery(t, r), "ask_contact") {
			if ask == nil {
				qrTestRows(w, nil)
				return
			}
			qrTestRows(w, []map[string]any{{
				"ask_contact":     *ask,
				"require_contact": *required,
			}})
			return
		}
		qrReportLiveCode(w, r)
	}
}

func qrReportBasicForm() url.Values {
	return url.Values{
		"title":       {"Oil leak"},
		"description": {"Oil under the pump, burnt smell."},
	}
}

func qrReportCreatedFormData(t *testing.T, created map[string]any) map[string]any {
	t.Helper()
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	fd, ok := created["form_data"].(map[string]any)
	if !ok {
		t.Fatalf("form_data must be an object, got %T", created["form_data"])
	}
	return fd
}

func qrReportAssertNoContactKeys(t *testing.T, fd map[string]any) {
	t.Helper()
	for _, key := range []string{qrReportContactName, qrReportContactDocument, qrReportContactPhone, qrReportContactEmail} {
		if _, ok := fd[key]; ok {
			t.Errorf("a form configured to hide contact details must not store %q, got %v", key, fd)
		}
	}
}

func TestIssueQRReport_ContactOptionalAcceptsAnEmptyBlock(t *testing.T) {
	ask, required := true, false
	var created map[string]any
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportOQLWithContact(t, &ask, &required),
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 31)
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", qrReportBasicForm())

	assertIssueTrackRedirect(t, rec, created)
	fd := qrReportCreatedFormData(t, created)
	for _, key := range []string{qrReportContactName, qrReportContactDocument, qrReportContactPhone, qrReportContactEmail} {
		if got, ok := fd[key]; !ok || got != "" {
			t.Errorf("an optional contact field must keep its submitted value, %q = %v", key, got)
		}
	}
	if created["notify_state"] != false {
		t.Errorf("an empty optional contact must not request email notifications, got %v", created["notify_state"])
	}
	if _, ok := created["notify_email"]; ok {
		t.Error("notify_email must not be stored without an address")
	}
}

func TestIssueQRReport_ContactHiddenIgnoresTamperedValues(t *testing.T) {
	ask, required := false, true
	var created map[string]any
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportOQLWithContact(t, &ask, &required),
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 31)
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	form := qrReportBasicForm()
	form.Set(qrReportContactName, "Forged Name")
	form.Set(qrReportContactEmail, "forged@correo.com")
	form.Set("notify_channel", "email")

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", form)

	assertIssueTrackRedirect(t, rec, created)
	qrReportAssertNoContactKeys(t, qrReportCreatedFormData(t, created))
	if created["notify_state"] != false {
		t.Errorf("a hidden contact block must force notify_state=false, got %v", created["notify_state"])
	}
	if _, ok := created["notify_email"]; ok {
		t.Error("a hidden contact block must never store notify_email")
	}
}

func TestIssueQRReport_ContactOptionalRequiresEmailOnlyWhenNotified(t *testing.T) {
	ask, required := true, false
	var created map[string]any
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/assets/5":   qrReportAsset("Baja", 0),
		"POST /api/v1/oql/query": qrReportOQLWithContact(t, &ask, &required),
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 31)
		},
	}
	qrReportFSM(routes)
	server := mockOLU(t, routes)
	defer server.Close()

	form := qrReportBasicForm()
	form.Set("notify_channel", "email")

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRReport, "/s/AAAAAAAAAAAAAAAA/report", form)

	if created != nil {
		t.Fatal("a report asking for email without an address must not be created")
	}
	if !strings.Contains(rec.Body.String(), "qr.report.error.contact_required") {
		t.Errorf("the email error must land on the form, got %q", rec.Body.String())
	}
}

func TestIssueQRReportPage_ContactModes(t *testing.T) {
	hidden := qrReportTestData()
	hidden.HideContact = true
	body := renderQRReportPage(t, hidden)
	if strings.Contains(body, `name="contact_email"`) || strings.Contains(body, `name="notify_channel"`) {
		t.Error("a hidden contact block must not render its inputs or its notification selector")
	}

	optional := qrReportTestData()
	optional.ContactOptional = true
	body = renderQRReportPage(t, optional)
	if !strings.Contains(body, `name="contact_email"`) {
		t.Error("an optional contact block must still be visible")
	}
	if got := strings.Count(body, `required="required"`); got != 2 {
		t.Errorf("only the two report fields may stay required, got %d required attributes", got)
	}
}

func TestIssueAnonymousContactSettingsFromRow(t *testing.T) {
	cases := []struct {
		name    string
		row     map[string]any
		wantAsk bool
		wantReq bool
	}{
		{"old row", map[string]any{}, true, true},
		{"optional", map[string]any{"ask_contact": true, "require_contact": false}, true, false},
		{"hidden", map[string]any{"ask_contact": false, "require_contact": true}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := issueAnonymousContactSettingsFromRow(tc.row)
			if got.Ask != tc.wantAsk || got.Required != tc.wantReq {
				t.Errorf("want ask=%v required=%v, got %+v", tc.wantAsk, tc.wantReq, got)
			}
		})
	}
}

func TestParseIssueAnonymousContactSettings(t *testing.T) {
	form := url.Values{"qr_contact_form": {"1"}, "qr_contact_ask": {"on"}, "qr_contact_required": {"on"}}
	req := httptest.NewRequest(http.MethodPost, "/assets/5/qr/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got := parseIssueAnonymousContactSettings(req)
	if !got.Ask || !got.Required {
		t.Errorf("checked options must be stored, got %+v", got)
	}

	form = url.Values{"qr_contact_form": {"1"}, "qr_contact_ask": {"on"}, "qr_contact_required": {"on"}}
	form.Del("qr_contact_ask")
	req = httptest.NewRequest(http.MethodPost, "/assets/5/qr/generate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got = parseIssueAnonymousContactSettings(req)
	if got.Ask || got.Required {
		t.Errorf("required must be ignored when contact is not asked, got %+v", got)
	}
}
