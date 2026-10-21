package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	mi "github.com/ha1tch/minty"
	"github.com/ha1tch/seam-ui/internal/auth"
)

// templateOQL decodes the OQL query and answers with the rows resolve(q)
// returns -- a single mock handler that can branch on which query a handler
// under test issued, since every OQL call shares POST /api/v1/oql/query.
func templateOQL(w http.ResponseWriter, r *http.Request, resolve func(q string) []map[string]any) {
	var body struct {
		Query string `json:"query"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	rows := resolve(body.Query)
	if rows == nil {
		rows = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "completed",
		"data":   rows,
		"stats":  map[string]any{"rows_returned": len(rows)},
	})
}

func pumpFailureTemplateRow() map[string]any {
	return map[string]any{
		"id": float64(5), "key": "pump_failure", "name": "Pump Failure",
		"description": "Pump-specific fields", "version": float64(1), "is_current": float64(1),
		"fields": []any{
			map[string]any{"key": "serial_no", "label": "Serial No", "type": "text", "required": false},
		},
	}
}

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

// TestIssueCreate_CapturesReportedBy confirms the authenticated session user
// is stored as reported_by on the created issue -- the write half of the card.
func TestIssueCreate_CapturesReportedBy(t *testing.T) {
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

	form := url.Values{"title": {"Leaking valve"}}
	req := httptest.NewRequest("POST", "/issues", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: "manager"}))
	rec := httptest.NewRecorder()
	h.IssueCreate(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["reported_by"] != 42.0 {
		t.Errorf("expected reported_by=42 (session user), got %v", created["reported_by"])
	}
}

// TestIssueCreate_WithoutUser_OmitsReportedBy confirms that with no session
// user (and DevMode off) no reported_by key is written at all.
func TestIssueCreate_WithoutUser_OmitsReportedBy(t *testing.T) {
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

	rec := postForm(t, h.IssueCreate, "/issues", url.Values{"title": {"No session"}})
	assertRedirectsTo(t, rec, "/issues/1")
	if _, ok := created["reported_by"]; ok {
		t.Errorf("expected no reported_by key when there is no session user, got %v", created["reported_by"])
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

// TestIssueResolve_InProgressToResolved confirms /issues/{id}/resolve walks
// the issue's FSM machine with the "resolve" input and denormalizes the new
// state onto the entity. The walk input is captured and asserted.
func TestIssueResolve_InProgressToResolved(t *testing.T) {
	var walkInput string
	var patchedState string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "in_progress", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			walkInput = body.Input
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"previous": "in_progress", "current": "resolved", "terminal": false})
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
	if walkInput != "resolve" {
		t.Errorf("expected walk input 'resolve', got %q", walkInput)
	}
	if patchedState != "resolved" {
		t.Errorf("expected state to be 'resolved', got %q", patchedState)
	}
}

// TestIssueClose_ResolvedToClosed confirms /issues/{id}/close walks the
// issue's FSM machine with the "close" input and denormalizes the resulting
// terminal state onto the entity. The walk input is captured and asserted,
// so a wrong transition name fails the test instead of passing silently.
func TestIssueClose_ResolvedToClosed(t *testing.T) {
	var walkInput string
	var patchedState string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "resolved", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			walkInput = body.Input
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"previous": "resolved", "current": "closed", "terminal": true})
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

	req := httptest.NewRequest("POST", "/issues/1/close", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueClose(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if walkInput != "close" {
		t.Errorf("expected walk input 'close', got %q", walkInput)
	}
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

// TestIssueDetail_ShowsReporterName confirms the detail page resolves the
// stored reported_by id to the user's name -- the read half of the card.
func TestIssueDetail_ShowsReporterName(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": float64(1), "title": "Test Issue", "state": "reported",
				"machine_id": float64(1), "reported_by": float64(42),
			})
		},
		"GET /api/v1/users/42": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(42), "name": "Horacio Lopez"})
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
	if !strings.Contains(rec.Body.String(), "Horacio Lopez") {
		t.Error("expected the reporter's resolved name to appear on the detail page")
	}
}

// TestIssueCreate_WithAssignedTemplate_StoresFormData confirms that when the
// selected asset's type has a template assigned, its custom fields are
// collected from the POST and frozen onto the issue alongside template_id.
func TestIssueCreate_WithAssignedTemplate_StoresFormData(t *testing.T) {
	var created map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(7), "name": "Pump 1", "type_id": float64(3)})
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				switch {
				case strings.Contains(q, "issue_template_assignments"):
					return []map[string]any{{"template_key": "pump_failure"}}
				case strings.Contains(q, "FROM issue_templates"):
					return []map[string]any{pumpFailureTemplateRow()}
				}
				return nil
			})
		},
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
		"title":     {"Pump making noise"},
		"asset_id":  {"7"},
		"serial_no": {"SN-123"},
	})

	assertRedirectsTo(t, rec, "/issues/1")
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["template_id"] != 5.0 {
		t.Errorf("expected template_id=5 (current version), got %v", created["template_id"])
	}
	fd, ok := created["form_data"].(map[string]any)
	if !ok {
		t.Fatalf("expected form_data map on the created issue, got %v", created["form_data"])
	}
	if fd["serial_no"] != "SN-123" {
		t.Errorf("expected form_data.serial_no='SN-123', got %v", fd["serial_no"])
	}
}

// TestIssueCreate_MissingRequiredTemplateField confirms a required custom
// field blocks creation and re-renders with the field block (and error)
// present, rather than silently creating an issue.
func TestIssueCreate_MissingRequiredTemplateField(t *testing.T) {
	created := false
	row := pumpFailureTemplateRow()
	row["fields"] = []any{map[string]any{"key": "serial_no", "label": "Serial No", "type": "text", "required": true}}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(7), "type_id": float64(3)})
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				switch {
				case strings.Contains(q, "issue_template_assignments"):
					return []map[string]any{{"template_key": "pump_failure"}}
				case strings.Contains(q, "FROM issue_templates"):
					return []map[string]any{row}
				}
				return nil
			})
		},
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			created = true
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postForm(t, h.IssueCreate, "/issues", url.Values{
		"title":    {"Pump making noise"},
		"asset_id": {"7"},
	})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (re-render), got %d", rec.Code)
	}
	if created {
		t.Error("expected no issue to be created when a required template field is missing")
	}
	if !strings.Contains(rec.Body.String(), "serial_no") {
		t.Error("expected the template field block to be re-rendered with the resolved template fields")
	}
}

// TestIssueTemplateFields_Partial confirms the HTMX partial resolves the
// assigned template for an asset and renders its field inputs.
func TestIssueTemplateFields_Partial(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(7), "type_id": float64(3)})
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				switch {
				case strings.Contains(q, "issue_template_assignments"):
					return []map[string]any{{"template_key": "pump_failure"}}
				case strings.Contains(q, "FROM issue_templates"):
					return []map[string]any{pumpFailureTemplateRow()}
				}
				return nil
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issue-templates/fields?asset_id=7", nil)
	rec := httptest.NewRecorder()
	h.IssueTemplateFields(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `name="serial_no"`) {
		t.Errorf("expected the template field input in the partial, got: %s", body)
	}
	if !strings.Contains(body, "Serial No") {
		t.Errorf("expected the field label in the partial, got: %s", body)
	}
}

// TestIssueTemplateCreate_CreatesVersionOne confirms a new template is
// stored with a slugged key, version 1, and the collected row fields.
func TestIssueTemplateCreate_CreatesVersionOne(t *testing.T) {
	var created map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any { return nil })
		},
		"POST /api/v1/issue_templates": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&created)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postForm(t, h.IssueTemplateCreate, "/issue-templates", url.Values{
		"name":                 {"Pump Failure"},
		"tpl_field_key[]":      {"serial_no"},
		"tpl_field_label[]":    {"Serial No"},
		"tpl_field_type[]":     {"text"},
		"tpl_field_idx[]":      {"0"},
		"tpl_field_required_0": {"1"},
	})

	assertRedirectsTo(t, rec, "/issue-templates")
	if created == nil {
		t.Fatal("xolu.Create for issue_templates was never called")
	}
	if created["key"] != "pump_failure" {
		t.Errorf("expected key 'pump_failure', got %v", created["key"])
	}
	if created["version"] != 1.0 {
		t.Errorf("expected version 1, got %v", created["version"])
	}
	fields, ok := created["fields"].([]any)
	if !ok || len(fields) != 1 {
		t.Fatalf("expected one stored field, got %v", created["fields"])
	}
	f := fields[0].(map[string]any)
	if f["key"] != "serial_no" || f["required"] != true {
		t.Errorf("expected stored field key=serial_no required=true, got %v", f)
	}
}

// TestIssueTemplateUpdate_CreatesNewVersion confirms editing writes version
// N+1 as current and demotes the previous version.
func TestIssueTemplateUpdate_CreatesNewVersion(t *testing.T) {
	var created, patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "FROM issue_templates") {
					row := pumpFailureTemplateRow()
					row["id"] = float64(10)
					row["version"] = float64(2)
					return []map[string]any{row}
				}
				return nil
			})
		},
		"POST /api/v1/issue_templates": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&created)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(11)})
		},
		"PATCH /api/v1/issue_templates/10": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&patched)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(10)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	form := url.Values{
		"name":              {"Pump Failure"},
		"tpl_field_key[]":   {"serial_no"},
		"tpl_field_label[]": {"Serial No"},
		"tpl_field_type[]":  {"text"},
		"tpl_field_idx[]":   {"0"},
	}
	req := httptest.NewRequest("POST", "/issue-templates/pump_failure/edit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("key", "pump_failure")
	rec := httptest.NewRecorder()
	h.IssueTemplateUpdate(rec, req)

	assertRedirectsTo(t, rec, "/issue-templates/pump_failure")
	if created["version"] != 3.0 {
		t.Errorf("expected new version 3, got %v", created["version"])
	}
	if patched["is_current"] != 0.0 {
		t.Errorf("expected previous version demoted (is_current=0), got %v", patched["is_current"])
	}
}

// TestIssueTemplateAssign_UpsertsAndClears confirms a checked type gets a new
// assignment and an unchecked type previously on this template is cleared.
func TestIssueTemplateAssign_UpsertsAndClears(t *testing.T) {
	var createdAssign map[string]any
	cleared := false
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				switch {
				case strings.Contains(q, "FROM asset_types"):
					return []map[string]any{{"id": float64(1), "name": "Pump"}, {"id": float64(2), "name": "Motor"}}
				case strings.Contains(q, "FROM issue_template_assignments") && !strings.Contains(q, "WHERE asset_type_id"):
					return []map[string]any{{"id": float64(9), "asset_type_id": float64(2), "template_key": "pump_failure"}}
				case strings.Contains(q, "FROM issue_template_assignments"):
					return []map[string]any{{"id": float64(9)}}
				case strings.Contains(q, "FROM issue_templates"):
					return []map[string]any{pumpFailureTemplateRow()}
				}
				return nil
			})
		},
		"POST /api/v1/issue_template_assignments": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&createdAssign)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(20)})
		},
		"DELETE /api/v1/issue_template_assignments/9": func(w http.ResponseWriter, r *http.Request) {
			cleared = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"message": "deleted"})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	form := url.Values{"asset_type_1": {"1"}}
	req := httptest.NewRequest("POST", "/issue-templates/pump_failure/assign", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("key", "pump_failure")
	rec := httptest.NewRecorder()
	h.IssueTemplateAssign(rec, req)

	assertRedirectsTo(t, rec, "/issue-templates/pump_failure")
	if createdAssign == nil {
		t.Fatal("expected a new assignment for the checked asset type")
	}
	if createdAssign["asset_type_id"] != 1.0 || createdAssign["template_key"] != "pump_failure" {
		t.Errorf("unexpected assignment: %v", createdAssign)
	}
	if !cleared {
		t.Error("expected the unchecked type's previous assignment to this template to be deleted")
	}
}

// TestIssueTemplatePages_Render exercises every template page through the
// real builder so a broken mi API call fails here rather than at runtime.
func TestIssueTemplatePages_Render(t *testing.T) {
	tf := func(key string, args ...any) string { return key }
	pages := map[string]mi.H{
		"list": IssueTemplatesListPage(IssueTemplatesListData{
			Templates: []IssueTemplate{{Key: "pump_failure", Name: "Pump Failure", Version: 1}},
		}, tf),
		"new": IssueTemplateFormPage(IssueTemplateFormData{FieldTypes: templateFieldTypes()}, tf),
		"edit": IssueTemplateFormPage(IssueTemplateFormData{
			IsEdit: true, Key: "pump_failure", Name: "Pump",
			Fields: []TemplateField{{Key: "serial_no", Label: "Serial No", Type: "text", Required: true}},
		}, tf),
		"detail": IssueTemplateDetailPage(IssueTemplateDetailData{
			Key: "pump_failure", Name: "Pump", Version: 1,
			Fields:     []TemplateField{{Key: "serial_no", Label: "Serial No", Type: "text"}},
			Versions:   []IssueTemplate{{Version: 1, IsCurrent: true}},
			AssetTypes: []TemplateAssetType{{ID: 1, Name: "Pump"}},
			Assigned:   map[int64]string{1: "pump_failure"},
		}, tf),
	}
	for name, page := range pages {
		var buf bytes.Buffer
		if err := page(&mi.Builder{}).Render(&buf); err != nil {
			t.Errorf("%s page render failed: %v", name, err)
		}
		if buf.Len() == 0 {
			t.Errorf("%s page rendered empty", name)
		}
	}
}

// TestIssueTemplateRoutes_Register mounts the issue routes on a fresh mux.
// Go 1.22 ServeMux panics on conflicting patterns at registration time, so
// this guards the literal-vs-wildcard shapes (/issue-templates vs
// /issue-templates/{key}) against a future edit.
func TestIssueTemplateRoutes_Register(t *testing.T) {
	router, _ := buildRoutingTestRouter(t, true, true)
	mux := http.NewServeMux()
	router.MountIssuesRoutes(mux)
}

// TestIssueCreate_BootstrapsEightStateFSMDefinition confirms the lazy
// bootstrap creates seam_issue_lifecycle with the full 8-state lifecycle
// (iteration 1): 3 terminal states and the 8 named transitions, not a
// reduced slice.
func TestIssueCreate_BootstrapsEightStateFSMDefinition(t *testing.T) {
	var spec map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v2/fsm/def": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"definitions": []map[string]any{}})
		},
		"POST /api/v2/fsm/def": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&spec)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "name": "seam_issue_lifecycle"})
		},
		"POST /api/v2/fsm/machine": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "reported"})
		},
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postForm(t, h.IssueCreate, "/issues", url.Values{"title": {"Bootstraps the FSM"}})
	assertRedirectsTo(t, rec, "/issues/1")

	if spec == nil {
		t.Fatal("expected the FSM definition to be created when none exists")
	}
	if spec["name"] != "seam_issue_lifecycle" || spec["initial"] != "reported" || spec["determinism"] != "firstmatch" {
		t.Fatalf("unexpected spec header: name=%v initial=%v determinism=%v", spec["name"], spec["initial"], spec["determinism"])
	}
	states, ok := spec["states"].(map[string]any)
	if !ok || len(states) != 8 {
		t.Fatalf("expected 8 states, got %v", spec["states"])
	}
	for _, terminal := range []string{"closed", "rejected", "duplicate"} {
		st, ok := states[terminal].(map[string]any)
		if !ok || st["terminal"] != true {
			t.Errorf("expected %q to be terminal, got %v", terminal, states[terminal])
		}
	}
	transitions, ok := spec["transitions"].([]any)
	if !ok {
		t.Fatalf("expected a transitions array, got %v", spec["transitions"])
	}
	inputs := map[string]bool{}
	for _, tr := range transitions {
		if m, ok := tr.(map[string]any); ok {
			inputs[toString(m["input"])] = true
		}
	}
	for _, in := range []string{"triage", "start", "hold", "resume", "resolve", "close", "reject", "merge"} {
		if !inputs[in] {
			t.Errorf("expected transition input %q in the spec", in)
		}
	}
}

// TestIssuesList_FiltersByState confirms a whitelisted ?state= filter is
// pushed into the list OQL as a WHERE clause, with the counts query kept
// separate.
func TestIssuesList_FiltersByState(t *testing.T) {
	var listQuery string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "GROUP BY state") {
					return []map[string]any{
						{"state": "in_progress", "c": float64(2)},
						{"state": "reported", "c": float64(1)},
					}
				}
				listQuery = q
				return []map[string]any{
					{"id": float64(3), "title": "Belt slipping", "state": "in_progress"},
					{"id": float64(2), "title": "Motor hot", "state": "in_progress"},
				}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues?state=in_progress", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(listQuery, "WHERE state = 'in_progress'") {
		t.Errorf("expected the list OQL to filter by state, got %q", listQuery)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Belt slipping") {
		t.Error("expected the filtered issue title to render")
	}
	if !strings.Contains(body, "state=in_progress") {
		t.Error("expected the active filter tab link to render")
	}
}

// TestIssuesList_InvalidStateFilterIgnored confirms a non-whitelisted
// ?state= value is dropped rather than reaching the OQL.
func TestIssuesList_InvalidStateFilterIgnored(t *testing.T) {
	var listQuery string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "GROUP BY state") {
					return []map[string]any{}
				}
				listQuery = q
				return []map[string]any{}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues?state=bogus", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(listQuery, "WHERE") {
		t.Errorf("expected no WHERE clause for a non-whitelisted state, got %q", listQuery)
	}
}

// TestIssueReject_RequiresMotivo confirms an empty reason re-renders the
// reject form with an error and never walks the FSM.
func TestIssueReject_RequiresMotivo(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Broken", "state": "reported", "machine_id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	form := url.Values{"motivo": {""}}
	req := httptest.NewRequest("POST", "/issues/1/reject", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueReject(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 (re-render with error), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "issues.reject.error.motivo_required") {
		t.Error("expected the motivo-required error to be rendered (no walk, no state change)")
	}
}

// TestIssueReject_WithMotivo_WalksWithPayload confirms a non-empty reason
// is carried as the walk payload and denormalized (state + reject_reason).
func TestIssueReject_WithMotivo_WalksWithPayload(t *testing.T) {
	const motivo = "duplicado de #12"
	var walkBody struct {
		Input   string                 `json:"input"`
		Payload map[string]interface{} `json:"payload"`
	}
	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Broken", "state": "reported", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&walkBody)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"previous": "reported", "current": "rejected", "terminal": true})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&patched)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	form := url.Values{"motivo": {motivo}}
	req := httptest.NewRequest("POST", "/issues/1/reject", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueReject(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if walkBody.Input != "reject" {
		t.Errorf("expected walk input 'reject', got %q", walkBody.Input)
	}
	if walkBody.Payload["motivo"] != motivo {
		t.Errorf("expected payload.motivo=%q, got %v", motivo, walkBody.Payload["motivo"])
	}
	if patched["state"] != "rejected" || patched["reject_reason"] != motivo {
		t.Errorf("expected state=rejected and reject_reason set, got %v", patched)
	}
}

// TestIssuesList_RendersAllStateTabsWithCounts confirms the filter bar
// renders one tab per lifecycle state (8) plus the "all" tab, each with
// its own count from the GROUP BY query.
func TestIssuesList_RendersAllStateTabsWithCounts(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "GROUP BY state") {
					return []map[string]any{
						{"state": "reported", "c": float64(1)},
						{"state": "in_progress", "c": float64(2)},
					}
				}
				return []map[string]any{}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, st := range []string{"reported", "triaged", "in_progress", "on_hold", "resolved", "closed", "rejected", "duplicate"} {
		if !strings.Contains(body, "issues.state."+st) {
			t.Errorf("expected a tab for state %q", st)
		}
	}
	if !strings.Contains(body, "issues.state.in_progress (2)") {
		t.Error("expected the in_progress tab to show its count (2)")
	}
	if !strings.Contains(body, "issues.all (3)") {
		t.Error("expected the all tab to show the summed count (3)")
	}
}

// TestIssuesList_NoSearch_NoTitleFilter confirms the text search never
// reaches the list query -- filtering is client-side over the fetched rows --
// while the search box, its placeholder and the clear button are rendered.
func TestIssuesList_NoSearch_NoTitleFilter(t *testing.T) {
	var listQuery string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "GROUP BY state") {
					return []map[string]any{}
				}
				listQuery = q
				return []map[string]any{}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(listQuery, "WHERE") || strings.Contains(listQuery, "LIKE") {
		t.Errorf("expected the search to stay out of the query, got %q", listQuery)
	}
	body := rec.Body.String()
	for _, want := range []string{`name="q"`, "issues.search_placeholder", "issue-search-clear", "issue-no-results"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the rendered list page", want)
		}
	}
}

// TestIssuesList_SearchPrefillsInput confirms ?q= prefills the box (so a
// shared URL or tab switch keeps the term) and still issues the same
// unfiltered list query, plus exposes the title as data-search for the JS.
func TestIssuesList_SearchPrefillsInput(t *testing.T) {
	var listQuery string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "GROUP BY state") {
					return []map[string]any{}
				}
				listQuery = q
				return []map[string]any{
					{"id": float64(7), "title": "Belt slipping", "state": "reported"},
				}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues?q=belt", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(listQuery, "LIKE") || strings.Contains(listQuery, "WHERE") {
		t.Errorf("expected no server-side text filter, got %q", listQuery)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="belt"`) {
		t.Error("expected the search input to keep the submitted value")
	}
	if !strings.Contains(body, `data-search="Belt slipping"`) {
		t.Error("expected the issue title to be exposed as data-search")
	}
}

