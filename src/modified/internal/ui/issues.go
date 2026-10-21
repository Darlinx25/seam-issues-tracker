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
	"net/url"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
	fe "github.com/ha1tch/seam-ui/internal/formengine"
	issueschemas "github.com/ha1tch/seam-ui/internal/formengine/schemas/issues"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// Issue mirrors what's stored in the "issues" xolu entity.
type Issue struct {
	ID             int64
	Title          string
	Description    string
	Priority       string // "critical" | "high" | "medium" | "low"
	Severity       string // "critical" | "major" | "minor"
	State          string // "reported" | "in_progress" | "closed"
	RejectReason   string
	MachineID      int64
	AssetID        int64
	AssetName      string // resolved, not stored -- populated on read for display
	ReportedBy     int64  // user id of the reporter, stored on the entity
	ReportedByName string // resolved, not stored -- populated on read for display
	CreatedAt      string

	// TemplateID is the specific template version the issue was created
	// with; FormData holds the custom field values it captured. Both are
	// frozen onto the issue so later template edits don't alter history.
	TemplateID int64
	FormData   map[string]any
}

const issueDefName = "seam_issue_lifecycle"

func issueFromEntity(e *xolu.Entity) Issue {
	return Issue{
		ID:           e.ID,
		Title:        toString(e.Data["title"]),
		Description:  toString(e.Data["description"]),
		Priority:     toString(e.Data["priority"]),
		Severity:     toString(e.Data["severity"]),
		State:        toString(e.Data["state"]),
		RejectReason: toString(e.Data["reject_reason"]),
		MachineID:    toInt64(e.Data["machine_id"]),
		AssetID:      toInt64(e.Data["asset_id"]),
		CreatedAt:    toString(e.Data["created_at"]),
		ReportedBy:   toInt64(e.Data["reported_by"]),
		TemplateID:   toInt64(e.Data["template_id"]),
		FormData:     toMap(e.Data["form_data"]),
	}
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

// IssuesList handles GET /issues.
func (h *UIHandler) IssuesList(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()

	// Allowed states (8-state lifecycle). Whitelist to avoid SQL injection via param.
	allowedStates := map[string]bool{
		"reported":    true,
		"triaged":     true,
		"in_progress": true,
		"on_hold":     true,
		"resolved":    true,
		"closed":      true,
		"rejected":    true,
		"duplicate":   true,
	}

	stateFilter := r.URL.Query().Get("state")
	if !allowedStates[stateFilter] {
		stateFilter = ""
	}

	// Text search (?q=) is applied client-side, over the rows already fetched
	// from xolu (title plus linked asset name). It needs no extra query: it's
	// only read here to prefill the box and keep the state-tab links in sync.
	search := strings.TrimSpace(r.URL.Query().Get("q"))

	// 1) Counts per state (all issues, not paged) to populate tabs
	counts := map[string]int{
		"reported":    0,
		"triaged":     0,
		"in_progress": 0,
		"on_hold":     0,
		"resolved":    0,
		"closed":      0,
		"rejected":    0,
		"duplicate":   0,
	}
	if groupRes, err := h.xoluClient.OQL(ctx, "SELECT state, COUNT(*) AS c FROM issues GROUP BY state"); err == nil {
		for _, row := range groupRes.Data {
			st := toString(row["state"])
			if allowedStates[st] {
				if v, ok := row["c"].(float64); ok {
					counts[st] = int(v)
				} else {
					counts[st] = int(toInt64(row["c"]))
				}
			}
		}
	} else {
		h.logger.Error("failed to count issues by state", "error", err)
	}

	// 2) List issues (filtered by state, as before). Keep TOP 100 and id DESC.
	// The text search deliberately stays out of this query -- the page's own
	// JS filters the rendered rows in real time.
	listQuery := "SELECT TOP 100 * FROM issues"
	if stateFilter != "" {
		// Quote state (validated against whitelist). escapeOQL exists but enum is safe; quote-doubling is harmless.
		listQuery += " WHERE state = '" + escapeOQL(stateFilter) + "'"
	}
	listQuery += " ORDER BY id DESC"

	result, err := h.xoluClient.OQL(ctx, listQuery)
	var issues []Issue
	if err == nil {
		for _, row := range result.Data {
			issues = append(issues, Issue{
				ID:        toInt64(row["id"]),
				Title:     toString(row["title"]),
				Priority:  toString(row["priority"]),
				Severity:  toString(row["severity"]),
				State:     toString(row["state"]),
				MachineID: toInt64(row["machine_id"]),
				AssetID:   toInt64(row["asset_id"]),
			})
		}
	} else {
		h.logger.Error("failed to list issues", "error", err)
	}

	// 3) Resolve linked asset names (unchanged behavior)
	for i := range issues {
		if issues[i].AssetID > 0 {
			if asset, err := h.xoluClient.Get(ctx, "assets", issues[i].AssetID); err == nil {
				issues[i].AssetName = toString(asset.Data["name"])
			}
		}
	}

	data := IssuesListData{Issues: issues, StateFilter: stateFilter, Counts: counts, SearchQuery: search}
	h.page.Render(w, r, t("issues.list_title"), "/issues", IssuesListPage(data, t))
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

	data := IssueFormData{CSRFToken: csrfToken, FormContent: formContent}
	h.page.Render(w, r, t("issues.new"), "/issues", IssueFormPage(data, t))
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
		data := IssueFormData{CSRFToken: csrfToken, FormContent: formContent, Errors: errors, TemplateFieldsContent: tplContent}
		h.page.Render(w, r, t("issues.new"), "/issues", IssueFormPage(data, t))
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

	issueData := map[string]any{
		"title":       title,
		"description": description,
		"priority":    priority,
		"severity":    severity,
		"state":       machine.State,
		"machine_id":  machine.ID,
		"created_at":  time.Now().UTC().Format(time.RFC3339),
	}
	if assetID > 0 {
		issueData["asset_id"] = assetID
	}
	issueData["asset_category"] = h.resolveAssetCategory(ctx, assetID)
	if tpl != nil {
		issueData["template_id"] = tpl.ID
		issueData["form_data"] = formData
	}
	if currentUser != nil && currentUser.ID > 0 {
		issueData["reported_by"] = currentUser.ID
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

	data := assetDetail
	data.Issue = issue
	data.CSRFToken = h.GetCSRFToken(w, r)
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

	result, err := h.xoluClient.WalkMachine(ctx, issue.MachineID, xolu.WalkRequest{Input: input})
	if err != nil {
		h.logger.Info("issue walk rejected", "id", id, "input", input, "error", err)
		csrfToken := h.GetCSRFToken(w, r)
		data := IssueDetailData{Issue: issue, CSRFToken: csrfToken, WalkError: err.Error()}
		h.page.Render(w, r, t("issues.detail_title"), "/issues", IssueDetailPage(data, t))
		return
	}

	patchData := map[string]any{
		"state":      result.Current,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := h.xoluClient.Patch(ctx, "issues", id, patchData); err != nil {
		h.logger.Error("issue walked but state patch failed", "id", id, "error", err)
	}

	http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// IssueRejectFormPage handles GET /issues/{id}/reject -- renders the
// reject form. A reason is mandatory; enforced server-side on submit.
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
	data := IssueRejectData{Issue: issueFromEntity(entity), CSRFToken: h.GetCSRFToken(w, r)}
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

	if motivo == "" {
		h.logger.Info("issue reject refused: motivo required", "id", id)
		data := IssueRejectData{Issue: issue, CSRFToken: h.GetCSRFToken(w, r), Error: t("issues.reject.error.motivo_required")}
		h.page.Render(w, r, t("issues.reject.title"), "/issues", IssueRejectPage(data, t))
		return
	}

	result, err := h.xoluClient.WalkMachine(ctx, issue.MachineID, xolu.WalkRequest{
		Input:   "reject",
		Payload: map[string]interface{}{"motivo": motivo},
	})
	if err != nil {
		h.logger.Info("issue reject walk failed", "id", id, "error", err)
		data := IssueDetailData{Issue: issue, CSRFToken: h.GetCSRFToken(w, r), WalkError: err.Error()}
		h.page.Render(w, r, t("issues.detail_title"), "/issues", IssueDetailPage(data, t))
		return
	}

	patchData := map[string]any{
		"state":         result.Current,
		"reject_reason": motivo,
		"updated_at":    time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := h.xoluClient.Patch(ctx, "issues", id, patchData); err != nil {
		h.logger.Error("issue rejected but patch failed", "id", id, "error", err)
	}

	http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// IssuesListData holds data for the issues list page.
type IssuesListData struct {
	Issues      []Issue
	StateFilter string
	Counts      map[string]int
	SearchQuery string
}

// issueTabHref builds a state-tab link that keeps the current title search
// (?q=) in place, so switching tabs doesn't silently drop the search. With no
// search active it returns the plain URL the tabs have always used.
func issueTabHref(state, search string) string {
	if search == "" {
		if state == "" {
			return "/issues"
		}
		return "/issues?state=" + state
	}
	v := url.Values{}
	if state != "" {
		v.Set("state", state)
	}
	v.Set("q", search)
	return "/issues?" + v.Encode()
}

// IssuesListPage renders the issues list page.
func IssuesListPage(data IssuesListData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	items := make([]SimpleListItem, len(data.Issues))
	for i, iss := range data.Issues {
		assetLine := t("issues.no_asset")
		if iss.AssetName != "" {
			assetLine = t("issues.linked_to") + ": " + iss.AssetName
		}
		items[i] = SimpleListItem{
			Href:     fmt.Sprintf("/issues/%d", iss.ID),
			Title:    iss.Title,
			Subtitle: assetLine,
			Badge:    issueStatusBadge(iss.State, t),
			Search:   strings.TrimSpace(iss.Title + " " + iss.AssetName),
		}
	}

	// States order (logical lifecycle)
	statesOrder := []string{
		"reported",
		"triaged",
		"in_progress",
		"on_hold",
		"resolved",
		"closed",
		"rejected",
		"duplicate",
	}

	return func(b *mi.Builder) mi.Node {
		// Build filter tabs
		var tabs []interface{}
		// "All" tab
		allCount := 0
		if data.Counts != nil {
			for _, c := range data.Counts {
				allCount += c
			}
		}
		allActive := data.StateFilter == ""
		allClasses := "px-3 py-1.5 text-sm rounded-lg border transition-colors "
		if allActive {
			allClasses += "bg-indigo-600 text-white border-indigo-600"
		} else {
			allClasses += "bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700"
		}
		tabs = append(tabs, b.A(mi.Href(issueTabHref("", data.SearchQuery)), mi.Class(allClasses), t("issues.all"), " (", strconv.Itoa(allCount), ")"))

		for _, st := range statesOrder {
			c := 0
			if data.Counts != nil {
				c = data.Counts[st]
			}
			active := data.StateFilter == st
			cls := "px-3 py-1.5 text-sm rounded-lg border transition-colors "
			if active {
				cls += "bg-indigo-600 text-white border-indigo-600"
			} else {
				cls += "bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700"
			}
			label := t("issues.state." + st)
			tabs = append(tabs, b.A(mi.Href(issueTabHref(st, data.SearchQuery)), mi.Class(cls), label, " (", strconv.Itoa(c), ")"))
		}

		// The search box and the state tabs share this bar. Submitting the
		// box keeps the active tab via a hidden input; clicking a tab keeps
		// the search via issueTabHref -- so the two combine in either order.
		// The search box and the state tabs share this bar. Filtering happens
		// entirely in issueSearchScript below, over the rows already rendered
		// from the fetched list, so it's instant and needs no extra query. The
		// input only prefills from ?q= so a shared URL or a tab switch keeps
		// the search.
		return b.Div(mi.Class("space-y-6"),
			b.Div(mi.Class("flex items-center justify-between"),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.list_title")),
				b.A(mi.Href("/issues/new"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
					t("issues.new"),
				),
			),
			// Filter bar
			b.Div(mi.Class("flex flex-wrap items-center gap-4"),
				b.Div(mi.Class("relative flex-1 max-w-md"),
					SearchInput("q", t("issues.search_placeholder"), data.SearchQuery,
						mi.ID("issue-search"),
						mi.Attr("style", "padding-right:2.5rem"),
					)(b),
					b.Button(mi.Type("button"), mi.ID("issue-search-clear"),
						mi.Class("hidden absolute right-4 top-1/2 -translate-y-1/2 material-icons text-lg text-gray-400 hover:text-gray-600 dark:hover:text-gray-200"),
						mi.Attr("aria-label", t("issues.search_clear")),
						"close",
					),
				),
				b.Div(append([]interface{}{mi.ID("issue-tabs"), mi.Class("flex flex-wrap gap-2")}, tabs...)...),
			),
			b.Div(mi.ID("issue-list"),
				SimpleListCard(items, t("issues.empty"))(b),
			),
			b.Div(mi.ID("issue-no-results"),
				mi.Class("hidden bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-8 text-center text-gray-500 dark:text-gray-400"),
				t("issues.search_empty"),
			),
			b.Style(mi.Raw(issueSearchStyle)),
			b.Script(mi.Raw(issueSearchScript)),
		)
	}
}

// issueSearchStyle hides the browser's own clear affordance on the search
// input so it doesn't overlap the explicit clear button next to it.
const issueSearchStyle = `
#issue-search::-webkit-search-cancel-button,
#issue-search::-webkit-search-decoration { -webkit-appearance: none; appearance: none; }
#issue-search::-ms-clear { display: none; }
`

// issueSearchScript filters the already-rendered issue rows by their
// data-search text (title plus linked asset name) as the user types: no
// round trip and no query involved. It also keeps the clear button, the
// state-tab links and the URL's ?q= in sync with the current input.
const issueSearchScript = `
(function () {
  var input = document.getElementById('issue-search');
  var clear = document.getElementById('issue-search-clear');
  var list = document.getElementById('issue-list');
  var noResults = document.getElementById('issue-no-results');
  var tabs = document.getElementById('issue-tabs');
  if (!input || !list) return;
  var rows = Array.prototype.slice.call(list.querySelectorAll('a[data-search]'));
  var links = tabs ? Array.prototype.slice.call(tabs.querySelectorAll('a')) : [];

  function sync() {
    var q = (input.value || '').trim();
    var needle = q.toLowerCase();
    if (clear) clear.classList.toggle('hidden', q === '');

    var shown = 0;
    rows.forEach(function (row) {
      var hay = (row.getAttribute('data-search') || '').toLowerCase();
      var match = needle === '' || hay.indexOf(needle) !== -1;
      row.style.display = match ? '' : 'none';
      if (match) shown++;
    });
    if (noResults) noResults.classList.toggle('hidden', !(needle !== '' && rows.length > 0 && shown === 0));

    links.forEach(function (a) {
      var href = a.getAttribute('href') || '';
      var parts = href.split('?');
      var params = new URLSearchParams(parts.length > 1 ? parts[1] : '');
      if (q !== '') { params.set('q', q); } else { params.delete('q'); }
      var query = params.toString();
      a.setAttribute('href', parts[0] + (query ? '?' + query : ''));
    });

    try {
      var url = new URL(window.location.href);
      if (q !== '') { url.searchParams.set('q', q); } else { url.searchParams.delete('q'); }
      window.history.replaceState(null, '', url.pathname + url.search);
    } catch (e) {}
  }

  input.addEventListener('input', sync);
  input.addEventListener('search', sync);
  if (clear) {
    clear.addEventListener('click', function () {
      input.value = '';
      sync();
      input.focus();
    });
  }
  sync();
})();
`

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

type IssueDetailData struct {
	Issue        Issue
	CSRFToken    string
	WalkError    string
	TemplateName string
	FormFields   []IssueFormField

	// Resolved from the linked asset on read (display only).
	AssetCategory     string // "critico" | "normal"
	AssetTypeName     string
	AssetLocationName string
	AssetMissing      bool // asset_id set but the asset could not be read
}

func IssueDetailPage(data IssueDetailData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	iss := data.Issue
	return func(b *mi.Builder) mi.Node {
		return b.Div(mi.Class("space-y-6 max-w-2xl"),
			b.Div(mi.Class("flex items-center gap-4"),
				b.A(mi.Href("/issues"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), iss.Title),
				issueStatusBadge(iss.State, t)(b),
			),
			Card("", func(b *mi.Builder) mi.Node {
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
				switch iss.State {
				case "reported":
					children = append(children, b.Form(mi.Class("mt-4"), mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/start", iss.ID)),
						b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
						b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
							t("issues.action.start")),
					))
				case "in_progress":
					children = append(children, b.Form(mi.Class("mt-4"), mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/resolve", iss.ID)),
						b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
						b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
							t("issues.action.resolve")),
					))
				case "resolved":
					children = append(children, b.Form(mi.Class("mt-4"), mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/close", iss.ID)),
						b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
						b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
							t("issues.action.close")),
					))
				}
				if iss.State != "closed" && iss.State != "rejected" && iss.State != "duplicate" {
					children = append(children, b.A(mi.Href(fmt.Sprintf("/issues/%d/reject", iss.ID)),
						mi.Class("mt-4 inline-block px-4 py-2 border border-red-300 dark:border-red-700 text-red-600 dark:text-red-400 rounded-lg font-medium hover:bg-red-50 dark:hover:bg-red-900/20"),
						t("issues.action.reject"),
					))
				}
				return b.Div(children...)
			})(b),
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
