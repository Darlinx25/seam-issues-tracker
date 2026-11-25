package ui

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	mi "github.com/ha1tch/minty"
	fe "github.com/ha1tch/seam-ui/internal/formengine"
)

var issueMergeSchema = []byte(`{
  "type": "object",
  "properties": {
    "canonical_id": {
      "type": "integer",
      "x-seam-input": "any-picker",
      "x-seam-source": "entity:issues"
    }
  }
}`)

type IssueMergeData struct {
	Issue       Issue
	CSRFToken   string
	FormContent mi.H
	Error       string
	Locked      bool
}

func issueMergeRefusalKey(issue Issue, hasChildren bool) string {
	if issue.MasterID > 0 {
		return "issues.merge.error.child"
	}
	if hasChildren {
		return "issues.merge.error.has_children"
	}
	switch issue.State {
	case "reported", "triaged", "in_progress":
		return ""
	default:
		return "issues.merge.error.state_invalid"
	}
}

func issueMergeCanonicalRefusalKey(id, canonicalID int64, canonical *Issue, hasChildren bool) string {
	if canonicalID <= 0 {
		return "issues.merge.error.canonical_required"
	}
	if canonicalID == id {
		return "issues.merge.error.self"
	}
	if canonical == nil || canonical.ID <= 0 {
		return "issues.merge.error.not_found"
	}
	if isIssueTerminal(canonical.State) {
		return "issues.merge.error.terminal"
	}
	if canonical.MasterID > 0 || hasChildren {
		return "issues.merge.error.group"
	}
	return ""
}

func issueHasChildren(links []Issue, id int64) bool {
	for _, link := range links {
		if link.MasterID == id {
			return true
		}
	}
	return false
}

func (h *UIHandler) renderMergeForm(ctx context.Context, canonicalID int64, csrfToken, errMsg string) mi.H {
	formEng := &fe.FormEngine{Translator: h.formEngineTranslator()}
	rc := fe.RenderContext{Locale: h.currentLocale(), CSRFToken: csrfToken, Module: "issues", Form: "merge"}
	defaults := map[string]any{}
	if canonicalID > 0 {
		defaults["canonical_id"] = canonicalID
	}
	var errs map[string]string
	if errMsg != "" {
		errs = map[string]string{"canonical_id": errMsg}
	}
	return formEng.Render(ctx, resolveSchema(ctx, "issues", "merge", issueMergeSchema), defaults, errs, rc)
}

func (h *UIHandler) IssueMergeFormPage(w http.ResponseWriter, r *http.Request) {
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
		h.logger.Info("issue merge form refused: not the responsible", "id", id, "assignee", issue.Assignee)
		h.Error403(w, r)
		return
	}
	csrfToken := h.GetCSRFToken(w, r)
	data := IssueMergeData{Issue: issue, CSRFToken: csrfToken}
	links, err := h.listIssueLinks(ctx)
	if err != nil {
		h.logger.Error("failed to load issue links for merge", "id", id, "error", err)
		data.Locked = true
		data.Error = t("issues.merge.error.failed")
	} else if key := issueMergeRefusalKey(issue, issueHasChildren(links, id)); key != "" {
		data.Locked = true
		data.Error = t(key)
	} else {
		data.FormContent = h.renderMergeForm(ctx, 0, csrfToken, "")
	}
	h.page.Render(w, r, t("issues.merge.title"), "/issues", IssueMergePage(data, t))
}

func (h *UIHandler) IssueMerge(w http.ResponseWriter, r *http.Request) {
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
	entity, err := h.xoluClient.Get(ctx, "issues", id)
	if err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	issue := issueFromEntity(entity)
	csrfToken := h.GetCSRFToken(w, r)
	canonicalID := toInt64FromString(strings.TrimSpace(r.FormValue("canonical_id")))
	renderErr := func(msg string) {
		data := IssueMergeData{
			Issue:       issue,
			CSRFToken:   csrfToken,
			Error:       msg,
			FormContent: h.renderMergeForm(ctx, canonicalID, csrfToken, msg),
		}
		_ = h.page.Render(w, r, t("issues.merge.title"), "/issues", IssueMergePage(data, t))
	}
	if !h.canActOnIssue(r, issue) {
		h.logger.Info("issue merge refused: not the responsible", "id", id, "assignee", issue.Assignee)
		h.Error403(w, r)
		return
	}
	links, err := h.listIssueLinks(ctx)
	if err != nil {
		h.logger.Error("failed to load issue links for merge", "id", id, "error", err)
		renderErr(t("issues.merge.error.failed"))
		return
	}
	if key := issueMergeRefusalKey(issue, issueHasChildren(links, id)); key != "" {
		h.logger.Info("issue merge refused", "id", id, "state", issue.State, "key", key)
		renderErr(t(key))
		return
	}
	var canonical *Issue
	if canonicalID > 0 && canonicalID != id {
		if canonicalEntity, gerr := h.xoluClient.Get(ctx, "issues", canonicalID); gerr == nil {
			c := issueFromEntity(canonicalEntity)
			canonical = &c
		}
	}
	if key := issueMergeCanonicalRefusalKey(id, canonicalID, canonical, issueHasChildren(links, canonicalID)); key != "" {
		h.logger.Info("issue merge canonical refused", "id", id, "canonical", canonicalID, "key", key)
		renderErr(t(key))
		return
	}
	h.issueTransition(w, r, "merge", map[string]interface{}{"canonical_id": canonicalID})
}

func IssueMergePage(data IssueMergeData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	iss := data.Issue
	return func(b *mi.Builder) mi.Node {
		children := []interface{}{
			b.Div(mi.Class("text-sm text-gray-700 dark:text-gray-300"), iss.Title),
			b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("issues.merge.help")),
		}
		if data.Error != "" {
			children = append(children, b.Div(mi.Class("text-sm text-red-600"), data.Error))
		}
		if !data.Locked && data.FormContent != nil {
			children = append(children, data.FormContent(b))
		}
		buttons := []interface{}{
			mi.Class("flex flex-wrap items-center justify-end gap-2"),
			b.A(mi.Href(fmt.Sprintf("/issues/%d", iss.ID)), mi.Class(issueActionClass(issueActionNeutral)),
				b.Span(mi.Class("material-icons text-base"), "close"),
				b.Span(t("action.cancel")),
			),
		}
		if !data.Locked {
			buttons = append(buttons, b.Button(mi.Type("submit"), mi.Class(issueActionClass(issueActionDanger)),
				b.Span(mi.Class("material-icons text-base"), "merge_type"),
				b.Span(t("issues.action.merge")),
			))
		}
		actions := []interface{}{
			mi.Class("flex flex-col gap-3 border-t border-gray-200 pt-4 sm:flex-row sm:items-center sm:justify-between dark:border-gray-700"),
			b.Div(buttons...),
		}
		return b.Div(mi.Class("space-y-6 max-w-2xl"),
			b.Div(mi.Class("flex items-center gap-4"),
				b.A(mi.Href(fmt.Sprintf("/issues/%d", iss.ID)), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.merge.title")),
			),
			Card("", func(b *mi.Builder) mi.Node {
				return b.Form(mi.Class("space-y-4"), mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/merge", iss.ID)),
					b.Div(children...),
					b.Div(actions...),
				)
			})(b),
		)
	}
}
