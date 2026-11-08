package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIssueNew_ShowsSingleSLAEstimateCard confirms the create form renders
// exactly one card, with the endpoint wiring and the default medium-priority
// resolution already filled in server-side.
func TestIssueNew_ShowsSingleSLAEstimateCard(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()
	h := newHandler(t, server)

	rec := httptest.NewRecorder()
	h.IssueNew(rec, httptest.NewRequest("GET", "/issues/new", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if got := strings.Count(body, `data-issue-sla="1"`); got != 1 {
		t.Fatalf("expected exactly one SLA estimate card, got %d", got)
	}
	for _, want := range []string{
		`hx-get="/issues/sla-estimate"`,
		`hx-trigger="change from:#field-priority, change from:#field-asset_id"`,
		`hx-swap="outerHTML"`,
		`data-sla-minutes="4320"`,
		"issue.priority.medium",
		"issues.sla.estimate.title",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected create form to contain %q", want)
		}
	}
	if strings.Contains(body, "issues.sla.estimate.normal") || strings.Contains(body, "issues.sla.estimate.critical") {
		t.Error("create form still renders the old two-card SLA copy")
	}
}

// TestIssueSLAEstimate_NoAssetUsesPriorityDefault checks the partial's
// baseline path: medium priority with no asset resolves the normal-asset
// resolution of 4320 minutes (3 days).
func TestIssueSLAEstimate_NoAssetUsesPriorityDefault(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()
	h := newHandler(t, server)

	rec := httptest.NewRecorder()
	h.IssueSLAEstimate(rec, httptest.NewRequest("GET", "/issues/sla-estimate?priority=medium", nil))

	body := rec.Body.String()
	if !strings.Contains(body, `data-sla-minutes="4320"`) {
		t.Errorf("expected medium/normal resolution of 4320 minutes, got: %s", body)
	}
	if !strings.Contains(body, `data-priority="medium"`) {
		t.Errorf("expected the priority badge to follow the request, got: %s", body)
	}
	if got := strings.Count(body, `data-issue-sla="1"`); got != 1 {
		t.Errorf("expected exactly one card in the partial, got %d", got)
	}
}

// TestIssueSLAEstimate_MediumIsShorterThanLow keeps the two priorities
// visibly distinct: Medium is 3 days for a normal asset, Low stays 7.
func TestIssueSLAEstimate_MediumIsShorterThanLow(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()
	h := newHandler(t, server)

	cases := []struct {
		priority string
		want     string
	}{
		{"medium", `data-sla-minutes="4320"`},
		{"low", `data-sla-minutes="10080"`},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.IssueSLAEstimate(rec, httptest.NewRequest("GET", "/issues/sla-estimate?priority="+c.priority, nil))
		if body := rec.Body.String(); !strings.Contains(body, c.want) {
			t.Errorf("priority %s: expected %s, got: %s", c.priority, c.want, body)
		}
	}
}

// TestIssueSLAEstimate_AssetCriticalityChangesResolution confirms the
// partial reads the linked asset's criticality, not just its id: a High
// asset turns high priority from 2880 into 1440 minutes.
func TestIssueSLAEstimate_AssetCriticalityChangesResolution(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": float64(7), "name": "Pump 1", "criticality": "Alta",
			})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := httptest.NewRecorder()
	h.IssueSLAEstimate(rec, httptest.NewRequest("GET", "/issues/sla-estimate?priority=high&asset_id=7", nil))

	if body := rec.Body.String(); !strings.Contains(body, `data-sla-minutes="1440"`) {
		t.Errorf("expected high/critical resolution of 1440 minutes, got: %s", body)
	}
}

// TestIssueSLAEstimate_TemplateOverrideWins confirms the same resolution
// helper used on create also drives the card, so a template's own
// resolution override is shown rather than the code default.
func TestIssueSLAEstimate_TemplateOverrideWins(t *testing.T) {
	row := pumpFailureTemplateRow()
	row["sla_config"] = map[string]any{
		"resolution": map[string]any{"high": float64(120)},
	}
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": float64(7), "name": "Pump 1", "type_id": float64(3), "criticality": "Alta",
			})
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
	})
	defer server.Close()
	h := newHandler(t, server)

	rec := httptest.NewRecorder()
	h.IssueSLAEstimate(rec, httptest.NewRequest("GET", "/issues/sla-estimate?priority=high&asset_id=7", nil))

	if body := rec.Body.String(); !strings.Contains(body, `data-sla-minutes="120"`) {
		t.Errorf("expected the template's 120-minute override, got: %s", body)
	}
}

// TestFormatSLAEstimateDuration_UsesWholeUnits keeps the displayed time
// readable and exact: whole days/hours win, otherwise minutes are shown.
func TestFormatSLAEstimateDuration_UsesWholeUnits(t *testing.T) {
	translate := func(key string, args ...any) string { return key }
	cases := []struct {
		minutes int64
		want    string
	}{
		{1440, "1 issues.sla.estimate.unit.day"},
		{2880, "2 issues.sla.estimate.unit.days"},
		{120, "2 issues.sla.estimate.unit.hours"},
		{1, "1 issues.sla.estimate.unit.minute"},
		{90, "90 issues.sla.estimate.unit.minutes"},
	}
	for _, c := range cases {
		if got := formatSLAEstimateDuration(translate, c.minutes); got != c.want {
			t.Errorf("formatSLAEstimateDuration(%d) = %q, want %q", c.minutes, got, c.want)
		}
	}
}
