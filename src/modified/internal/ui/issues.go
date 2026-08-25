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
	"fmt"
	"net/http"
	"strconv"
	"time"

	mi "github.com/ha1tch/minty"
	fe "github.com/ha1tch/seam-ui/internal/formengine"
	issueschemas "github.com/ha1tch/seam-ui/internal/formengine/schemas/issues"
)

// Issue mirrors what's stored in the "issues" xolu entity.
type Issue struct {
	ID          int64
	Title       string
	Description string
	Status      string // "open" | "closed"
	AssetID     int64
	AssetName   string // resolved, not stored -- populated on read for display
	CreatedAt   string
}

// IssuesList handles GET /issues.
func (h *UIHandler) IssuesList(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()

	result, err := h.xoluClient.OQL(ctx, "SELECT TOP 100 * FROM issues ORDER BY id DESC")
	var issues []Issue
	if err == nil {
		for _, row := range result.Data {
			issues = append(issues, Issue{
				ID:      toInt64(row["id"]),
				Title:   toString(row["title"]),
				Status:  toString(row["status"]),
				AssetID: toInt64(row["asset_id"]),
			})
		}
	} else {
		h.logger.Error("failed to list issues", "error", err)
	}

	// Resolve linked asset names for display -- a small number of
	// issues per page (TOP 100), one Get per linked asset is
	// acceptable here; not worth a join/batch fetch for a "simple"
	// module's list page. asset_id itself already came from the
	// SELECT * above, no need to re-query for it per row.
	for i := range issues {
		if issues[i].AssetID > 0 {
			if asset, err := h.xoluClient.Get(ctx, "assets", issues[i].AssetID); err == nil {
				issues[i].AssetName = toString(asset.Data["name"])
			}
		}
	}

	data := IssuesListData{Issues: issues}
	h.page.Render(w, r, t("issues.list_title"), "/issues", IssuesListPage(data, t))
}

// IssueNew handles GET /issues/new.
func (h *UIHandler) IssueNew(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()
	csrfToken := h.GetCSRFToken(w, r)

	formEng := &fe.FormEngine{Translator: h.formEngineTranslator()}
	rc := fe.RenderContext{Locale: h.currentLocale(), CSRFToken: csrfToken, Module: "issues", Form: "issue"}
	formContent := formEng.Render(ctx, issueschemas.Schema("create"), nil, nil, rc)

	data := IssueFormData{CSRFToken: csrfToken, FormContent: formContent}
	h.page.Render(w, r, t("issues.new"), "/issues", IssueFormPage(data, t))
}

