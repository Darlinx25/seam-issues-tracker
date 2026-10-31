package ui

// Tests for the grouping card ("Agrupar incidencias en un Issue Madre",
// iteration 2). planIssueGroup is tested as a pure function; the handlers are
// tested against the same mock xolu server the rest of the issue tests use.
// Ids stay in the 1..4 range on purpose: mockOLU matches routes by prefix, so
// a "GET /api/v1/issues/1" route would also answer "/10".

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ha1tch/seam-ui/internal/auth"
)

// groupEntity answers a Get with a fixed entity.
func groupEntity(data map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	}
}

// groupPatchRecorder records the body of a Patch so the test can assert what
// was denormalized onto the entity.
func groupPatchRecorder(seen map[int64]map[string]any, id int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen[id] = body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": float64(id)})
	}
}

// groupWalkRecorder records which machine was walked and answers with the
// given resulting state.
func groupWalkRecorder(seen *[]int64, id int64, to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, id)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"previous": "", "current": to, "terminal": false})
	}
}

func issueGroupRow(id int64, title, state string, assetID, masterID int64) map[string]any {
	return map[string]any{
		"id":        float64(id),
		"title":     title,
		"state":     state,
		"asset_id":  float64(assetID),
		"master_id": float64(masterID),
	}
}

func getIssueGroupPageAs(t *testing.T, h http.HandlerFunc, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/issues/group", nil)
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: role}))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func postIssueGroupFormAs(t *testing.T, h http.HandlerFunc, fields url.Values, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/issues/group", strings.NewReader(fields.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: role}))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// --- Pure planning rules ---

func TestPlanIssueGroup(t *testing.T) {
	reported := func(id int64) Issue { return Issue{ID: id, State: "reported", AssetID: 5} }

	cases := []struct {
		name        string
		issues      []Issue
		hasChildren map[int64]bool
		wantKeep    int64
		wantJoin    []int64
		wantErr     string
	}{
		{"oldest becomes the case", []Issue{reported(7), reported(3)}, nil, 3, []int64{7}, ""},
		{"input order does not decide", []Issue{reported(9), reported(4), reported(6)}, nil, 4, []int64{9, 6}, ""},
		{"one issue is not a group", []Issue{reported(4)}, nil, 0, nil, "issues.group.error.too_few"},
		{"empty selection", nil, nil, 0, nil, "issues.group.error.too_few"},
		{"duplicates collapse", []Issue{reported(4), reported(4)}, nil, 0, nil, "issues.group.error.too_few"},
		{"a child cannot be regrouped", []Issue{reported(1), {ID: 2, State: "reported", AssetID: 5, MasterID: 9}}, nil, 0, nil, "issues.group.error.child_selected"},
		{"a case cannot be nested", []Issue{reported(1), reported(2)}, map[int64]bool{2: true}, 0, nil, "issues.group.error.master_selected"},
		{"a terminal issue cannot be grouped", []Issue{reported(1), {ID: 2, State: "closed", AssetID: 5}}, nil, 0, nil, "issues.group.error.terminal"},
		{"states must match", []Issue{reported(1), {ID: 2, State: "triaged", AssetID: 5}}, nil, 0, nil, "issues.group.error.state_mismatch"},
		{"assets must match", []Issue{reported(1), {ID: 2, State: "reported", AssetID: 6}}, nil, 0, nil, "issues.group.error.asset_mismatch"},
		{"no asset on both is still one asset", []Issue{{ID: 2, State: "reported"}, {ID: 1, State: "reported"}}, nil, 1, []int64{2}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := planIssueGroup(c.issues, c.hasChildren)
			if plan.Error != c.wantErr {
				t.Fatalf("Error = %q, want %q", plan.Error, c.wantErr)
			}
			if plan.Keep.ID != c.wantKeep {
				t.Errorf("Keep.ID = %d, want %d", plan.Keep.ID, c.wantKeep)
			}
			if len(plan.Join) != len(c.wantJoin) {
				t.Fatalf("Join = %d issues, want %d", len(plan.Join), len(c.wantJoin))
			}
			for i, id := range c.wantJoin {
				if plan.Join[i].ID != id {
					t.Errorf("Join[%d].ID = %d, want %d", i, plan.Join[i].ID, id)
				}
			}
		})
	}
}

// --- Picker ---

