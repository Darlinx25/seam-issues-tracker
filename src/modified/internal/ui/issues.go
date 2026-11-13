package ui

// Issue Tracker module (2026-08-05) -- a simple, always-on module, not
// a gated playground like Work Orders was. Deliberately minimal: a
// title, a description, a plain open/closed status (no FSM, no guards
// -- "simple" was the explicit ask, and a two-state status field
// doesn't need xolu's own FSM primitives the way Work Orders' real
// lifecycle did), and an optional link to an asset via the
// asset-picker widget -- its third real consumer after parent_id and
// sensor binding, confirmed generic enough for this by the two-picker-
// per-page test written just before this module (T-72's own follow-up
// verification).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
	"github.com/ha1tch/seam-ui/internal/auth"
	fe "github.com/ha1tch/seam-ui/internal/formengine"
	issueschemas "github.com/ha1tch/seam-ui/internal/formengine/schemas/issues"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// Issue mirrors what's stored in the "issues" xolu entity.
type Issue struct {
	ID           int64
	Title        string
	Description  string
	Priority     string // "critical" | "high" | "medium" | "low"
	Severity     string // "critical" | "major" | "minor"
	State        string // "reported" | "triaged" | "in_progress" | "on_hold" | "resolved" | "closed" | "rejected" | "duplicate"
	RejectReason string
	MachineID    int64
	AssetID      int64
	AssetName    string // resolved, not stored -- populated on read for display
	Channel      string // "app" | "qr" | "api"; empty on rows created before it existed
	// TrackToken is the opaque token of the public tracking page. It is only
	// written by the anonymous QR report, so an empty value means "this issue
	// has no public link".
	TrackToken string
	// TrackExpiresAt is the RFC3339 deadline of the public link. It is written
	// only the first time the issue reaches a closing state; later closing
	// transitions must not extend it.
	TrackExpiresAt string
	ReportedBy     int64  // user id of the reporter, stored on the entity
	ReportedByName string // resolved, not stored -- populated on read for display
	Assignee       int64
	AssigneeName   string
	CreatedAt      string

	// TemplateID is the specific template version the issue was created
	// with; FormData holds the custom field values it captured. Both are
	// frozen onto the issue so later template edits don't alter history.
	TemplateID               int64
	FormData                 map[string]any
	AssetCategory            string
	SLAFirstResponseDeadline string
	SLAResolutionDeadline    string
	FirstResponseAt          string
	FirstRespondedBy         int64

	// MasterID links a child issue to the case that groups it (0 = not
	// grouped). ChildCount is resolved on read for display only: it counts
	// how many issues point at this one, i.e. whether it is a case.
	MasterID   int64
	ChildCount int
}

const issueDefName = "seam_issue_lifecycle"

func issueFromEntity(e *xolu.Entity) Issue {
	return Issue{
		ID:                       e.ID,
		Title:                    toString(e.Data["title"]),
		Description:              toString(e.Data["description"]),
		Priority:                 toString(e.Data["priority"]),
		Severity:                 toString(e.Data["severity"]),
		State:                    toString(e.Data["state"]),
		RejectReason:             toString(e.Data["reject_reason"]),
		MachineID:                toInt64(e.Data["machine_id"]),
		AssetID:                  toInt64(e.Data["asset_id"]),
		Channel:                  toString(e.Data["channel"]),
		TrackToken:               toString(e.Data["track_token"]),
		TrackExpiresAt:           toString(e.Data["track_expires_at"]),
		CreatedAt:                toString(e.Data["created_at"]),
		ReportedBy:               toInt64(e.Data["reported_by"]),
		Assignee:                 toInt64(e.Data["assignee"]),
		TemplateID:               toInt64(e.Data["template_id"]),
		FormData:                 toMap(e.Data["form_data"]),
		AssetCategory:            toString(e.Data["asset_category"]),
		SLAFirstResponseDeadline: toString(e.Data["sla_first_response_deadline"]),
		SLAResolutionDeadline:    toString(e.Data["sla_resolution_deadline"]),
		FirstResponseAt:          toString(e.Data["first_response_at"]),
		FirstRespondedBy:         toInt64(e.Data["first_responded_by"]),
		MasterID:                 toInt64(e.Data["master_id"]),
	}
}

// canActOnIssue reports whether the current user may run the lifecycle
// actions (start, resolve, close, reject) on the given issue. A child issue
// is never managed on its own: its lifecycle follows the case that groups it
// (see propagateIssueTransition), so the actions are refused here even for
// the responsible and for admins. Otherwise, an issue with a responsible
// belongs to that person: only they -- and the admins, who keep the last
// word -- may act on it, so a third party can neither start nor resolve nor
// reject an issue that was handed to someone else. An issue without a
// responsible keeps the previous behaviour: anyone with access to it may act.
func (h *UIHandler) canActOnIssue(r *http.Request, issue Issue) bool {
	if issue.MasterID > 0 {
		return false
	}
	u := h.getCurrentUser(r)
	if u == nil || u.ID == 0 {
		return false
	}
	if issue.Assignee <= 0 || issue.Assignee == u.ID {
		return true
	}
	return auth.RoleRank(u.Role) >= auth.RoleRank(auth.RoleAdmin)
}

// buildIssueHistory maps xolu's machine history into display entries,
// resolving each distinct actor id to a user name once. The machine's
// initial entry has no input; it is reported as "created" and attributed
// to the issue's reporter.
func (h *UIHandler) buildIssueHistory(ctx context.Context, entries []xolu.HistoryEntry, reporterName string) []IssueHistoryEntry {
	names := map[int64]string{}
	resolve := func(id int64) string {
		if id <= 0 {
			return ""
		}
		if name, ok := names[id]; ok {
			return name
		}
		name := ""
		if u, err := h.xoluClient.Get(ctx, "users", id); err == nil {
			name = toString(u.Data["name"])
		}
		names[id] = name
		return name
	}

	out := make([]IssueHistoryEntry, 0, len(entries))
	for _, e := range entries {
		item := IssueHistoryEntry{At: e.At, Action: "created", To: e.To}
		if e.From != nil {
			item.From = *e.From
		}
		if e.Input != nil {
			item.Action = *e.Input
		}
		if len(e.Payload) > 0 {
			var p map[string]any
			if json.Unmarshal(e.Payload, &p) == nil {
				item.ActorID = toInt64(p["actor"])
				item.Reason = toString(p["motivo"])
				item.Canonical = toInt64(p["canonical_id"])
			}
		}
		if item.Action == "created" {
			item.ActorName = reporterName
		} else {
			item.ActorName = resolve(item.ActorID)
		}
		out = append(out, item)
	}
	return out
}

// assetCategoryFromCriticality maps an asset's criticality to the issue's
// stored asset_category, per the canonical table (ROADMAP section 4):
// High -> "critico"; Medium/Low -> "normal". Any unrecognised value
// (including the empty string) defaults to "normal".
func assetCategoryFromCriticality(criticality string) string {
	switch criticality {
	case "High":
		return "critico"
	case "Medium", "Low":
		return "normal"
	default:
		return "normal"
	}
}

// assetCategoryFromStoredCriticality normalises the stored criticality token
// (Spanish in the assets entity: "Alta"/"Media"/"Baja") to the canonical
// English token and maps it to the issue category.
func assetCategoryFromStoredCriticality(raw string) string {
	canonical := toString(translateAssetEnumValue("criticality", raw, false))
	return assetCategoryFromCriticality(canonical)
}

// resolveAssetCategory reads the linked asset's stored criticality and
// resolves its category. Returns "normal" when there is no asset or when
// the asset cannot be read.
func (h *UIHandler) resolveAssetCategory(ctx context.Context, assetID int64) string {
	if assetID <= 0 {
		return "normal"
	}
	asset, err := h.xoluClient.Get(ctx, "assets", assetID)
	if err != nil {
		return "normal"
	}
	return assetCategoryFromStoredCriticality(toString(asset.Data["criticality"]))
}

// defaultSLAFirstResponseMinutes returns fallback minutes for first response
// based on priority + assetCategory (fixed in code).
func defaultSLAFirstResponseMinutes(priority, assetCategory string) int64 {
	switch assetCategory {
	case "critico":
		switch priority {
		case "critical":
			return 60
		case "high":
			return 120
		case "medium":
			return 240
		case "low":
			return 240
		default:
			return 120
		}
	default:
		switch priority {
		case "critical":
			return 120
		case "high":
			return 240
		case "medium":
			return 480
		case "low":
			return 480
		default:
			return 240
		}
	}
}

// defaultSLAResolutionMinutes returns fallback minutes for resolution.
func defaultSLAResolutionMinutes(priority, assetCategory string) int64 {
	switch assetCategory {
	case "critico":
		switch priority {
		case "critical":
			return 240
		case "high":
			return 1440
		case "medium":
			return 1440
		case "low":
			return 2880
		default:
			return 1440
		}
	default:
		switch priority {
		case "critical":
			return 1440
		case "high":
			return 2880
		case "medium":
			return 4320
		case "low":
			return 10080
		default:
			return 2880
		}
	}
}

