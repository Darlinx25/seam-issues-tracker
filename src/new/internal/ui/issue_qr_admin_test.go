package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ha1tch/minty"
)

func TestParseIssueQRAdminFiltersDefaults(t *testing.T) {
	f := parseIssueQRAdminFilters(url.Values{})
	if f.Status != "all" {
		t.Fatalf("expected all, got %s", f.Status)
	}
	if f.Page != 1 {
		t.Fatalf("expected page 1, got %d", f.Page)
	}
}

func TestParseIssueQRAdminFilters(t *testing.T) {
	v := url.Values{}
	v.Set("q", " server ")
	v.Set("status", "active")
	v.Set("asset", "123")
	v.Set("page", "2")
	f := parseIssueQRAdminFilters(v)
	if f.Search != "server" {
		t.Fatalf("bad search: %s", f.Search)
	}
	if f.Status != "active" {
		t.Fatalf("bad status: %s", f.Status)
	}
	if f.AssetID != 123 {
		t.Fatalf("bad asset: %d", f.AssetID)
	}
	if f.Page != 2 {
		t.Fatalf("bad page: %d", f.Page)
	}
}

func TestParseIssueQRAdminFiltersInvalid(t *testing.T) {
	v := url.Values{}
	v.Set("status", "bad")
	v.Set("asset", "x")
	v.Set("page", "0")
	f := parseIssueQRAdminFilters(v)
	if f.Status != "all" {
		t.Fatalf("expected all, got %s", f.Status)
	}
	if f.AssetID != 0 {
		t.Fatalf("expected 0, got %d", f.AssetID)
	}
	if f.Page != 1 {
		t.Fatalf("expected page 1, got %d", f.Page)
	}
}

func TestIssueQRAdminLink(t *testing.T) {
	f := IssueQRCodeAdminFilters{Search: "a", AssetID: 5, Status: "active", Page: 2}
	l := f.link(nil)
	if l == "/issue-qr-codes" {
		t.Fatalf("expected query params")
	}
	l2 := f.link(map[string]string{"page": ""})
	if l2 == "" {
		t.Fatalf("bad link")
	}
	f2 := IssueQRCodeAdminFilters{}
	if f2.link(nil) != "/issue-qr-codes" {
		t.Fatalf("expected base path")
	}
}

func TestIssueQRAdminTotalPages(t *testing.T) {
	if issueQRAdminTotalPages(0) != 1 {
		t.Fatalf("bad")
	}
	if issueQRAdminTotalPages(25) != 1 {
		t.Fatalf("bad")
	}
	if issueQRAdminTotalPages(26) != 2 {
		t.Fatalf("bad")
	}
}

func TestIssueQRCodesAdminPageRenders(t *testing.T) {
	tr := func(k string, a ...any) string { return k }
	data := IssueQRCodesListData{
		Filters:   IssueQRCodeAdminFilters{Status: "all", Page: 1},
		Rows:      []issueQRAdminRow{{ID: 1, Code: "ABC", AssetID: 10, AssetName: "Server", Active: true, CreatorName: "Alice", CreatedAt: "2025-01-01"}},
		Total:     1,
		CSRFToken: "tok",
	}
	n := IssueQRCodesAdminPage(data, tr)
	if n == nil {
		t.Fatal("nil node")
	}
	// just ensure it builds
	_ = context.Background()
	_ = minty.Builder{}
}

func TestAppendQuery(t *testing.T) {
	s := appendQuery("/issue-qr-codes", "notice", "revoked")
	if s == "/issue-qr-codes" {
		t.Fatalf("bad")
	}
	s2 := appendQuery("/issue-qr-codes?x=1", "notice", "ok", "count", "2")
	if s2 == "/issue-qr-codes?x=1" {
		t.Fatalf("bad")
	}
}