func TestIssueGroupForm_NonAdminRefused(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()
	h := newHandler(t, server)

	if rec := getIssueGroupPageAs(t, h.IssueGroupFormPage, "operator"); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin, got %d", rec.Code)
	}
}

func TestIssueGroupForm_ListsOnlyGroupableIssues(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "machine_id") {
					return []map[string]any{
						{"id": float64(4), "title": "Hija", "state": "reported", "machine_id": float64(4), "master_id": float64(3)},
					}
				}
				return []map[string]any{
					issueGroupRow(1, "Fuga en bomba", "reported", 5, 0),
					issueGroupRow(2, "Fuga en bomba bis", "reported", 5, 0),
					issueGroupRow(3, "Caso existente", "reported", 5, 0),
					issueGroupRow(4, "Hija", "reported", 5, 3),
					issueGroupRow(5, "Cerrada", "closed", 5, 0),
				}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	body := getIssueGroupPageAs(t, h.IssueGroupFormPage, "admin").Body.String()
	for _, want := range []string{"Fuga en bomba", "Fuga en bomba bis", "issues.group.title"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected the picker to contain %q", want)
		}
	}
	for _, unwanted := range []string{"Caso existente", "Hija", "Cerrada"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("expected %q to be excluded from the picker", unwanted)
		}
	}
}

// --- Grouping ---

func TestIssueGroup_Success_LinksChildrenToOldest(t *testing.T) {
	patched := map[int64]map[string]any{}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1":   groupEntity(map[string]any{"id": float64(1), "title": "A", "state": "reported", "asset_id": float64(5), "machine_id": float64(1)}),
		"GET /api/v1/issues/2":   groupEntity(map[string]any{"id": float64(2), "title": "B", "state": "reported", "asset_id": float64(5), "machine_id": float64(2)}),
		"GET /api/v1/issues/3":   groupEntity(map[string]any{"id": float64(3), "title": "C", "state": "reported", "asset_id": float64(5), "machine_id": float64(3)}),
		"PATCH /api/v1/issues/1": groupPatchRecorder(patched, 1),
		"PATCH /api/v1/issues/2": groupPatchRecorder(patched, 2),
		"PATCH /api/v1/issues/3": groupPatchRecorder(patched, 3),
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueGroupFormAs(t, h.IssueGroup, url.Values{"ids": {"3", "1", "2"}}, "admin")
	assertRedirectsTo(t, rec, "/issues/1")

	if _, ok := patched[1]; ok {
		t.Error("the case itself must not be patched")
	}
	for _, id := range []int64{2, 3} {
		body, ok := patched[id]
		if !ok {
			t.Fatalf("issue %d was never patched", id)
		}
		if body["master_id"] != 1.0 {
			t.Errorf("issue %d master_id = %v, want 1", id, body["master_id"])
		}
	}
}

func TestIssueGroup_TooFew_ShowsError(t *testing.T) {
	patched := map[int64]map[string]any{}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1":   groupEntity(map[string]any{"id": float64(1), "title": "A", "state": "reported", "asset_id": float64(5)}),
		"PATCH /api/v1/issues/1": groupPatchRecorder(patched, 1),
	})
	defer server.Close()
	h := newHandler(t, server)

	body := postIssueGroupFormAs(t, h.IssueGroup, url.Values{"ids": {"1"}}, "admin").Body.String()
	if !strings.Contains(body, "issues.group.error.too_few") {
		t.Error("expected the too-few error to be rendered")
	}
	if len(patched) != 0 {
		t.Error("a refused grouping must not write anything")
	}
}

func TestIssueGroup_ChildSelected_Refused(t *testing.T) {
	patched := map[int64]map[string]any{}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1":   groupEntity(map[string]any{"id": float64(1), "title": "A", "state": "reported", "asset_id": float64(5)}),
		"GET /api/v1/issues/2":   groupEntity(map[string]any{"id": float64(2), "title": "B", "state": "reported", "asset_id": float64(5), "master_id": float64(9)}),
		"PATCH /api/v1/issues/1": groupPatchRecorder(patched, 1),
		"PATCH /api/v1/issues/2": groupPatchRecorder(patched, 2),
	})
	defer server.Close()
	h := newHandler(t, server)

	body := postIssueGroupFormAs(t, h.IssueGroup, url.Values{"ids": {"1", "2"}}, "admin").Body.String()
	if !strings.Contains(body, "issues.group.error.child_selected") {
		t.Error("expected the child-selected error to be rendered")
	}
	if len(patched) != 0 {
		t.Error("a refused grouping must not write anything")
	}
}