func calculateSLADeadlineFromMinutes(minutes int64, now time.Time) string {
	if minutes <= 0 {
		return ""
	}
	return now.Add(time.Duration(minutes) * time.Minute).UTC().Format(time.RFC3339)
}

func slaMinutesFromTemplate(tpl *IssueTemplate, kind string, priority string) int64 {
	if tpl == nil || tpl.SLAConfig == nil {
		return 0
	}
	if m, ok := tpl.SLAConfig[kind]; ok {
		if v, ok := m[priority]; ok && v > 0 {
			return v
		}
	}
	return 0
}

// issueResolutionMinutes resolves the exact resolution time used when an
// issue is created: the assigned template wins, otherwise the priority and
// asset-category default applies.
func issueResolutionMinutes(tpl *IssueTemplate, priority, assetCategory string) int64 {
	if m := slaMinutesFromTemplate(tpl, "resolution", priority); m > 0 {
		return m
	}
	return defaultSLAResolutionMinutes(priority, assetCategory)
}

// formatSLAEstimateDuration renders an exact minute count in the largest
// whole unit that divides it, so the create form reads "7 días" instead of
// "10080m". Template overrides that are not whole days/hours stay in minutes.
func formatSLAEstimateDuration(t func(key string, args ...any) string, minutes int64) string {
	if minutes <= 0 {
		return "—"
	}
	if minutes%(24*60) == 0 {
		n := minutes / (24 * 60)
		if n == 1 {
			return "1 " + t("issues.sla.estimate.unit.day")
		}
		return fmt.Sprintf("%d %s", n, t("issues.sla.estimate.unit.days"))
	}
	if minutes%60 == 0 {
		n := minutes / 60
		if n == 1 {
			return "1 " + t("issues.sla.estimate.unit.hour")
		}
		return fmt.Sprintf("%d %s", n, t("issues.sla.estimate.unit.hours"))
	}
	if minutes == 1 {
		return "1 " + t("issues.sla.estimate.unit.minute")
	}
	return fmt.Sprintf("%d %s", minutes, t("issues.sla.estimate.unit.minutes"))
}

func formatDurationMinutes(m int64) string {
	if m < 0 {
		m = 0
	}
	if m == 0 {
		return "0m"
	}
	d := m / (24 * 60)
	h := (m % (24 * 60)) / 60
	mi := m % 60
	var parts []string
	if d > 0 {
		parts = append(parts, fmt.Sprintf("%dd", d))
	}
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if mi > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", mi))
	}
	return strings.Join(parts, " ")
}

func formatTimeShort(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Format("02/01 15:04")
}

// calculateSLADeadline computes the SLA deadline as a function of the
// issue priority and the stored asset category. The table is fixed in
// code per the canonical roadmap:
//
//	critical asset (critico): critical 4h, high 24h, medium 48h
//	normal asset (normal):    critical 24h, high 48h, medium 7d
//
// Any unrecognised combination falls back to a safe default.
func calculateSLADeadline(priority, assetCategory string, now time.Time) string {
	var d time.Duration
	switch assetCategory {
	case "critico":
		switch priority {
		case "critical":
			d = 4 * time.Hour
		case "high":
			d = 24 * time.Hour
		case "medium":
			d = 24 * time.Hour
		case "low":
			d = 48 * time.Hour
		default:
			d = 24 * time.Hour
		}
	default:
		switch priority {
		case "critical":
			d = 24 * time.Hour
		case "high":
			d = 48 * time.Hour
		case "medium":
			d = 3 * 24 * time.Hour
		case "low":
			d = 7 * 24 * time.Hour
		default:
			d = 48 * time.Hour
		}
	}
	return now.Add(d).UTC().Format(time.RFC3339)
}

// resolveUserName resolves a user id to its display name, reading the
// "users" entity directly. Returns an empty string when the user is gone.
func (h *UIHandler) resolveUserName(ctx context.Context, id int64) string {
	if id <= 0 || h.xoluClient == nil {
		return ""
	}
	u, err := h.xoluClient.Get(ctx, "users", id)
	if err != nil {
		return ""
	}
	if name := toString(u.Data["name"]); name != "" {
		return name
	}
	return toString(u.Data["email"])
}

