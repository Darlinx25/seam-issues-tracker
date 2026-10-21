package ui

// Assign responsible (card 4) -- a dedicated /issues/{id}/assign page. It
// sets the issue's assignee (a data field) and, when the issue is still
// "reported", also fires the "triage" transition, so assigning doubles as
// the triage step. On any other non-terminal state it only rewrites the
// assignee; terminal issues are refused. The person is chosen with the
// generic any-picker widget bound to entity:users, whose backend already
// goes through auth.UserService -- no formengine change is needed, and the
// schema is inline (resolveSchema) so nothing is added under
// internal/formengine/schemas.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
	fe "github.com/ha1tch/seam-ui/internal/formengine"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// issueAssignSchema is the single-field form for the assign page. It reuses
// the any-picker widget (data-seam-picker) over the users entity;
// x-seam-selector adds the Browse dialog backed by the Users selector.
var issueAssignSchema = []byte(`{
  "type": "object",
  "properties": {
    "assignee": {
      "type": "integer",
      "title": "Assignee",
      "x-seam-input": "any-picker",
      "x-seam-source": "entity:users",
      "x-seam-selector": "users"
    }
  }
}`)

// IssueAssignData holds data for the assign page.
type IssueAssignData struct {
	Issue       Issue
	CSRFToken   string
	FormContent mi.H
	Error       string
}

// renderAssignForm renders the picker with the given current value and an
// optional error, shared by the GET page and the POST re-render.
func (h *UIHandler) renderAssignForm(ctx context.Context, assignee int64, csrfToken, errMsg string) mi.H {
	formEng := &fe.FormEngine{Translator: h.formEngineTranslator()}
	rc := fe.RenderContext{Locale: h.currentLocale(), CSRFToken: csrfToken, Module: "issues", Form: "assign"}
	defaults := map[string]any{}
	if assignee > 0 {
		defaults["assignee"] = assignee
	}
	var errs map[string]string
	if errMsg != "" {
		errs = map[string]string{"assignee": errMsg}
	}
	return formEng.Render(ctx, resolveSchema(ctx, "issues", "assign", issueAssignSchema), defaults, errs, rc)
}

// IssueAssignFormPage handles GET /issues/{id}/assign.
func (h *UIHandler) IssueAssignFormPage(w http.ResponseWriter, r *http.Request) {
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
	csrfToken := h.GetCSRFToken(w, r)
	data := IssueAssignData{
		Issue:       issue,
		CSRFToken:   csrfToken,
		FormContent: h.renderAssignForm(ctx, issue.Assignee, csrfToken, ""),
	}
	h.page.Render(w, r, t("issues.assign.title"), "/issues", IssueAssignPage(data, t))
}

// IssueAssign handles POST /issues/{id}/assign. In "reported" it walks the
// machine with "triage" and stores the new state together with the
// assignee; in any other non-terminal state it only rewrites the assignee.
// Terminal issues are refused before the FSM is touched.
func (h *UIHandler) IssueAssign(w http.ResponseWriter, r *http.Request) {
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
	assignee := toInt64FromString(strings.TrimSpace(r.FormValue("assignee")))

	entity, err := h.xoluClient.Get(ctx, "issues", id)
	if err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	issue := issueFromEntity(entity)
	csrfToken := h.GetCSRFToken(w, r)

	renderErr := func(msg string) {
		data := IssueAssignData{
			Issue:       issue,
			CSRFToken:   csrfToken,
			FormContent: h.renderAssignForm(ctx, issue.Assignee, csrfToken, msg),
			Error:       msg,
		}
		_ = h.page.Render(w, r, t("issues.assign.title"), "/issues", IssueAssignPage(data, t))
	}

	if issue.State == "closed" || issue.State == "rejected" || issue.State == "duplicate" {
		h.logger.Info("issue assign refused: terminal state", "id", id, "state", issue.State)
		renderErr(t("issues.assign.error.terminal"))
		return
	}
	if assignee <= 0 {
		h.logger.Info("issue assign refused: assignee required", "id", id)
		renderErr(t("issues.assign.error.assignee_required"))
		return
	}

	patchData := map[string]any{
		"assignee":   assignee,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if issue.State == "reported" {
		result, werr := h.xoluClient.WalkMachine(ctx, issue.MachineID, xolu.WalkRequest{Input: "triage"})
		if werr != nil {
			h.logger.Info("issue assign triage walk failed", "id", id, "error", werr)
			renderErr(werr.Error())
			return
		}
		patchData["state"] = result.Current
	}
	if _, err := h.xoluClient.Patch(ctx, "issues", id, patchData); err != nil {
		h.logger.Error("issue assigned but patch failed", "id", id, "error", err)
	}

	http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// IssueAssignPage renders the assign form.
func IssueAssignPage(data IssueAssignData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	iss := data.Issue
	return func(b *mi.Builder) mi.Node {
		var children []interface{}
		children = append(children,
			b.Div(mi.Class("text-sm text-gray-700 dark:text-gray-300"), iss.Title),
		)
		if data.Error != "" {
			children = append(children, b.Div(mi.Class("text-sm text-red-600"), data.Error))
		}
		children = append(children, data.FormContent(b))
		return b.Div(mi.Class("space-y-6 max-w-2xl"),
			b.Div(mi.Class("flex items-center gap-4"),
				b.A(mi.Href(fmt.Sprintf("/issues/%d", iss.ID)), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.assign.title")),
			),
			Card("", func(b *mi.Builder) mi.Node {
				return b.Form(mi.Class("space-y-4"), mi.Method("post"), mi.Action(fmt.Sprintf("/issues/%d/assign", iss.ID)),
					b.Div(children...),
					b.Div(mi.Class("flex justify-end gap-3 pt-4"),
						b.A(mi.Href(fmt.Sprintf("/issues/%d", iss.ID)), mi.Class("px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg font-medium hover:bg-gray-50 dark:hover:bg-gray-700"),
							t("action.cancel"),
						),
						b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
							t("issues.action.assign"),
						),
					),
				)
			})(b),
		)
	}
}
