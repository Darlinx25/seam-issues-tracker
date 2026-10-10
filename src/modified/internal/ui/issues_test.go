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
// the issue's FSM machine with input "resolve", then denormalizes the
// new state onto the entity. resolve now lands on "resolved"; the separate
// close transition (resolved -> closed) is covered by the close action.
func TestIssueResolve_InProgressToResolved(t *testing.T) {
	var patchedState string
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "in_progress", "machine_id": float64(1)})
		},
		"POST /api/v2/fsm/machine/1/walk": func(w http.ResponseWriter, r *http.Request) {
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
	if patchedState != "resolved" {
		t.Errorf("expected state to be 'resolved', got %q", patchedState)
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