// resolveUserNames resolves several user ids to their display names with a
// single OQL query, so rendering a list of rows doesn't turn into one
// request per row. Ids that can't be resolved are simply left out of the
// map. The query is skipped entirely when there is no id to resolve.
func (h *UIHandler) resolveUserNames(ctx context.Context, ids []int64) map[int64]string {
	names := map[int64]string{}
	if h.xoluClient == nil {
		return names
	}
	seen := map[int64]bool{}
	parts := []string{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	if len(parts) == 0 {
		return names
	}
	query := "SELECT id, name, email FROM users WHERE id IN (" + strings.Join(parts, ", ") + ")"
	res, err := h.xoluClient.OQL(ctx, query)
	if err != nil {
		h.logger.Error("failed to resolve user names", "error", err)
		return names
	}
	for _, row := range res.Data {
		id := toInt64(row["id"])
		if id <= 0 {
			continue
		}
		name := toString(row["name"])
		if name == "" {
			name = toString(row["email"])
		}
		if name != "" {
			names[id] = name
		}
	}
	return names
}

// IssuesList handles GET /issues.
func (h *UIHandler) IssuesList(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()

	filters := parseIssueListFilters(r.URL.Query())

	var meID int64
	if u := h.getCurrentUser(r); u != nil {
		meID = u.ID
	}

	// An asset filter typed as a name resolves to asset ids once. The text
	// search does the same so it keeps matching the linked asset's name, as
	// it did while the filtering ran client-side.
	var filterAssetIDs, searchAssetIDs []int64
	if filters.Asset != "" && !validOQLIntFilter(filters.Asset) {
		filterAssetIDs = h.issueAssetIDsByName(ctx, filters.Asset, 100)
	}
	if filters.Search != "" {
		searchAssetIDs = h.issueAssetIDsByName(ctx, filters.Search, 50)
	}

	conds := filters.conditions(meID, filterAssetIDs, searchAssetIDs)
	allConds := filters.withState(conds)

	// 1) Counts per state, with every filter except the state itself, so the
	// tabs say how many issues are behind each one under the current filters.
	counts := map[string]int{}
	for _, st := range issueStateOrder {
		counts[st] = 0
	}
	if groupRes, err := h.xoluClient.OQL(ctx, issueStateCountsQuery(conds)); err == nil {
		for _, row := range groupRes.Data {
			st := toString(row["state"])
			if _, ok := counts[st]; ok {
				counts[st] = int(toInt64(row["c"]))
			}
		}
	} else {
		h.logger.Error("failed to count issues by state", "error", err)
	}

	// 2) Total matching rows. The page is clamped against it so a stale
	// ?page= lands on the last page instead of an empty one.
	total := 0
	if totalRes, err := h.xoluClient.OQL(ctx, issueListCountQuery(allConds)); err == nil && len(totalRes.Data) > 0 {
		total = int(toInt64(totalRes.Data[0]["c"]))
	} else if err != nil {
		h.logger.Error("failed to count issues", "error", err)
	}
	totalPages := issueListTotalPages(total, issueListPageSize)
	if filters.Page > totalPages {
		filters.Page = totalPages
	}
	if filters.Page < 1 {
		filters.Page = 1
	}

	// 3) The page of rows itself.
	var rows []map[string]any
	var err error
	if filters.Sort == issueSortPriority || filters.Sort == issueSortSLA {
		rows, err = h.issueListPageByBuckets(ctx, allConds, issueListBuckets(filters.Sort), filters.Page, issueListPageSize)
		if err != nil {
			h.logger.Error("failed to list issues by bucket", "sort", filters.Sort, "error", err)
			filters.Sort = ""
			result, fallbackErr := h.xoluClient.OQL(ctx, issueListPageQuery(allConds, filters.Sort, filters.Page, issueListPageSize))
			err = fallbackErr
			if fallbackErr == nil {
				rows = result.Data
			}
		}
	} else {
		result, queryErr := h.xoluClient.OQL(ctx, issueListPageQuery(allConds, filters.Sort, filters.Page, issueListPageSize))
		err = queryErr
		if queryErr == nil {
			rows = result.Data
		}
	}
	var issues []Issue
	if err == nil {
		for _, row := range rows {
			issues = append(issues, Issue{
				ID:                    toInt64(row["id"]),
				Title:                 toString(row["title"]),
				Priority:              toString(row["priority"]),
				Severity:              toString(row["severity"]),
				State:                 toString(row["state"]),
				Channel:               toString(row["channel"]),
				MachineID:             toInt64(row["machine_id"]),
				AssetID:               toInt64(row["asset_id"]),
				Assignee:              toInt64(row["assignee"]),
				MasterID:              toInt64(row["master_id"]),
				SLAResolutionDeadline: toString(row["sla_resolution_deadline"]),
			})
		}
	} else {
		h.logger.Error("failed to list issues", "error", err)
	}

	// 4) Resolve linked asset names (unchanged behavior)
	for i := range issues {
		if issues[i].AssetID > 0 {
			if asset, err := h.xoluClient.Get(ctx, "assets", issues[i].AssetID); err == nil {
				issues[i].AssetName = toString(asset.Data["name"])
			}
		}
	}

	// 5) Resolve the assignees' names in one query, so the list can show
	// who is responsible for each issue without a request per row.
	assigneeIDs := make([]int64, 0, len(issues))
	for _, iss := range issues {
		assigneeIDs = append(assigneeIDs, iss.Assignee)
	}
	assigneeNames := h.resolveUserNames(ctx, assigneeIDs)
	for i := range issues {
		issues[i].AssigneeName = assigneeNames[issues[i].Assignee]
	}

	// 6) Resolve the grouping links in a single read: the list shows each
	// case's child count and each child's case from the same result, so a
	// hundred rows don't become a hundred requests.
	childCounts := map[int64]int{}
	if links, err := h.listIssueLinks(ctx); err == nil {
		for _, l := range links {
			childCounts[l.MasterID]++
		}
	} else {
		h.logger.Error("failed to load issue grouping links", "error", err)
	}
	for i := range issues {
		issues[i].ChildCount = childCounts[issues[i].ID]
	}

	data := IssuesListData{
		Issues:   issues,
		Filters:  filters,
		Counts:   counts,
		Total:    total,
		PageSize: issueListPageSize,
		CanGroup: h.canManageIssueGroups(r),
	}
	h.page.Render(w, r, t("issues.list_title"), "/issues", IssuesListPage(data, t))
}

// issueAssetIDsByName resolves an asset-name fragment to asset ids, so the
// list can filter by asset without asking the operator for a numeric id.
func (h *UIHandler) issueAssetIDsByName(ctx context.Context, name string, limit int) []int64 {
	name = strings.TrimSpace(name)
	if name == "" || h.xoluClient == nil {
		return nil
	}
	query := "SELECT TOP " + strconv.Itoa(limit) + " id FROM assets WHERE name LIKE '%" + escapeOQL(name) + "%'"
	res, err := h.xoluClient.OQL(ctx, query)
	if err != nil {
		h.logger.Error("failed to resolve asset ids by name", "error", err)
		return nil
	}
	ids := make([]int64, 0, len(res.Data))
	for _, row := range res.Data {
		if id := toInt64(row["id"]); id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func issueFormDefaults() map[string]interface{} {
	return map[string]interface{}{"priority": "medium", "severity": "minor"}
}

// IssueNew handles GET /issues/new.
func (h *UIHandler) IssueNew(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	csrfToken := h.GetCSRFToken(w, r)

	formEng := &fe.FormEngine{Translator: h.formEngineTranslator()}
	rc := fe.RenderContext{Locale: h.currentLocale(), CSRFToken: csrfToken, Module: "issues", Form: "issue"}
	formContent := formEng.Render(ctx, resolveSchema(ctx, "issues", "create", issueschemas.Schema("create")), issueFormDefaults(), nil, rc)

	data := IssueFormData{
		CSRFToken:         csrfToken,
		FormContent:       formContent,
		Priority:          "medium",
		ResolutionMinutes: defaultSLAResolutionMinutes("medium", "normal"),
	}
	h.page.Render(w, r, t("issues.new"), "/issues/new", IssueFormPage(data, t))
}

// IssueSLAEstimate handles GET /issues/sla-estimate -- the HTMX partial that
// refreshes the create form's resolution estimate when the priority or the
// selected asset changes. Resolving the asset here keeps the displayed time
// identical to the deadline stored on creation, including template overrides.
func (h *UIHandler) IssueSLAEstimate(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	priority := r.URL.Query().Get("priority")
	switch priority {
	case "critical", "high", "medium", "low":
	default:
		priority = "medium"
	}
	assetID := toInt64FromString(r.URL.Query().Get("asset_id"))
	var tpl *IssueTemplate
	assetCategory := "normal"
	if assetID > 0 {
		tpl = h.resolveIssueTemplate(r.Context(), assetID)
		assetCategory = h.resolveAssetCategory(r.Context(), assetID)
	}
	minutes := issueResolutionMinutes(tpl, priority, assetCategory)
	_ = h.page.RenderPartial(w, issueSLAEstimate(minutes, priority, t))
}

// IssueCreate handles POST /issues.
func (h *UIHandler) IssueCreate(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	title := r.FormValue("title")
	description := r.FormValue("description")
	assetIDStr := r.FormValue("asset_id")
	priority := r.FormValue("priority")
	severity := r.FormValue("severity")
	csrfToken := h.GetCSRFToken(w, r)
	currentUser := h.getCurrentUser(r)

	if priority == "" {
		priority = "medium"
	}
	if severity == "" {
		severity = "minor"
	}

	// Resolve the template assigned to the selected asset's type (if any)
	// and collect its custom field values.
	assetID := toInt64FromString(assetIDStr)
	var tpl *IssueTemplate
	if assetID > 0 {
		tpl = h.resolveIssueTemplate(ctx, assetID)
	}
	formData := map[string]any{}
	fieldErrors := map[string]string{}
	if tpl != nil {
		for _, f := range tpl.Fields {
			v := r.FormValue(f.Key)
			formData[f.Key] = v
			if f.Required && strings.TrimSpace(v) == "" {
				fieldErrors[f.Key] = t("field.required")
			}
		}
	}

	errors := map[string]string{}
	if title == "" {
		errors["title"] = t("issues.error.title_required")
	}
	if len(errors) > 0 || len(fieldErrors) > 0 {
		formEng := &fe.FormEngine{Translator: h.formEngineTranslator()}
		rc := fe.RenderContext{Locale: h.currentLocale(), CSRFToken: csrfToken, Module: "issues", Form: "issue"}
		values := issueFormDefaults()
		values["title"] = title
		values["description"] = description
		values["asset_id"] = assetIDStr
		if priority != "" {
			values["priority"] = priority
		}
		if severity != "" {
			values["severity"] = severity
		}
		formContent := formEng.Render(ctx, resolveSchema(ctx, "issues", "create", issueschemas.Schema("create")), values, errors, rc)
		var tplContent mi.H
		if tpl != nil && len(tpl.Fields) > 0 {
			tplContent = h.renderTemplateFields(ctx, tpl, formData, fieldErrors, csrfToken)
		}
		resolutionMinutes := defaultSLAResolutionMinutes(priority, "normal")
		if assetID > 0 {
			resolutionMinutes = issueResolutionMinutes(tpl, priority, h.resolveAssetCategory(ctx, assetID))
		}
		data := IssueFormData{
			CSRFToken:             csrfToken,
			FormContent:           formContent,
			Errors:                errors,
			TemplateFieldsContent: tplContent,
			Priority:              priority,
			ResolutionMinutes:     resolutionMinutes,
		}
		h.page.Render(w, r, t("issues.new"), "/issues/new", IssueFormPage(data, t))
		return
	}

	defID, err := h.ensureIssueFSMDefinition(ctx)
	if err != nil {
		h.logger.Error("failed to bootstrap issue FSM definition", "error", err)
		http.Error(w, "Failed to initialize issue workflow", http.StatusInternalServerError)
		return
	}

	machine, err := h.xoluClient.CreateMachine(ctx, xolu.CreateMachineRequest{Definition: defID})
	if err != nil {
		h.logger.Error("failed to create issue FSM machine", "error", err)
		http.Error(w, "Failed to create issue workflow", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	issueData := map[string]any{
		"title":       title,
		"description": description,
		"priority":    priority,
		"severity":    severity,
		"state":       machine.State,
		"machine_id":  machine.ID,
		"channel":     "app",
		"created_at":  now.Format(time.RFC3339),
	}
	if assetID > 0 {
		issueData["asset_id"] = assetID
	}
	assetCategory := h.resolveAssetCategory(ctx, assetID)
	issueData["asset_category"] = assetCategory

	firstRespMin := slaMinutesFromTemplate(tpl, "first_response", priority)
	if firstRespMin <= 0 {
		firstRespMin = defaultSLAFirstResponseMinutes(priority, assetCategory)
	}
	resMin := issueResolutionMinutes(tpl, priority, assetCategory)
	if d := calculateSLADeadlineFromMinutes(firstRespMin, now); d != "" {
		issueData["sla_first_response_deadline"] = d
	}
	if d := calculateSLADeadlineFromMinutes(resMin, now); d != "" {
		issueData["sla_resolution_deadline"] = d
	}
	if tpl != nil {
		issueData["template_id"] = tpl.ID
		issueData["form_data"] = formData
	}
	if currentUser != nil && currentUser.ID > 0 {
		issueData["reported_by"] = currentUser.ID
		issueData["assignee"] = currentUser.ID
	}

	entity, err := h.xoluClient.Create(ctx, "issues", issueData)
	if err != nil {
		h.logger.Error("failed to create issue", "error", err)
		http.Error(w, "Failed to save issue", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/issues/"+strconv.FormatInt(entity.ID, 10), http.StatusSeeOther)
}

// IssueDetail handles GET /issues/{id}.
func (h *UIHandler) IssueDetail(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}

	entity, err := h.xoluClient.Get(ctx, "issues", id)
	if err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}

	issue := issueFromEntity(entity)
	var assetDetail IssueDetailData
	if issue.AssetID > 0 {
		if asset, err := h.xoluClient.Get(ctx, "assets", issue.AssetID); err == nil {
			issue.AssetName = toString(asset.Data["name"])
			assetDetail.AssetCategory = assetCategoryFromStoredCriticality(toString(asset.Data["criticality"]))
			if typeID := toInt64(asset.Data["type_id"]); typeID > 0 {
				if at, err := h.xoluClient.Get(ctx, "asset_types", typeID); err == nil {
					assetDetail.AssetTypeName = toString(at.Data["name"])
				}
			}
			if locID := toInt64(asset.Data["location_id"]); locID > 0 {
				if loc, err := h.xoluClient.Get(ctx, "locations", locID); err == nil {
					assetDetail.AssetLocationName = toString(loc.Data["short_name"])
				}
			}
		} else {
			assetDetail.AssetMissing = true
		}
	}
	if issue.ReportedBy > 0 {
		if u, err := h.xoluClient.Get(ctx, "users", issue.ReportedBy); err == nil {
			issue.ReportedByName = toString(u.Data["name"])
		}
	}
	if issue.Assignee > 0 {
		issue.AssigneeName = h.resolveUserName(ctx, issue.Assignee)
	}

	data := assetDetail
	data.Issue = issue
	data.CSRFToken = h.GetCSRFToken(w, r)
	data.CanAssign = h.canAssignIssue(r, issue)
	data.CanAct = h.canActOnIssue(r, issue)
	data.CanUngroup = issue.MasterID > 0 && h.canManageIssueGroups(r)
	if issue.TrackToken != "" {
		data.TrackingURL = h.issueTrackingLink(issue.TrackToken)
	}

	// Grouping is resolved on read for display only: a child points at its
	// case, a case lists its children. Neither is stored twice.
	if issue.MasterID > 0 {
		if master, err := h.xoluClient.Get(ctx, "issues", issue.MasterID); err == nil {
			data.MasterTitle = toString(master.Data["title"])
		} else {
			h.logger.Error("failed to load the case of an issue", "id", id, "master_id", issue.MasterID, "error", err)
		}
	} else if links, err := h.listIssueLinks(ctx); err == nil {
		for _, l := range links {
			if l.MasterID == issue.ID {
				data.Children = append(data.Children, l)
			}
		}
	} else {
		h.logger.Error("failed to load the children of an issue", "id", id, "error", err)
	}
	if issue.TemplateID > 0 {
		if tplEntity, err := h.xoluClient.Get(ctx, issueTemplatesEntity, issue.TemplateID); err == nil {
			tpl := issueTemplateFromRow(tplEntity.Data)
			data.TemplateName = tpl.Name
			for _, f := range tpl.Fields {
				data.FormFields = append(data.FormFields, IssueFormField{
					Key:   f.Key,
					Label: f.Label,
					Value: toString(issue.FormData[f.Key]),
				})
			}
		}
	}

	if issue.MachineID > 0 {
		if hist, err := h.xoluClient.GetMachineHistory(ctx, issue.MachineID); err == nil {
			data.History = h.buildIssueHistory(ctx, hist, issue.ReportedByName)
		} else {
			h.logger.Error("failed to load issue history", "id", id, "error", err)
		}
	}

	h.page.Render(w, r, t("issues.detail_title"), "/issues", IssueDetailPage(data, t))
}

// IssueStart handles POST /issues/{id}/start -- walks from "reported" to "in_progress".
func (h *UIHandler) IssueStart(w http.ResponseWriter, r *http.Request) {
	h.IssueTransition(w, r, "start")
}

// IssueResolve handles POST /issues/{id}/resolve -- walks from "in_progress" to "closed".
func (h *UIHandler) IssueResolve(w http.ResponseWriter, r *http.Request) {
	h.IssueTransition(w, r, "resolve")
}

// IssueClose handles POST /issues/{id}/close -- walks from "resolved" to "closed".
func (h *UIHandler) IssueClose(w http.ResponseWriter, r *http.Request) {
	h.IssueTransition(w, r, "close")
}

// IssueTransition is the shared fetch-walk-denormalize sequence.
func (h *UIHandler) IssueTransition(w http.ResponseWriter, r *http.Request, input string) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}

	entity, err := h.xoluClient.Get(ctx, "issues", id)
	if err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	issue := issueFromEntity(entity)

	// The lifecycle actions belong to the responsible: a third party may
	// neither start, resolve nor close an issue that was handed to someone
	// else. Admins keep the last word (see canActOnIssue).
	if !h.canActOnIssue(r, issue) {
		h.logger.Info("issue transition refused: not the responsible", "id", id, "input", input, "assignee", issue.Assignee)
		h.Error403(w, r)
		return
	}

	// Mark first response only once when starting
	if input == "start" && issue.FirstResponseAt == "" {
		if u := h.getCurrentUser(r); u != nil && u.ID > 0 {
			now := time.Now().UTC()
			patch := map[string]any{
				"first_response_at":  now.Format(time.RFC3339),
				"first_responded_by": u.ID,
			}
			if _, err := h.xoluClient.Patch(ctx, "issues", issue.ID, patch); err != nil {
				h.logger.Error("failed to mark first response", "id", issue.ID, "error", err)
			} else {
				issue.FirstResponseAt = patch["first_response_at"].(string)
				issue.FirstRespondedBy = u.ID
			}
		}
	}

	var payload map[string]interface{}
	if u := h.getCurrentUser(r); u != nil && u.ID > 0 {
		payload = map[string]interface{}{"actor": u.ID}
	}
	result, err := h.xoluClient.WalkMachine(ctx, issue.MachineID, xolu.WalkRequest{Input: input, Payload: payload})
	if err != nil {
		h.logger.Info("issue walk rejected", "id", id, "input", input, "error", err)
		csrfToken := h.GetCSRFToken(w, r)
		data := IssueDetailData{Issue: issue, CSRFToken: csrfToken, WalkError: err.Error(), CanAssign: h.canAssignIssue(r, issue), CanAct: h.canActOnIssue(r, issue)}
		h.page.Render(w, r, t("issues.detail_title"), "/issues", IssueDetailPage(data, t))
		return
	}

	now := time.Now().UTC()
	patchData := map[string]any{
		"state":      result.Current,
		"updated_at": now.Format(time.RFC3339),
	}
	// The clock starts once. A later close after resolve must not extend the
	// reporter's access window; recalculating here would do exactly that.
	if issue.TrackToken != "" && issue.TrackExpiresAt == "" {
		if expires := issueTrackExpiry(result.Current, now); expires != "" {
			patchData["track_expires_at"] = expires
		}
	}
	if _, err := h.xoluClient.Patch(ctx, "issues", id, patchData); err != nil {
		h.logger.Error("issue walked but state patch failed", "id", id, "error", err)
	}

	// The case is the single management point: the same input is replayed
	// on every child, so the whole group keeps one state.
	if issue.MasterID == 0 {
		h.propagateIssueTransition(ctx, issue, input, payload, nil)
	}

	http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// IssueRejectFormPage handles GET /issues/{id}/reject -- renders the
// reject form. A reason is mandatory; enforced server-side on submit.
// Rejecting belongs to the responsible, so anyone else gets a 403.
func (h *UIHandler) IssueRejectFormPage(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	entity, err := h.xoluClient.Get(ctx, "issues", id)
	if err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	issue := issueFromEntity(entity)
	if !h.canActOnIssue(r, issue) {
		h.logger.Info("issue reject form refused: not the responsible", "id", id, "assignee", issue.Assignee)
		h.Error403(w, r)
		return
	}
	data := IssueRejectData{Issue: issue, CSRFToken: h.GetCSRFToken(w, r)}
	h.page.Render(w, r, t("issues.reject.title"), "/issues", IssueRejectPage(data, t))
}

// IssueReject handles POST /issues/{id}/reject -- walks the FSM machine
// with input "reject" carrying the motivo as payload, then denormalizes
// state and reject_reason onto the entity. An empty motivo re-renders
// the form with an error and never touches the FSM.
func (h *UIHandler) IssueReject(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	motivo := strings.TrimSpace(r.FormValue("motivo"))

	entity, err := h.xoluClient.Get(ctx, "issues", id)
	if err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	issue := issueFromEntity(entity)

	// Rejecting is a lifecycle action: it belongs to the responsible, like
	// start and resolve (see canActOnIssue).
	if !h.canActOnIssue(r, issue) {
		h.logger.Info("issue reject refused: not the responsible", "id", id, "assignee", issue.Assignee)
		h.Error403(w, r)
		return
	}

	if motivo == "" {
		h.logger.Info("issue reject refused: motivo required", "id", id)
		data := IssueRejectData{Issue: issue, CSRFToken: h.GetCSRFToken(w, r), Error: t("issues.reject.error.motivo_required")}
		h.page.Render(w, r, t("issues.reject.title"), "/issues", IssueRejectPage(data, t))
		return
	}

	payload := map[string]interface{}{"motivo": motivo}
	if u := h.getCurrentUser(r); u != nil && u.ID > 0 {
		payload["actor"] = u.ID
	}
	result, err := h.xoluClient.WalkMachine(ctx, issue.MachineID, xolu.WalkRequest{
		Input:   "reject",
		Payload: payload,
	})
	if err != nil {
		h.logger.Info("issue reject walk failed", "id", id, "error", err)
		data := IssueDetailData{Issue: issue, CSRFToken: h.GetCSRFToken(w, r), WalkError: err.Error(), CanAssign: h.canAssignIssue(r, issue), CanAct: h.canActOnIssue(r, issue)}
		h.page.Render(w, r, t("issues.detail_title"), "/issues", IssueDetailPage(data, t))
		return
	}

	now := time.Now().UTC()
	patchData := map[string]any{
		"state":         result.Current,
		"reject_reason": motivo,
		"updated_at":    now.Format(time.RFC3339),
	}
	if issue.TrackToken != "" && issue.TrackExpiresAt == "" {
		if expires := issueTrackExpiry(result.Current, now); expires != "" {
			patchData["track_expires_at"] = expires
		}
	}
	if _, err := h.xoluClient.Patch(ctx, "issues", id, patchData); err != nil {
		h.logger.Error("issue rejected but patch failed", "id", id, "error", err)
	}

	// Rejecting a case rejects the whole group: the reason travels with it.
	if issue.MasterID == 0 {
		h.propagateIssueTransition(ctx, issue, "reject", payload, map[string]any{"reject_reason": motivo})
	}

	http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// IssuesListData holds data for the issues list page.
type IssuesListData struct {
	Issues   []Issue
	Filters  IssueListFilters
	Counts   map[string]int
	Total    int
	PageSize int

	// CanGroup tells the page whether to offer the administrative "group
	// into a case" action: admins only (see canManageIssueGroups).
	CanGroup bool
}

// IssuesListPage renders the issues list page.
func IssuesListPage(data IssuesListData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	now := time.Now().UTC()
	items := make([]SimpleListItem, len(data.Issues))
	for i := range data.Issues {
		iss := data.Issues[i]
		assetLine := t("issues.no_asset")
		if iss.AssetName != "" {
			assetLine = t("issues.linked_to") + ": " + iss.AssetName
		}
		assigneeLine := t("issues.assignee") + ": " + t("issues.unassigned")
		if iss.AssigneeName != "" {
			assigneeLine = t("issues.assignee") + ": " + iss.AssigneeName
		}
		assigneeLine += " · " + t(issueChannelLabelKey(iss.Channel))
		if iss.ChildCount > 0 {
			assigneeLine += " · " + t("issues.group.case_of") + ": " + strconv.Itoa(iss.ChildCount)
		}
		if iss.MasterID > 0 {
			assigneeLine += " · " + t("issues.group.child_of") + ": #" + strconv.FormatInt(iss.MasterID, 10)
		}

		row := iss
		badges := mi.H(func(b *mi.Builder) mi.Node {
			children := []interface{}{mi.Class("flex items-center gap-2")}
			children = append(children, issueStatusBadge(row.State, t)(b))
			if row.Priority != "" {
				children = append(children,
					Badge(t("issues.priority."+row.Priority), issuePriorityVariant(row.Priority))(b))
			}
			return b.Div(children...)
		})

		items[i] = SimpleListItem{
			Href:     fmt.Sprintf("/issues/%d", iss.ID),
			Title:    iss.Title,
			Subtitle: assetLine,
			Meta:     assigneeLine,
			Badge:    badges,
			Aside:    issueSLAIndicator(row.SLAResolutionDeadline, row.State, now, t),
		}
	}

	return func(b *mi.Builder) mi.Node {
		cfg := ListPageConfig{
			Title:       t("issues.list_title"),
			Subtitle:    t("issues.list.subtitle"),
			CreateURL:   "/issues/new",
			CreateLabel: t("issues.new"),
			CreateIcon:  "add",
		}
		if data.CanGroup {
			cfg.SecondaryURL = "/issues/group"
			cfg.SecondaryLabel = t("issues.group.button")
			cfg.SecondaryIcon = "group_work"
		}

		var content mi.H
		if len(items) == 0 {
			icon := "assignment"
			title := t("issues.empty")
			description := t("issues.empty_hint")
			var action mi.H
			if data.Filters.Active() {
				icon = "search_off"
				title = t("issues.filter.empty_title")
				description = t("issues.filter.empty")
				action = func(b *mi.Builder) mi.Node {
					return b.A(mi.Href(data.Filters.clearFiltersLink()), mi.Class("text-sm font-medium text-indigo-600 hover:text-indigo-700 dark:text-indigo-400"), t("issues.filter.clear"))
				}
			}
			content = func(b *mi.Builder) mi.Node {
				return b.Div(mi.Class("bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700"),
					EmptyState(icon, title, description, action)(b),
				)
			}
		} else {
			content = func(b *mi.Builder) mi.Node {
				return b.Div(mi.ID("issue-list"), SimpleListCard(items, "")(b))
			}
		}

		pagination := issueListPagination(data.Filters, len(items), data.Total, t)
		return ListPage(cfg,
			nil,
			issueListWorkspace(data.Filters, data.Counts, content, pagination, t),
			nil,
		)(b)
	}
}

// issueStatusBadge maps this module's own status vocabulary to the
// shared Badge(text, variant) primitive (internal/ui/components.go) --
// "warning" (amber) for open, Badge's own default (gray) for closed,
// since "closed" doesn't match any of Badge's known variant keywords.
// Was a hand-rolled duplicate of Badge's own pill markup until
// 2026-08-06; now a thin mapping, no markup of its own.
func issueStatusBadge(state string, t func(key string, args ...any) string) mi.H {
	switch state {
	case "in_progress":
		return Badge(t("issues.state.in_progress"), "info")
	case "closed":
		return Badge(t("issues.state.closed"), "success")
	case "triaged":
		return Badge(t("issues.state.triaged"), "warning")
	case "on_hold":
		return Badge(t("issues.state.on_hold"), "warning")
	case "resolved":
		return Badge(t("issues.state.resolved"), "info")
	case "rejected":
		return Badge(t("issues.state.rejected"), "danger")
	case "duplicate":
		return Badge(t("issues.state.duplicate"), "danger")
	default:
		return Badge(t("issues.state.reported"), "warning")
	}
}

// IssueFormData holds data for the create-issue form.
type IssueFormData struct {
	CSRFToken   string
	FormContent mi.H
	Errors      map[string]string

	// Priority and ResolutionMinutes drive the single resolution-estimate
	// card on first paint and after a validation error, so the form still
	// shows the right time without waiting for the HTMX partial.
	Priority          string
	ResolutionMinutes int64

	// TemplateFieldsContent is the pre-rendered custom-field block of the
	// resolved template, used when re-rendering after a validation error so
	// the values seen before submit are preserved. Nil on a fresh GET, where
	// the block is loaded over HTMX from the asset picker.
	TemplateFieldsContent mi.H
}

// IssueFormPage renders the create-issue form.
func IssueFormPage(data IssueFormData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	priority := data.Priority
	switch priority {
	case "critical", "high", "medium", "low":
	default:
		priority = "medium"
	}
	resolutionMinutes := data.ResolutionMinutes
	if resolutionMinutes <= 0 {
		resolutionMinutes = defaultSLAResolutionMinutes(priority, "normal")
	}
	return func(b *mi.Builder) mi.Node {
		return b.Div(mi.Class("space-y-6 max-w-2xl"),
			b.Div(mi.Class("flex items-center gap-4"),
				b.A(mi.Href("/issues"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.new")),
			),
			Card("", func(b *mi.Builder) mi.Node {
				tplFields := []interface{}{
					mi.ID("issue-template-fields"),
					mi.Attr("hx-get", "/issue-templates/fields"),
					mi.Attr("hx-trigger", "change from:#field-asset_id"),
					mi.Attr("hx-target", "this"),
					mi.Attr("hx-include", "#field-asset_id"),
				}
				if data.TemplateFieldsContent != nil {
					tplFields = append(tplFields, data.TemplateFieldsContent(b))
				}
				return b.Form(mi.Class("space-y-4"), mi.Method("post"), mi.Action("/issues"),
					data.FormContent(b),
					b.Div(tplFields...),
					issueSLAEstimate(resolutionMinutes, priority, t)(b),
					b.Div(mi.Class("flex justify-end gap-3 pt-4"),
						b.A(mi.Href("/issues"), mi.Class("px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg font-medium hover:bg-gray-50 dark:hover:bg-gray-700"),
							t("action.cancel"),
						),
						b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
							t("issues.action.create"),
						),
					),
				)
			})(b),
		)
	}
}

// issueSLAEstimate renders the single resolution-time card at the bottom of
// the create form. The HTMX attributes let the server recompute the exact
// time when the priority or asset changes; the initial render already carries
// the same markup so the card is useful before any request.
func issueSLAEstimate(minutes int64, priority string, t func(key string, args ...any) string) mi.H {
	switch priority {
	case "critical", "high", "medium", "low":
	default:
		priority = "medium"
	}
	return func(b *mi.Builder) mi.Node {
		return b.Div(
			mi.ID("issue-sla-estimate"),
			mi.Attr("data-issue-sla", "1"),
			mi.Attr("data-priority", priority),
			mi.Attr("data-sla-minutes", strconv.FormatInt(minutes, 10)),
			mi.Attr("hx-get", "/issues/sla-estimate"),
			mi.Attr("hx-trigger", "change from:#field-priority, change from:#field-asset_id"),
			mi.Attr("hx-target", "this"),
			mi.Attr("hx-swap", "outerHTML"),
			mi.Attr("hx-include", "#field-priority, #field-asset_id"),
			mi.Attr("role", "status"),
			mi.Attr("aria-live", "polite"),
			mi.Class("rounded-lg border border-indigo-100 bg-indigo-50/60 px-3 py-2.5 dark:border-indigo-900/40 dark:bg-indigo-950/20"),
			b.Div(mi.Class("flex items-center gap-3"),
				b.Div(mi.Class("flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-indigo-600 text-white"),
					b.Span(mi.Class("material-icons text-base"), "schedule"),
				),
				b.Div(mi.Class("min-w-0 flex-1"),
					b.Div(mi.Class("flex flex-wrap items-center gap-x-2"),
						b.Span(mi.Class("text-xs font-semibold text-gray-800 dark:text-gray-100"), t("issues.sla.estimate.title")),
						b.Span(mi.Class("rounded-full bg-white/80 px-1.5 py-0.5 text-xs font-medium text-indigo-700 ring-1 ring-indigo-200 dark:bg-gray-900/50 dark:text-indigo-300 dark:ring-indigo-800"),
							t("issue.priority."+priority),
						),
					),
					b.P(mi.Attr("title", t("issues.sla.estimate.hint")), mi.Class("mt-0.5 truncate text-xs text-gray-500 dark:text-gray-400"), t("issues.sla.estimate.subtitle")),
				),
				b.Div(mi.Attr("data-sla-value", "1"), mi.Class("shrink-0 text-right text-lg font-semibold leading-tight text-gray-900 dark:text-white"), formatSLAEstimateDuration(t, minutes)),
			),
		)
	}
}

// IssueRejectData holds data for the reject-issue form page.
type IssueRejectData struct {
	Issue     Issue
	CSRFToken string
	Error     string
}

// IssueRejectPage renders the reject-issue form -- a reason is mandatory.
func IssueRejectPage(data IssueRejectData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	iss := data.Issue
	return func(b *mi.Builder) mi.Node {
		var children []interface{}
		if data.Error != "" {
			children = append(children, b.Div(mi.Class("text-sm text-red-600"), data.Error))
		}
		children = append(children,
			b.Div(mi.Class("text-sm text-gray-700 dark:text-gray-300"), iss.Title),
			b.Label(mi.Class("block text-sm font-medium text-gray-700 dark:text-gray-300"), t("issues.reject.motivo")),
			b.Textarea(
				mi.Name("motivo"), mi.Required(),
				mi.Class("mt-1 w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 text-gray-900 dark:text-white text-sm focus:ring-2 focus:ring-indigo-500"),
				mi.Attr("rows", "3"),
			),
		)
		return b.Div(mi.Class("space-y-6 max-w-2xl"),
			b.Div(mi.Class("flex items-center gap-4"),
				b.A(mi.Href(fmt.Sprintf("/issues/%d", iss.ID)), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.reject.title")),
			),
			Card("", func(b *mi.Builder) mi.Node {
				return b.Form(mi.Class("space-y-4"), mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/reject", iss.ID)),
					b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
					b.Div(children...),
					b.Div(mi.Class("flex justify-end gap-3 pt-4"),
						b.A(mi.Href(fmt.Sprintf("/issues/%d", iss.ID)), mi.Class("px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg font-medium hover:bg-gray-50 dark:hover:bg-gray-700"),
							t("action.cancel"),
						),
						b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-red-600 text-white rounded-lg hover:bg-red-700 font-medium"),
							t("issues.action.reject"),
						),
					),
				)
			})(b),
		)
	}
}

// IssueFormField is one resolved custom-template field for display: label
// shown, key used, and the value captured at creation.
type IssueFormField struct {
	Key   string
	Label string
	Value string
}

// IssueHistoryEntry is one step of an issue's lifecycle as shown in the
// detail page's History section. Action is the FSM input that drove the
// step ("created" for the machine's initial entry).
type IssueHistoryEntry struct {
	At        string
	Action    string
	ActorID   int64
	ActorName string
	From      string
	To        string
	Reason    string
	Canonical int64
}

type IssueDetailData struct {
	Issue        Issue
	CSRFToken    string
	WalkError    string
	TemplateName string
	FormFields   []IssueFormField

	// CanAssign tells the page whether the current user may (re)assign the
	// responsible of this issue: only admins, and only while the issue is
	// still in a pre-start state (see canAssignIssues).
	CanAssign bool

	// CanAct tells the page whether the current user may run the lifecycle
	// actions (start, resolve, close, reject) on this issue: the
	// responsible, or an admin. An issue without a responsible can be acted
	// on by anyone, and a child issue never (see canActOnIssue).
	CanAct bool

	// CanUngroup tells the page whether to offer detaching this child from
	// its case: admins only, and only while it is actually a child.
	CanUngroup bool

	// Grouping, resolved on read (display only): the case this issue
	// belongs to, or the issues this case groups.
	MasterTitle string
	Children    []Issue

	// Resolved from the linked asset on read (display only).
	AssetCategory     string // "critico" | "normal"
	AssetTypeName     string
	AssetLocationName string
	AssetMissing      bool // asset_id set but the asset could not be read
	History           []IssueHistoryEntry
	// TrackingURL is the public link of a QR report, resolved on read. Empty
	// when the issue has no tracking token.
	TrackingURL string
}

type issueActionVariant string

const (
	issueActionPrimary   issueActionVariant = "primary"
	issueActionSecondary issueActionVariant = "secondary"
	issueActionDanger    issueActionVariant = "danger"
	issueActionNeutral   issueActionVariant = "neutral"
)

func issueActionClass(variant issueActionVariant) string {
	base := "inline-flex h-9 items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3.5 text-sm font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-offset-2 dark:focus:ring-offset-gray-800"
	switch variant {
	case issueActionPrimary:
		return base + " bg-indigo-600 text-white hover:bg-indigo-700 focus:ring-indigo-500"
	case issueActionSecondary:
		return base + " border border-indigo-300 text-indigo-600 hover:bg-indigo-50 focus:ring-indigo-500 dark:border-indigo-700 dark:text-indigo-400 dark:hover:bg-indigo-900/20"
	case issueActionDanger:
		return base + " border border-red-300 text-red-600 hover:bg-red-50 focus:ring-red-500 dark:border-red-700 dark:text-red-400 dark:hover:bg-red-900/20"
	default:
		return base + " border border-gray-300 text-gray-700 hover:bg-gray-50 focus:ring-gray-500 dark:border-gray-600 dark:text-gray-300 dark:hover:bg-gray-700"
	}
}

func IssueDetailPage(data IssueDetailData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	iss := data.Issue
	return func(b *mi.Builder) mi.Node {
		now := time.Now()

		mainCard := Card("", func(b *mi.Builder) mi.Node {
			var children []interface{}
			children = append(children,
				b.Div(mi.Class("text-sm text-gray-700 dark:text-gray-300"), iss.Description),
			)
			if iss.AssetID > 0 {
				if data.AssetMissing {
					children = append(children, b.Div(mi.Class("mt-4 text-sm text-amber-600 dark:text-amber-400"),
						t("issues.asset.unavailable")))
				} else {
					children = append(children, b.Div(mi.Class("mt-4 text-sm"),
						b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.linked_to")+": "),
						b.A(mi.Href(fmt.Sprintf("/assets/%d", iss.AssetID)), mi.Class("text-indigo-600 dark:text-indigo-400 hover:underline"), iss.AssetName),
					))
					children = append(children,
						b.Div(mi.Class("mt-1 text-sm"),
							b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.asset.category")+": "),
							b.Span(mi.Class("text-gray-700 dark:text-gray-300"), t("issues.asset.category."+data.AssetCategory)),
						),
						b.Div(mi.Class("mt-1 text-sm"),
							b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.asset.type")+": "),
							b.Span(mi.Class("text-gray-700 dark:text-gray-300"), data.AssetTypeName),
						),
						b.Div(mi.Class("mt-1 text-sm"),
							b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.asset.location")+": "),
							b.Span(mi.Class("text-gray-700 dark:text-gray-300"), data.AssetLocationName),
						),
					)
				}
			}
			if iss.ReportedByName != "" {
				children = append(children, b.Div(mi.Class("mt-4 text-sm"),
					b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.reported_by")+": "),
					b.Span(mi.Class("text-gray-700 dark:text-gray-300"), iss.ReportedByName),
				))
			}
			assigneeLabel := iss.AssigneeName
			if assigneeLabel == "" {
				assigneeLabel = t("issues.unassigned")
			}
			children = append(children, b.Div(mi.Class("mt-4 text-sm"),
				b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.assignee")+": "),
				b.Span(mi.Class("text-gray-700 dark:text-gray-300"), assigneeLabel),
			))
			if iss.MasterID > 0 {
				caseLabel := "#" + strconv.FormatInt(iss.MasterID, 10)
				if data.MasterTitle != "" {
					caseLabel = data.MasterTitle
				}
				children = append(children, b.Div(
					mi.Class("mt-4 px-3 py-2 rounded-lg bg-blue-50 dark:bg-blue-900/20 border border-blue-200 dark:border-blue-800 text-sm text-blue-700 dark:text-blue-300 space-y-1"),
					b.Div(t("issues.group.manage_here")),
					b.A(mi.Href(fmt.Sprintf("/issues/%d", iss.MasterID)), mi.Class("font-medium hover:underline"),
						t("issues.group.child_of")+": "+caseLabel),
				))
			}
			if data.TemplateName != "" {
				children = append(children, b.Div(mi.Class("mt-4 text-sm"),
					b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.template_label")+": "),
					b.Span(mi.Class("text-gray-700 dark:text-gray-300"), data.TemplateName),
				))
			}
			if len(data.FormFields) > 0 {
				var fieldRows []interface{}
				fieldRows = append(fieldRows, b.H3(mi.Class("text-sm font-semibold text-gray-700 dark:text-gray-300 mb-2"), t("issues.form_data")))
				for _, f := range data.FormFields {
					label := f.Label
					if label == "" {
						label = f.Key
					}
					fieldRows = append(fieldRows, b.Div(mi.Class("flex items-center justify-between px-3 py-2 border-b border-gray-100 dark:border-gray-700 last:border-b-0"),
						b.Span(mi.Class("text-sm text-gray-500 dark:text-gray-400"), label),
						b.Span(mi.Class("text-sm text-gray-700 dark:text-gray-300"), f.Value),
					))
				}
				children = append(children, b.Div(append([]interface{}{mi.Class("mt-4 rounded-lg border border-gray-200 dark:border-gray-700")}, fieldRows...)...))
			}
			if data.WalkError != "" {
				children = append(children, b.Div(mi.Class("mt-4 px-3 py-2 rounded-lg bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-sm text-red-700 dark:text-red-400"),
					data.WalkError))
			}
			if iss.RejectReason != "" {
				children = append(children, b.Div(mi.Class("mt-4 px-3 py-2 rounded-lg bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 text-sm text-amber-700 dark:text-amber-400"),
					t("issues.reject.motivo")+": "+iss.RejectReason))
			}
			actionBar := []interface{}{
				mi.Class("mt-6 flex flex-wrap items-center gap-2 border-t border-gray-200 pt-4 dark:border-gray-700"),
			}
			if data.CanAct {
				switch iss.State {
				case "reported", "triaged":
					actionBar = append(actionBar, b.Form(mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/start", iss.ID)),
						b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
						b.Button(mi.Type("submit"), mi.Class(issueActionClass(issueActionPrimary)),
							b.Span(mi.Class("material-icons text-base"), "play_arrow"),
							b.Span(t("issues.action.start")),
						),
					))
				case "in_progress":
					actionBar = append(actionBar, b.Form(mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/resolve", iss.ID)),
						b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
						b.Button(mi.Type("submit"), mi.Class(issueActionClass(issueActionPrimary)),
							b.Span(mi.Class("material-icons text-base"), "check_circle"),
							b.Span(t("issues.action.resolve")),
						),
					))
				case "resolved":
					actionBar = append(actionBar, b.Form(mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/close", iss.ID)),
						b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
						b.Button(mi.Type("submit"), mi.Class(issueActionClass(issueActionPrimary)),
							b.Span(mi.Class("material-icons text-base"), "done_all"),
							b.Span(t("issues.action.close")),
						),
					))
				}
			}
			if data.CanAssign {
				actionBar = append(actionBar, b.A(mi.Href(fmt.Sprintf("/issues/%d/assign", iss.ID)),
					mi.Class(issueActionClass(issueActionSecondary)),
					b.Span(mi.Class("material-icons text-base"), "person_add"),
					b.Span(t("issues.action.assign")),
				))
			}
			dangerBar := []interface{}{
				mi.Class("flex flex-wrap items-center gap-2 sm:ml-auto"),
			}
			if data.CanUngroup {
				dangerBar = append(dangerBar, b.Form(mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/ungroup", iss.ID)),
					b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
					b.Button(mi.Type("submit"), mi.Class(issueActionClass(issueActionNeutral)),
						b.Span(mi.Class("material-icons text-base"), "link_off"),
						b.Span(t("issues.group.ungroup")),
					),
				))
			}
			if data.CanAct && iss.State != "closed" && iss.State != "rejected" && iss.State != "duplicate" {
				dangerBar = append(dangerBar, b.A(mi.Href(fmt.Sprintf("/issues/%d/reject", iss.ID)),
					mi.Class(issueActionClass(issueActionDanger)),
					b.Span(mi.Class("material-icons text-base"), "block"),
					b.Span(t("issues.action.reject")),
				))
			}
			if len(dangerBar) > 1 {
				actionBar = append(actionBar, b.Div(dangerBar...))
			}
			if len(actionBar) > 1 {
				children = append(children, b.Div(actionBar...))
			}
			return b.Div(children...)
		})(b)

		historyCard := Card(t("issues.history.title"), func(b *mi.Builder) mi.Node {
			if len(data.History) == 0 {
				return b.P(mi.Class("text-sm text-gray-400 italic"), t("issues.history.empty"))
			}
			var rows []interface{}
			for _, e := range data.History {
				label := t("issues.history.action." + e.Action)
				if e.Action == "created" {
					label = t("issues.history.created")
				}
				meta := e.At
				if e.ActorName != "" {
					meta = e.ActorName + " · " + e.At
				} else if e.ActorID > 0 {
					meta = "#" + strconv.FormatInt(e.ActorID, 10) + " · " + e.At
				}

				parts := []interface{}{
					b.Div(mi.Class("flex items-center justify-between gap-3"),
						b.Span(mi.Class("text-sm font-medium text-gray-900 dark:text-white"), label),
						b.Span(mi.Class("text-xs text-gray-500 dark:text-gray-400 whitespace-nowrap"), meta),
					),
				}
				if e.From != "" && e.To != "" {
					parts = append(parts, b.Div(mi.Class("text-xs text-gray-500 dark:text-gray-400"),
						t("issues.state."+e.From)+" → "+t("issues.state."+e.To)))
				}
				if e.Reason != "" {
					parts = append(parts, b.Div(mi.Class("mt-1 text-xs text-amber-700 dark:text-amber-400"),
						t("issues.history.reason")+": "+e.Reason))
				}
				if e.Canonical > 0 {
					parts = append(parts, b.Div(mi.Class("mt-1 text-xs"),
						b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.history.duplicate_of")+": "),
						b.A(mi.Href(fmt.Sprintf("/issues/%d", e.Canonical)),
							mi.Class("text-indigo-600 dark:text-indigo-400 hover:underline"),
							"#"+strconv.FormatInt(e.Canonical, 10)),
					))
				}
				rows = append(rows, b.Div(append([]interface{}{
					mi.Class("px-4 py-3 border-b border-gray-100 dark:border-gray-700 last:border-b-0 space-y-1"),
				}, parts...)...))
			}
			return b.Div(rows...)
		})(b)

		var slaCard mi.Node
		isTerminal := iss.State == "closed" || iss.State == "rejected" || iss.State == "duplicate"
		showFirst := iss.FirstResponseAt == "" && iss.SLAFirstResponseDeadline != ""
		showRes := !showFirst && iss.FirstResponseAt != "" && iss.SLAResolutionDeadline != "" && !isTerminal
		if showFirst || showRes {
			var title string
			var deadline string
			if showFirst {
				title = t("issues.sla.first_response")
				deadline = iss.SLAFirstResponseDeadline
			} else {
				title = t("issues.sla.resolution")
				deadline = iss.SLAResolutionDeadline
			}
			if dl, err := time.Parse(time.RFC3339, deadline); err == nil {
				var created time.Time
				if c, err2 := time.Parse(time.RFC3339, iss.CreatedAt); err2 == nil {
					created = c
				}
				total := dl.Sub(created)
				if total < 0 {
					total = 0
				}
				totalMin := int64(total / time.Minute)
				var elapsed time.Duration
				if now.Before(created) {
					elapsed = 0
				} else {
					elapsed = now.Sub(created)
				}
				var pct int
				if total > 0 {
					pct = int((elapsed * 100) / total)
				}
				if pct < 0 {
					pct = 0
				}
				if pct > 100 {
					pct = 100
				}
				barColor := "bg-indigo-500"
				if pct >= 90 {
					barColor = "bg-red-500"
				} else if pct >= 70 {
					barColor = "bg-amber-500"
				}
				if now.After(dl) && !isTerminal {
					barColor = "bg-red-600"
					pct = 100
				}
				remaining := dl.Sub(now)
				remTxt := t("issues.sla.remaining") + ": " + formatDurationMinutes(int64(remaining/time.Minute))
				if remaining < 0 && !isTerminal {
					remTxt = t("issues.sla.overdue_by") + ": " + formatDurationMinutes(int64(-remaining/time.Minute))
				}
				prioLabel := iss.Priority
				if p := t("issues.priority." + iss.Priority); p != "" && !strings.Contains(p, ".") {
					prioLabel = p
				}
				maxTxt := formatDurationMinutes(totalMin)
				slaCard = Card(title, func(b *mi.Builder) mi.Node {
					return b.Div(mi.Class("space-y-3"),
						b.Div(mi.Class("text-center text-sm font-medium text-gray-900 dark:text-white"), remTxt),
						b.Div(mi.Class("w-full bg-gray-200 dark:bg-gray-700 rounded-full h-2"),
							b.Div(mi.Class(barColor+" h-2 rounded-full"), mi.Style("width: "+strconv.Itoa(pct)+"%")),
						),
						b.Div(mi.Class("flex items-center justify-between text-xs text-gray-500 dark:text-gray-400"),
							b.Span(t("issues.sla.starts_at")+": "+formatTimeShort(iss.CreatedAt)),
							b.Span(t("issues.sla.due_at")+": "+formatTimeShort(deadline)),
						),
						b.Div(mi.Class("border-t border-dashed border-gray-200 dark:border-gray-700 pt-2 text-xs text-gray-600 dark:text-gray-300 space-y-1"),
							b.Div(t("issues.sla.priority")+": "+prioLabel),
							b.Div(t("issues.sla.max_time")+": "+maxTxt),
						),
					)
				})(b)
			}
		}

		// The public link is only present for QR reports. It stays visible
		// after expiry, but the copy action disappears: the operator needs to
		// know why the reporter says the page does not open.
		var trackingCard mi.Node
		if data.TrackingURL != "" {
			expired := issueTrackExpired(iss.TrackExpiresAt, now)
			trackingCard = Card(t("issues.tracking.title"), func(b *mi.Builder) mi.Node {
				children := []interface{}{
					b.A(
						mi.Href(data.TrackingURL),
						mi.Target("_blank"),
						mi.Class("block truncate font-mono text-sm text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"),
						data.TrackingURL,
					),
					b.P(mi.Class("mt-2 text-xs text-gray-500 dark:text-gray-400"),
						t("issues.tracking.hint")),
				}
				if expired {
					children = append(children, b.P(
						mi.Class("mt-3 text-xs text-red-600 dark:text-red-400"),
						t("issues.tracking.expired"),
					))
				} else {
					children = append(children, b.Div(
						mi.Class("mt-3 flex flex-wrap items-center gap-2"),
						Button(t("issue_qr.copy"), "secondary",
							mi.Type("button"),
							mi.Attr("onclick", issueQRCodeCopyScript(data.TrackingURL)),
						)(b),
					))
					if at := formatIssueTrackTime(iss.TrackExpiresAt); at != "" {
						children = append(children, b.P(
							mi.Class("mt-2 text-xs text-gray-500 dark:text-gray-400"),
							t("issues.tracking.expires_at")+": "+at,
						))
					}
				}
				return b.Div(children...)
			})(b)
		}

		// A case lists its children, each linking back to its own detail;
		// the child's own page points here instead (no actions there).
		var childrenCard mi.Node
		if iss.MasterID == 0 && len(data.Children) > 0 {
			childrenCard = Card(t("issues.group.children"), func(b *mi.Builder) mi.Node {
				var rows []interface{}
				for _, c := range data.Children {
					rows = append(rows, b.A(mi.Href(fmt.Sprintf("/issues/%d", c.ID)),
						mi.Class("flex items-center justify-between gap-3 px-4 py-3 border-b border-gray-100 dark:border-gray-700 last:border-b-0 hover:bg-gray-50 dark:hover:bg-gray-700/50"),
						b.Span(mi.Class("text-sm text-gray-900 dark:text-white truncate"), c.Title),
						issueStatusBadge(c.State, t)(b),
					))
				}
				return b.Div(rows...)
			})(b)
		}

		rightCol := []interface{}{childrenCard, slaCard, trackingCard, historyCard}
		rightDivArgs := []interface{}{mi.Class("space-y-6")}
		for _, n := range rightCol {
			if n != nil {
				rightDivArgs = append(rightDivArgs, n)
			}
		}

		return b.Div(mi.Class("space-y-6"),
			b.Div(mi.Class("flex items-center gap-4"),
				b.A(mi.Href("/issues"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), iss.Title),
				issueStatusBadge(iss.State, t)(b),
			),
			b.Div(mi.Class("grid grid-cols-1 lg:grid-cols-3 gap-6"),
				b.Div(mi.Class("lg:col-span-2 space-y-6"), mainCard),
				b.Div(rightDivArgs...),
			),
		)
	}
}

// ensureIssueFSMDefinition idempotently ensures the FSM definition exists,
// creating it on first use. Called lazily from IssueCreate, not at startup.
func (h *UIHandler) ensureIssueFSMDefinition(ctx context.Context) (int64, error) {
	defs, err := h.xoluClient.ListMachineDefs(ctx)
	if err != nil {
		return 0, err
	}
	for _, d := range defs {
		if d.Name == issueDefName {
			return d.ID, nil
		}
	}

	result, err := h.xoluClient.CreateMachineDef(ctx, xolu.MachineSpec{
		Name:        issueDefName,
		Description: "Seam AMS Issues lifecycle (8 states, iteration 1)",
		Initial:     "reported",
		Determinism: "firstmatch",
		States: map[string]xolu.StateDef{
			"reported":    {Terminal: false},
			"triaged":     {Terminal: false},
			"in_progress": {Terminal: false},
			"on_hold":     {Terminal: false},
			"resolved":    {Terminal: false},
			"closed":      {Terminal: true},
			"rejected":    {Terminal: true},
			"duplicate":   {Terminal: true},
		},
		Transitions: []xolu.TransitionDef{
			{From: json.RawMessage(`"reported"`), Input: "triage", To: "triaged"},
			{From: json.RawMessage(`["reported","triaged"]`), Input: "start", To: "in_progress"},
			{From: json.RawMessage(`["triaged","in_progress"]`), Input: "hold", To: "on_hold"},
			{From: json.RawMessage(`"on_hold"`), Input: "resume", To: "in_progress"},
			{From: json.RawMessage(`"in_progress"`), Input: "resolve", To: "resolved"},
			{From: json.RawMessage(`"resolved"`), Input: "close", To: "closed"},
			{From: json.RawMessage(`["reported","triaged","in_progress","on_hold","resolved"]`), Input: "reject", To: "rejected"},
			{From: json.RawMessage(`["reported","triaged","in_progress"]`), Input: "merge", To: "duplicate"},
		},
	})
	if err != nil {
		return 0, err
	}
	return result.ID, nil
}