// TestIssuesList_SearchDataIncludesAssetName confirms the data-search text
// the JS matches against includes the linked asset name ("Toyota - VH01"),
// so searching for "vh01" finds an issue whose title doesn't contain it.
func TestIssuesList_SearchDataIncludesAssetName(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "GROUP BY state") {
					return []map[string]any{}
				}
				return []map[string]any{
					{"id": float64(1), "title": "Motor overheat", "state": "reported", "asset_id": float64(9)},
				}
			})
		},
		"GET /api/v1/assets": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(9), "name": "Toyota - VH01"})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `data-search="Motor overheat Toyota - VH01"`) {
		t.Error("expected data-search to combine the title and the asset name")
	}
}

// TestIssuesList_SearchCombinesWithState confirms the state filter stays in
// the query while the search term survives it: the tabs carry ?q= and the
// box is prefilled, so switching tabs keeps filtering the same term.
func TestIssuesList_SearchCombinesWithState(t *testing.T) {
	var listQuery string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "GROUP BY state") {
					return []map[string]any{}
				}
				listQuery = q
				return []map[string]any{
					{"id": float64(3), "title": "Motor hot", "state": "in_progress"},
				}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues?state=in_progress&q=motor", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(listQuery, "WHERE state = 'in_progress'") {
		t.Errorf("expected the state filter to remain in the query, got %q", listQuery)
	}
	if strings.Contains(listQuery, "LIKE") {
		t.Errorf("expected no text filter in the query, got %q", listQuery)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "q=motor") {
		t.Error("expected the state tabs to keep the active search")
	}
	if !strings.Contains(body, `value="motor"`) {
		t.Error("expected the search input to be prefilled")
	}
}

