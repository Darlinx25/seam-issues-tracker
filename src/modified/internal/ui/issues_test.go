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
// including asset_id sends it through correctly: the issue FSM
// definition is bootstrapped (or found), a machine instance is created
// for this issue, and state/machine_id are denormalized onto the entity
// alongside priority/severity.
func TestIssueCreate_WithAsset_LinksCorrectly(t *testing.T) {
	var created map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v2/fsm/def": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"definitions": []map[string]any{{"id": float64(1), "name": "seam_issue_lifecycle"}}})
		},
		"POST /api/v2/fsm/machine": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "reported"})
		},
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
		"priority":    {"high"},
		"severity":    {"major"},
	})

	assertRedirectsTo(t, rec, "/issues/1")
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["state"] != "reported" {
		t.Errorf("expected state to be 'reported' (machine initial), got %v", created["state"])
	}
	if created["machine_id"] != 1.0 {
		t.Errorf("expected machine_id=1, got %v", created["machine_id"])
	}
	if created["priority"] != "high" {
		t.Errorf("expected priority to be 'high', got %v", created["priority"])
	}
	if created["severity"] != "major" {
		t.Errorf("expected severity to be 'major', got %v", created["severity"])
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
		"GET /api/v2/fsm/def": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"definitions": []map[string]any{{"id": float64(1), "name": "seam_issue_lifecycle"}}})
		},
		"POST /api/v2/fsm/machine": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "reported"})
		},
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

// TestIssueStart_ReportedToInProgress confirms /issues/{id}/start walks
// the issue's FSM machine with input "start", then denormalizes the new
// state onto the entity.
func TestIssueStart_ReportedToInProgress(t *testing.T) {
	var patchedState string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "reported", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"previous": "reported", "current": "in_progress", "terminal": false})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			patchedState = toString(body["state"])
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("POST", "/issues/1/start", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueStart(rec, req)
	assertRedirectsTo(t, rec, "/issues/1")
	if patchedState != "in_progress" {
		t.Errorf("expected state to be 'in_progress', got %q", patchedState)
	}
}

// TestIssueResolve_InProgressToClosed confirms /issues/{id}/resolve walks
// the issue's FSM machine with input "resolve", then denormalizes the
// new state onto the entity.
func TestIssueResolve_InProgressToClosed(t *testing.T) {
	var patchedState string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "in_progress", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"previous": "in_progress", "current": "closed", "terminal": true})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			patchedState = toString(body["state"])
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("POST", "/issues/1/resolve", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueResolve(rec, req)
	assertRedirectsTo(t, rec, "/issues/1")
	if patchedState != "closed" {
		t.Errorf("expected state to be 'closed', got %q", patchedState)
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