func TestIssueQRCodesAdminPageSeparatesBulkAndRowForms(t *testing.T) {
	tr := func(k string, a ...any) string { return k }
	data := IssueQRCodesListData{
		Filters:   IssueQRCodeAdminFilters{Status: "all", Page: 1},
		Rows:      []issueQRAdminRow{{ID: 1, Code: "ABC", AssetID: 10, AssetName: "Server", Active: true, CreatorName: "Alice", CreatedAt: "2025-01-01"}},
		Total:     1,
		CSRFToken: "tok",
	}
	html := minty.RenderToString(IssueQRCodesAdminPage(data, tr))

	if strings.Contains(html, "return confirm(") {
		t.Fatal("native confirm must be replaced by SeamConfirm")
	}
	if !strings.Contains(html, "SeamConfirm.showForm") {
		t.Fatal("expected SeamConfirm.showForm on revoke buttons")
	}
	if !strings.Contains(html, `name="ids"`) || !strings.Contains(html, `form="issue-qr-bulk-revoke-form"`) {
		t.Fatal("bulk checkbox must point at the external bulk form")
	}
	if !strings.Contains(html, `action="/issue-qr-codes/1/revoke"`) {
		t.Fatal("individual revoke form must keep its own action")
	}
	if strings.Count(html, "<form") != 3 {
		t.Fatalf("expected filter, bulk and row forms, got %d", strings.Count(html, "<form"))
	}
	if strings.Count(html, `name="return_to"`) != 2 {
		t.Fatalf("expected return_to in bulk and row forms, got %d", strings.Count(html, `name="return_to"`))
	}
}

func TestIssueQRCodeAdminRevoke_UsesReturnTo(t *testing.T) {
	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issue_qr_codes/7": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 7, "code": "ABC", "asset_id": 10, "active": true})
		},
		"PATCH /api/v1/issue_qr_codes/7": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			qrTestJSON(w, map[string]any{"id": 7})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRCodeAdminRevoke, "/issue-qr-codes/7/revoke", url.Values{
		"_csrf":     {"tok"},
		"return_to": {"/issue-qr-codes?q=server&status=active&page=2"},
	})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	want := "/issue-qr-codes?notice=revoked&page=2&q=server&status=active"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("redirect: want %q, got %q", want, got)
	}
	if patched == nil {
		t.Fatal("the code was not patched")
	}
	if patched["active"] != false {
		t.Errorf("the code must be deactivated, got %v", patched["active"])
	}
	if _, ok := patched["revoked_at"]; !ok {
		t.Error("revoked_at must be stamped")
	}
}

func TestIssueQRCodesBulkRevoke_NoSelectionKeepsReturnTo(t *testing.T) {
	h := newHandler(t, mockOLU(t, nil))
	rec := postForm(t, h.IssueQRCodesBulkRevoke, "/issue-qr-codes/revoke", url.Values{
		"return_to": {"/issue-qr-codes?status=revoked"},
	})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	want := "/issue-qr-codes?notice=none_selected&status=revoked"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("redirect: want %q, got %q", want, got)
	}
}

func TestIssueQRCodesBulkRevoke_RevokesSelectedWithReturnTo(t *testing.T) {
	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issue_qr_codes/8": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 8, "code": "XYZ", "asset_id": 11, "active": true})
		},
		"PATCH /api/v1/issue_qr_codes/8": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			qrTestJSON(w, map[string]any{"id": 8})
		},
	})
	defer server.Close()

	h := newHandler(t, server)
	rec := postForm(t, h.IssueQRCodesBulkRevoke, "/issue-qr-codes/revoke", url.Values{
		"_csrf":     {"tok"},
		"return_to": {"/issue-qr-codes?q=pump"},
		"ids":       {"8"},
	})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	want := "/issue-qr-codes?count=1&notice=bulk_revoked&q=pump"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("redirect: want %q, got %q", want, got)
	}
	if patched == nil || patched["active"] != false {
		t.Fatalf("the selected code must be revoked, got %v", patched)
	}
}
