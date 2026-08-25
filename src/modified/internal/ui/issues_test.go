package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestIssueCreate_RequiredTitle confirms title validation actually
// blocks a submission missing it, re-rendering with the error rather
// than silently creating an untitled issue.
func TestIssueCreate_RequiredTitle(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{})
	defer server.Close()
	h := newHandler(t, server)

	rec := postForm(t, h.IssueCreate, "/issues", url.Values{
		"title": {""},
	})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (re-render with error), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "issues.error.title_required") {
		t.Error("expected the title-required error key in the re-rendered form (test handlers have no i18n wired, so the raw key renders, not translated text)")
	}
	if !strings.Contains(rec.Body.String(), "text-red-600") {
		t.Error("expected the error to render with error styling, not just present as inert text")
	}
}

// TestIssueCreate_WithAsset_LinksCorrectly confirms a real create call
// including asset_id sends it through correctly, and status defaults
// to "open" without the caller needing to supply it.
func TestIssueCreate_WithAsset_LinksCorrectly(t *testing.T) {
	var created map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&created)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postForm(t, h.IssueCreate, "/issues", url.Values{
		"title":       {"Conveyor belt making noise"},
		"description": {"Grinding sound on startup"},
		"asset_id":    {"7"},
	})

	assertRedirectsTo(t, rec, "/issues/1")
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["status"] != "open" {
		t.Errorf("expected status to default to 'open', got %v", created["status"])
	}
	if created["asset_id"] != 7.0 {
		t.Errorf("expected asset_id=7, got %v", created["asset_id"])
	}
}

// TestIssueCreate_WithoutAsset_OmitsAssetID confirms the picker's own
// "nothing selected" state (an empty string, not "0") correctly
// results in no asset_id being sent at all, not a spurious asset_id=0.
func TestIssueCreate_WithoutAsset_OmitsAssetID(t *testing.T) {
	var created map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&created)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(2)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postForm(t, h.IssueCreate, "/issues", url.Values{
		"title":    {"General question, not asset-specific"},
		"asset_id": {""},
	})

	assertRedirectsTo(t, rec, "/issues/2")
	if _, ok := created["asset_id"]; ok {
		t.Errorf("expected no asset_id key at all when the picker had no selection, got %v", created["asset_id"])
	}
}

// TestIssueToggleStatus_OpenToClosedAndBack confirms the toggle
// actually flips based on the entity's current state, both directions,
// not just a one-way "always set closed."
func TestIssueToggleStatus_OpenToClosedAndBack(t *testing.T) {
	currentStatus := "open"
	var patchedStatus string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "status": currentStatus})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			patchedStatus = toString(body["status"])
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("POST", "/issues/1/toggle-status", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueToggleStatus(rec, req)
	assertRedirectsTo(t, rec, "/issues/1")
	if patchedStatus != "closed" {
		t.Errorf("expected open->closed, got patched status %q", patchedStatus)
	}

	currentStatus = "closed"
	req2 := httptest.NewRequest("POST", "/issues/1/toggle-status", nil)
	req2.SetPathValue("id", "1")
	rec2 := httptest.NewRecorder()
	h.IssueToggleStatus(rec2, req2)
	assertRedirectsTo(t, rec2, "/issues/1")
	if patchedStatus != "open" {
		t.Errorf("expected closed->open, got patched status %q", patchedStatus)
	}
}

// TestIssueDetail_ResolvesLinkedAssetName confirms the detail page
// correctly resolves and shows the linked asset's name, not just its
// raw ID.
func TestIssueDetail_ResolvesLinkedAssetName(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": float64(1), "title": "Test Issue", "status": "open", "asset_id": float64(9),
			})
		},
		"GET /api/v1/assets/9": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(9), "name": "Excavadora CAT 320"})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues/1", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Excavadora CAT 320") {
		t.Error("expected the linked asset's real name to appear on the detail page")
	}
}
