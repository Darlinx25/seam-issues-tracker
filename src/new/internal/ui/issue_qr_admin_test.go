package ui

import (
	"context"
	"net/url"
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
	t := func(k string, a ...any) string { return k }
	data := IssueQRCodesListData{
		Filters:   IssueQRCodeAdminFilters{Status: "all", Page: 1},
		Rows:      []issueQRAdminRow{{ID: 1, Code: "ABC", AssetID: 10, AssetName: "Server", Active: true, CreatorName: "Alice", CreatedAt: "2025-01-01"}},
		Total:     1,
		CSRFToken: "tok",
	}
	n := IssueQRCodesAdminPage(data, t)
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