func TestIssueGroup_NonAdminRefused(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()
	h := newHandler(t, server)

	if rec := postIssueGroupFormAs(t, h.IssueGroup, url.Values{"ids": {"1", "2"}}, "operator"); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin, got %d", rec.Code)
	}
}

// --- Detaching ---

func TestIssueUngroup_ClearsMasterID(t *testing.T) {
	patched := map[int64]map[string]any{}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/2":   groupEntity(map[string]any{"id": float64(2), "title": "B", "state": "in_progress", "master_id": float64(1)}),
		"PATCH /api/v1/issues/2": groupPatchRecorder(patched, 2),
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueUngroup, "2", "/issues/2/ungroup", url.Values{}, "admin")
	assertRedirectsTo(t, rec, "/issues/2")
	if patched[2]["master_id"] != 0.0 {
		t.Errorf("master_id = %v, want 0", patched[2]["master_id"])
	}
}

func TestIssueUngroup_NonAdminRefused(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()
	h := newHandler(t, server)

	if rec := postIssueForm(t, h.IssueUngroup, "2", "/issues/2/ungroup", url.Values{}, "operator"); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin, got %d", rec.Code)
	}
}

// --- Propagation ---

func TestIssueTransition_PropagatesToChildren(t *testing.T) {
	walked := []int64{}
	patched := map[int64]map[string]any{}

	routes := map[string]http.HandlerFunc{
		"GET /api/v1/issues/1":   groupEntity(map[string]any{"id": float64(1), "title": "Caso", "state": "in_progress", "machine_id": float64(1)}),
		"PATCH /api/v1/issues/1": groupPatchRecorder(patched, 1),
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				return []map[string]any{
					{"id": float64(2), "title": "B", "state": "in_progress", "machine_id": float64(2), "master_id": float64(1)},
					{"id": float64(3), "title": "C", "state": "in_progress", "machine_id": float64(3), "master_id": float64(1)},
				}
			})
		},
	}
	for _, id := range []int64{1, 2, 3} {
		routes[fmt.Sprintf("POST /api/v2/fsm/machine/%d/walk", id)] = groupWalkRecorder(&walked, id, "resolved")
		if id != 1 {
			routes[fmt.Sprintf("PATCH /api/v1/issues/%d", id)] = groupPatchRecorder(patched, id)
		}
	}
	server := mockOLU(t, routes)
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueResolve, "1", "/issues/1/resolve", url.Values{}, "operator")
	assertRedirectsTo(t, rec, "/issues/1")

	if len(walked) != 3 {
		t.Fatalf("expected the case and both children to walk, got %v", walked)
	}
	for _, id := range []int64{1, 2, 3} {
		if patched[id]["state"] != "resolved" {
			t.Errorf("issue %d state = %v, want resolved", id, patched[id]["state"])
		}
	}
}

func TestIssueReject_PropagatesReasonToChildren(t *testing.T) {
	patched := map[int64]map[string]any{}

	routes := map[string]http.HandlerFunc{
		"GET /api/v1/issues/1":   groupEntity(map[string]any{"id": float64(1), "title": "Caso", "state": "reported", "machine_id": float64(1)}),
		"PATCH /api/v1/issues/1": groupPatchRecorder(patched, 1),
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				return []map[string]any{
					{"id": float64(2), "title": "B", "state": "reported", "machine_id": float64(2), "master_id": float64(1)},
					{"id": float64(3), "title": "C", "state": "reported", "machine_id": float64(3), "master_id": float64(1)},
				}
			})
		},
	}
	walked := []int64{}
	for _, id := range []int64{1, 2, 3} {
		routes[fmt.Sprintf("POST /api/v2/fsm/machine/%d/walk", id)] = groupWalkRecorder(&walked, id, "rejected")
		if id != 1 {
			routes[fmt.Sprintf("PATCH /api/v1/issues/%d", id)] = groupPatchRecorder(patched, id)
		}
	}
	server := mockOLU(t, routes)
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueReject, "1", "/issues/1/reject", url.Values{"motivo": {"duplicado de #12"}}, "operator")
	assertRedirectsTo(t, rec, "/issues/1")

	if patched[1]["reject_reason"] != "duplicado de #12" {
		t.Fatalf("the case did not keep the reason: %v", patched[1])
	}
	for _, id := range []int64{2, 3} {
		if patched[id]["state"] != "rejected" {
			t.Errorf("child %d state = %v, want rejected", id, patched[id]["state"])
		}
		if patched[id]["reject_reason"] != "duplicado de #12" {
			t.Errorf("child %d reject_reason = %v, want the case's reason", id, patched[id]["reject_reason"])
		}
	}
}