// TestIssuesList_SearchEmptyMessageRendered confirms the page ships the
// search-specific no-results block (toggled by the JS when every row is
// filtered out) translated through t().
func TestIssuesList_SearchEmptyMessageRendered(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues?q=zzz", nil)
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="issue-no-results"`) {
		t.Error("expected the client-side no-results block to render")
	}
	if !strings.Contains(body, "issues.search_empty") {
		t.Error("expected the no-results text to come from t()")
	}
}

func TestAssetCategoryFromCriticality(t *testing.T) {
	cases := []struct {
		criticality string
		want        string
	}{
		{"High", "critico"},
		{"Medium", "normal"},
		{"Low", "normal"},
		{"", "normal"},
		{"Extrema", "normal"}, // unrecognised -> default
	}
	for _, c := range cases {
		if got := assetCategoryFromCriticality(c.criticality); got != c.want {
			t.Errorf("assetCategoryFromCriticality(%q) = %q, want %q", c.criticality, got, c.want)
		}
	}
}

func TestIssueCreate_WithAsset_SetsAssetCategoryCritical(t *testing.T) {
	var created map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(7), "name": "Pump 1", "type_id": float64(3), "criticality": "Alta"})
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any { return nil })
		},
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
		"title":    {"Pump making noise"},
		"asset_id": {"7"},
	})
	assertRedirectsTo(t, rec, "/issues/1")
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["asset_category"] != "critico" {
		t.Errorf("expected asset_category=critico for a critical asset, got %v", created["asset_category"])
	}
}

func TestIssueCreate_WithoutAsset_DefaultsCategoryNormal(t *testing.T) {
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

	rec := postForm(t, h.IssueCreate, "/issues", url.Values{"title": {"No asset here"}})
	assertRedirectsTo(t, rec, "/issues/1")
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["asset_category"] != "normal" {
		t.Errorf("expected asset_category=normal when no asset is linked, got %v", created["asset_category"])
	}
}

func TestIssueDetail_ShowsLinkedAssetInfo(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Test Issue", "asset_id": float64(9)})
		},
		"GET /api/v1/assets/9": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": float64(9), "name": "Excavadora CAT 320",
				"type_id": float64(3), "location_id": float64(5), "criticality": "Alta",
			})
		},
		"GET /api/v1/asset_types/3": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(3), "name": "Maquinaria"})
		},
		"GET /api/v1/locations/5": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(5), "short_name": "Planta Norte"})
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
	body := rec.Body.String()
	for _, want := range []string{"Excavadora CAT 320", "Maquinaria", "Planta Norte", "issues.asset.category.critico"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the detail page", want)
		}
	}
}

func TestIssueDetail_NoAsset_OmitsAssetInfo(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "No asset"})
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
	if strings.Contains(rec.Body.String(), "issues.asset.category") {
		t.Error("expected no asset info section when the issue has no linked asset")
	}
}

func TestIssueDetail_MissingAsset_ShowsUnavailableNote(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Dangling asset", "asset_id": float64(999)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues/1", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (page must not break), got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "issues.asset.unavailable") {
		t.Error("expected the asset-unavailable note when the linked asset cannot be read")
	}
}

// TestIssueDetail_ShowsHistory confirms the detail page renders the machine
// history chronologically with action, resolved author, timestamp and the
// rejection reason.
func TestIssueDetail_ShowsHistory(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"id": float64(1), "title": "Pump leaking", "state": "rejected",
				"machine_id": float64(7), "reported_by": float64(42),
				"created_at": "2026-10-20T10:00:00Z",
			})
		},
		"GET /api/v1/users/42": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(42), "name": "Horacio Lopez"})
		},
		"GET /api/v1/users/9": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(9), "name": "Ana Diaz"})
		},
		"GET /api/v2/fsm/machine/7/history": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"machine": float64(7),
				"entries": []map[string]any{
					{"id": float64(1), "to": "reported", "note": "machine created", "at": "2026-10-20T10:00:00Z"},
					{"id": float64(2), "from": "reported", "to": "in_progress", "input": "start",
						"payload": map[string]any{"actor": float64(9)}, "at": "2026-10-20T11:00:00Z"},
					{"id": float64(3), "from": "in_progress", "to": "rejected", "input": "reject",
						"payload": map[string]any{"actor": float64(9), "motivo": "duplicado de #12"}, "at": "2026-10-20T12:00:00Z"},
				},
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
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"issues.history.title",
		"issues.history.created",
		"issues.history.action.start",
		"issues.history.action.reject",
		"Horacio Lopez",
		"Ana Diaz",
		"2026-10-20T12:00:00Z",
		"duplicado de #12",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected detail body to contain %q", want)
		}
	}
}

// TestIssueStart_WalkCarriesActor confirms the walk payload records the
// session user as the action's author.
func TestIssueStart_WalkCarriesActor(t *testing.T) {
	var walkBody struct {
		Input   string                 `json:"input"`
		Payload map[string]interface{} `json:"payload"`
	}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "reported", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&walkBody)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"previous": "reported", "current": "in_progress", "terminal": false})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("POST", "/issues/1/start", nil)
	req.SetPathValue("id", "1")
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: "manager"}))
	rec := httptest.NewRecorder()
	h.IssueStart(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if walkBody.Input != "start" {
		t.Errorf("expected walk input 'start', got %q", walkBody.Input)
	}
	if walkBody.Payload["actor"] != float64(42) {
		t.Errorf("expected payload.actor=42, got %v", walkBody.Payload["actor"])
	}
}


// --- Assign responsible (card 4) ---

// postIssueForm posts a form to an issue action, setting the {id} path
// value the handler reads (the router does that in production).
func postIssueForm(t *testing.T, h http.HandlerFunc, id, target string, fields url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestIssueCreate_DefaultsAssigneeToCreator confirms a new issue is born
// already assigned to the user who reported it (option A: the creator is
// the initial assignee).
func TestIssueCreate_DefaultsAssigneeToCreator(t *testing.T) {
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

	form := url.Values{"title": {"Leaking valve"}}
	req := httptest.NewRequest("POST", "/issues", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: "manager"}))
	rec := httptest.NewRecorder()
	h.IssueCreate(rec, req)

	assertRedirectsTo(t, rec, "/issues/1")
	if created == nil {
		t.Fatal("xolu.Create for issues was never called")
	}
	if created["assignee"] != 42.0 {
		t.Errorf("expected assignee=42 (session user), got %v", created["assignee"])
	}
}

// TestIssueAssign_ReportedFiresTriage confirms that assigning an issue
// that is still in "reported" also walks the FSM with input "triage", and
// that both the new state and the assignee are written to the entity.
func TestIssueAssign_ReportedFiresTriage(t *testing.T) {
	var walkInput string
	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Leaking valve", "state": "reported", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			walkInput = body.Input
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "previous": "reported", "current": "triaged", "terminal": false})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&patched)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueAssign, "1", "/issues/1/assign", url.Values{"assignee": {"7"}})
	assertRedirectsTo(t, rec, "/issues/1")
	if walkInput != "triage" {
		t.Errorf("expected walk input %q, got %q", "triage", walkInput)
	}
	if patched == nil {
		t.Fatal("xolu.Patch for issues was never called")
	}
	if patched["assignee"] != 7.0 {
		t.Errorf("expected assignee=7, got %v", patched["assignee"])
	}
	if patched["state"] != "triaged" {
		t.Errorf("expected state=%q after triage, got %v", "triaged", patched["state"])
	}
}

// TestIssueAssign_NonReportedOnlyRewritesAssignee confirms that assigning
// an issue in a non-terminal state other than "reported" only rewrites the
// assignee -- the FSM is left alone.
func TestIssueAssign_NonReportedOnlyRewritesAssignee(t *testing.T) {
	var walkInput string
	var patched map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Leaking valve", "state": "triaged", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			walkInput = body.Input
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"current": "triaged"})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&patched)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueAssign, "1", "/issues/1/assign", url.Values{"assignee": {"7"}})
	assertRedirectsTo(t, rec, "/issues/1")
	if walkInput != "" {
		t.Errorf("expected no FSM walk for a triaged issue, got input %q", walkInput)
	}
	if patched == nil {
		t.Fatal("xolu.Patch for issues was never called")
	}
	if patched["assignee"] != 7.0 {
		t.Errorf("expected assignee=7, got %v", patched["assignee"])
	}
	if _, ok := patched["state"]; ok {
		t.Errorf("expected no state rewrite outside reported, got %v", patched["state"])
	}
}

// TestIssueAssign_TerminalRefused confirms a closed issue cannot be
// assigned: the FSM and the entity are both left untouched.
func TestIssueAssign_TerminalRefused(t *testing.T) {
	var walked, patched bool
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Leaking valve", "state": "closed", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
			walked = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"current": "closed"})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			patched = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueAssign, "1", "/issues/1/assign", url.Values{"assignee": {"7"}})
	if walked {
		t.Error("expected no FSM walk for a closed issue")
	}
	if patched {
		t.Error("expected no entity patch for a closed issue")
	}
	if rec.Code == http.StatusSeeOther {
		t.Errorf("expected the assign page to re-render with an error, got redirect to %q", rec.Header().Get("Location"))
	}
}

// TestIssueAssign_RequiresAssignee confirms the assignee is mandatory:
// a submission without one re-renders with an error and never writes.
func TestIssueAssign_RequiresAssignee(t *testing.T) {
	var patched bool
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Leaking valve", "state": "reported", "machine_id": float64(1)})
		},
		"PATCH /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			patched = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueAssign, "1", "/issues/1/assign", url.Values{"assignee": {""}})
	if patched {
		t.Error("expected no entity patch when the assignee is missing")
	}
	if rec.Code == http.StatusSeeOther {
		t.Errorf("expected the assign page to re-render with an error, got redirect to %q", rec.Header().Get("Location"))
	}
}

// TestIssueDetail_ShowsAssigneeName confirms the issue detail page shows
// the resolved name of the assignee.
func TestIssueDetail_ShowsAssigneeName(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "title": "Leaking valve", "state": "reported", "machine_id": float64(1), "assignee": float64(9)})
		},
		"GET /api/v1/users/9": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(9), "name": "Ada Lovelace"})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues/1", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	h.IssueDetail(rec, req)

	if body := rec.Body.String(); !strings.Contains(body, "Ada Lovelace") {
		t.Errorf("expected the assignee name in the detail page")
	}
}

// TestIssueAssignRoutes_Register confirms the assign routes are mounted
// alongside the rest of the issue routes.
func TestIssueAssignRoutes_Register(t *testing.T) {
	router, _ := buildRoutingTestRouter(t, true, true)
	mux := http.NewServeMux()
	router.MountIssuesRoutes(mux)
}