// IssueCreate handles POST /issues.
func (h *UIHandler) IssueCreate(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	title := r.FormValue("title")
	description := r.FormValue("description")
	assetIDStr := r.FormValue("asset_id")
	csrfToken := h.GetCSRFToken(w, r)

	errors := map[string]string{}
	if title == "" {
		errors["title"] = t("issues.error.title_required")
	}
	if len(errors) > 0 {
		formEng := &fe.FormEngine{Translator: h.formEngineTranslator()}
		rc := fe.RenderContext{Locale: h.currentLocale(), CSRFToken: csrfToken, Module: "issues", Form: "issue"}
		formContent := formEng.Render(ctx, issueschemas.Schema("create"),
			map[string]interface{}{"title": title, "description": description, "asset_id": assetIDStr}, errors, rc)
		data := IssueFormData{CSRFToken: csrfToken, FormContent: formContent, Errors: errors}
		h.page.Render(w, r, t("issues.new"), "/issues", IssueFormPage(data, t))
		return
	}

	issueData := map[string]any{
		"title":       title,
		"description": description,
		"status":      "open",
		"created_at":  time.Now().UTC().Format(time.RFC3339),
	}
	// asset_id is optional -- the picker's own hidden field submits an
	// empty string when nothing was selected, not "0"; only set it
	// when a real selection was made.
	if assetID, err := strconv.ParseInt(assetIDStr, 10, 64); err == nil && assetID > 0 {
		issueData["asset_id"] = assetID
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
	t := h.translateFunc()
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

	issue := Issue{
		ID:          entity.ID,
		Title:       toString(entity.Data["title"]),
		Description: toString(entity.Data["description"]),
		Status:      toString(entity.Data["status"]),
		AssetID:     toInt64(entity.Data["asset_id"]),
		CreatedAt:   toString(entity.Data["created_at"]),
	}
	if issue.AssetID > 0 {
		if asset, err := h.xoluClient.Get(ctx, "assets", issue.AssetID); err == nil {
			issue.AssetName = toString(asset.Data["name"])
		}
	}

	data := IssueDetailData{Issue: issue, CSRFToken: h.GetCSRFToken(w, r)}
	h.page.Render(w, r, t("issues.detail_title"), "/issues", IssueDetailPage(data, t))
}

// IssueToggleStatus handles POST /issues/{id}/toggle-status -- flips
// open<->closed. Deliberately not an FSM machine walk: a two-state
// status field for a "simple" module doesn't need xolu's own FSM
// primitives, the same reasoning documented at the top of this file.
func (h *UIHandler) IssueToggleStatus(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
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
	newStatus := "closed"
	if toString(entity.Data["status"]) == "closed" {
		newStatus = "open"
	}
	if _, err := h.xoluClient.Patch(ctx, "issues", id, map[string]any{"status": newStatus}); err != nil {
		h.logger.Error("failed to toggle issue status", "id", id, "error", err)
	}

	http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ─── Pages ──────────────────────────────────────────────────────────────

// IssuesListData holds data for the issues list page.
type IssuesListData struct {
	Issues []Issue
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
			Badge:    issueStatusBadge(iss.Status, t),
		}
	}
	return func(b *mi.Builder) mi.Node {
		return b.Div(mi.Class("space-y-6"),
			b.Div(mi.Class("flex items-center justify-between"),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.list_title")),
				b.A(mi.Href("/issues/new"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
					t("issues.new"),
				),
			),
			SimpleListCard(items, t("issues.empty"))(b),
		)
	}
}

// issueStatusBadge maps this module's own status vocabulary to the
// shared Badge(text, variant) primitive (internal/ui/components.go) --
// "warning" (amber) for open, Badge's own default (gray) for closed,
// since "closed" doesn't match any of Badge's known variant keywords.
// Was a hand-rolled duplicate of Badge's own pill markup until
// 2026-08-06; now a thin mapping, no markup of its own.
func issueStatusBadge(status string, t func(key string, args ...any) string) mi.H {
	if status == "open" {
		return Badge(t("issues.status.open"), "warning")
	}
	return Badge(t("issues.status.closed"), "closed")
}

// IssueFormData holds data for the create-issue form.
type IssueFormData struct {
	CSRFToken   string
	FormContent mi.H
	Errors      map[string]string
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
				return b.Form(mi.Class("space-y-4"), mi.Method("post"), mi.Action("/issues"),
					data.FormContent(b),
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

// IssueDetailData holds data for the issue detail page.
type IssueDetailData struct {
	Issue     Issue
	CSRFToken string
}

// IssueDetailPage renders the issue detail page.
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
				issueStatusBadge(iss.Status, t)(b),
			),
			Card("", func(b *mi.Builder) mi.Node {
				var children []interface{}
				children = append(children,
					b.Div(mi.Class("text-sm text-gray-700 dark:text-gray-300"), iss.Description),
				)
				if iss.AssetName != "" {
					children = append(children, b.Div(mi.Class("mt-4 text-sm"),
						b.Span(mi.Class("text-gray-500 dark:text-gray-400"), t("issues.linked_to")+": "),
						b.A(mi.Href(fmt.Sprintf("/assets/%d", iss.AssetID)), mi.Class("text-indigo-600 dark:text-indigo-400 hover:underline"), iss.AssetName),
					))
				}
				children = append(children, b.Form(mi.Class("mt-4"), mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/toggle-status", iss.ID)),
					b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
					b.Button(mi.Type("submit"), mi.Class("px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg font-medium hover:bg-gray-50 dark:hover:bg-gray-700"),
						toggleLabel(iss.Status, t),
					),
				))
				return b.Div(children...)
			})(b),
		)
	}
}

func toggleLabel(status string, t func(key string, args ...any) string) string {
	if status == "closed" {
		return t("issues.action.reopen")
	}
	return t("issues.action.close")
}