// --- Blocking and display ---

func TestIssueDetail_ChildShowsCaseAndHidesActions(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/2": groupEntity(map[string]any{"id": float64(2), "title": "Hija", "state": "in_progress", "machine_id": float64(2), "master_id": float64(1)}),
		"GET /api/v1/issues/1": groupEntity(map[string]any{"id": float64(1), "title": "Caso madre", "state": "in_progress"}),
	})
	defer server.Close()
	h := newHandler(t, server)

	body := getIssueDetail(t, h.IssueDetail, "2", "admin").Body.String()
	if !strings.Contains(body, "issues.group.manage_here") {
		t.Error("expected the child to point at its case")
	}
	if !strings.Contains(body, "Caso madre") {
		t.Error("expected the case's title to be shown")
	}
	if strings.Contains(body, "/issues/2/resolve") || strings.Contains(body, "/issues/2/start") {
		t.Error("a child must not offer its own lifecycle actions")
	}
	if strings.Contains(body, "/issues/2/assign") {
		t.Error("a child must not be reassigned directly")
	}
	if !strings.Contains(body, "/issues/2/ungroup") {
		t.Error("an admin must be able to detach the child")
	}
}

func TestIssueDetail_CaseListsItsChildren(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/1": groupEntity(map[string]any{"id": float64(1), "title": "Caso", "state": "reported", "machine_id": float64(1)}),
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				return []map[string]any{
					{"id": float64(2), "title": "Hija del caso", "state": "reported", "machine_id": float64(2), "master_id": float64(1)},
				}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	body := getIssueDetail(t, h.IssueDetail, "1", "operator").Body.String()
	if !strings.Contains(body, "issues.group.children") {
		t.Error("expected the case to render its children section")
	}
	if !strings.Contains(body, "Hija del caso") {
		t.Error("expected the child's title in the section")
	}
}

func TestIssueAssign_ChildRefused(t *testing.T) {
	patched := map[int64]map[string]any{}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/issues/2":   groupEntity(map[string]any{"id": float64(2), "title": "B", "state": "reported", "master_id": float64(1)}),
		"PATCH /api/v1/issues/2": groupPatchRecorder(patched, 2),
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := postIssueForm(t, h.IssueAssign, "2", "/issues/2/assign", url.Values{"assignee": {"7"}}, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the form to be re-rendered, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "issues.group.error.child_locked") {
		t.Error("expected the child-locked error to be rendered")
	}
	if len(patched) != 0 {
		t.Error("a child's responsible must not be changed directly")
	}
}

func TestIssuesList_ShowsCaseAndChildMarkers(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			templateOQL(w, r, func(q string) []map[string]any {
				if strings.Contains(q, "COUNT(*)") {
					return []map[string]any{{"state": "reported", "c": float64(2)}}
				}
				if strings.Contains(q, "machine_id") {
					return []map[string]any{
						{"id": float64(2), "title": "Hija", "state": "reported", "machine_id": float64(2), "master_id": float64(1)},
					}
				}
				return []map[string]any{
					{"id": float64(1), "title": "Caso", "state": "reported", "machine_id": float64(1), "master_id": float64(0)},
					{"id": float64(2), "title": "Hija", "state": "reported", "machine_id": float64(2), "master_id": float64(1)},
				}
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issues", nil)
	req = req.WithContext(auth.ContextWithClaims(req.Context(),
		&auth.Claims{UserID: 42, TenantID: 1, Role: "operator"}))
	rec := httptest.NewRecorder()
	h.IssuesList(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "issues.group.case_of") {
		t.Error("expected the case row to say how many issues it groups")
	}
	if !strings.Contains(body, "issues.group.child_of: #1") {
		t.Error("expected the child row to name its case")
	}
}

func TestIssueGroupRoutes_Register(t *testing.T) {
	router, _ := buildRoutingTestRouter(t, true, true)
	mux := http.NewServeMux()
	router.MountIssuesRoutes(mux)
}
