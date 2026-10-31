package ui

// Group issues into a case (iteration 2, "Agrupar incidencias en un Issue
// Madre") -- no new entity: the oldest issue of the selection is promoted to
// the case and the rest get master_id pointing at it. That keeps the case's
// own machine (with its history) instead of creating a parallel entity whose
// FSM would have to be kept in sync by hand, and it keeps the hierarchy to a
// single level: a child is never a case.
//
// The case is the only management point. canActOnIssue and canAssignIssue
// refuse children outright, and every lifecycle step on the case is replayed
// on its children (propagateIssueTransition / propagateIssueAssignment), so
// the group cannot drift apart.
//
// Grouping is the administrative panel's action: only admins may open
// /issues/group, submit the grouping or detach a child. The module itself is
// mounted with requireAuth only, so the gate lives in these handlers, the
// same way assignment does.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
	"github.com/ha1tch/seam-ui/internal/auth"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// isIssueTerminal reports whether the state ends the issue's lifecycle. A
// terminal issue is never grouped: it is already closed history.
func isIssueTerminal(state string) bool {
	return state == "closed" || state == "rejected" || state == "duplicate"
}

// issueGroupPlan is the outcome of planning a grouping: Keep is promoted to
// the case, Join gets linked to it. Error holds the i18n key explaining why
// the selection cannot be grouped ("" when the plan is valid).
type issueGroupPlan struct {
	Keep  Issue
	Join  []Issue
	Error string
}

// planIssueGroup decides what a selection would become. Pure on purpose: the
// rules of the card (same asset, same state, non-terminal, not already a
// child, not already a case, at least two) are worth testing without a
// server, and the handler then only has to execute the plan.
func planIssueGroup(issues []Issue, hasChildren map[int64]bool) issueGroupPlan {
	// Deduplicate: the picker can only submit each id once, but a pure
	// function must not depend on the caller being careful.
	seen := map[int64]bool{}
	unique := make([]Issue, 0, len(issues))
	for _, iss := range issues {
		if iss.ID <= 0 || seen[iss.ID] {
			continue
		}
		seen[iss.ID] = true
		unique = append(unique, iss)
	}
	if len(unique) < 2 {
		return issueGroupPlan{Error: "issues.group.error.too_few"}
	}

	for _, iss := range unique {
		if iss.MasterID > 0 {
			return issueGroupPlan{Error: "issues.group.error.child_selected"}
		}
	}
	for _, iss := range unique {
		if hasChildren[iss.ID] {
			return issueGroupPlan{Error: "issues.group.error.master_selected"}
		}
	}
	for _, iss := range unique {
		if isIssueTerminal(iss.State) {
			return issueGroupPlan{Error: "issues.group.error.terminal"}
		}
	}

	keep := unique[0]
	for _, iss := range unique {
		if iss.State != unique[0].State {
			return issueGroupPlan{Error: "issues.group.error.state_mismatch"}
		}
		if iss.AssetID != unique[0].AssetID {
			return issueGroupPlan{Error: "issues.group.error.asset_mismatch"}
		}
		if iss.ID < keep.ID {
			keep = iss
		}
	}

	plan := issueGroupPlan{Keep: keep}
	for _, iss := range unique {
		if iss.ID != keep.ID {
			plan.Join = append(plan.Join, iss)
		}
	}
	return plan
}

// canManageIssueGroups reports whether the current user may group, ungroup
// or otherwise administer the grouping: admins only.
func (h *UIHandler) canManageIssueGroups(r *http.Request) bool {
	u := h.getCurrentUser(r)
	if u == nil || u.ID == 0 {
		return false
	}
	return auth.RoleRank(u.Role) >= auth.RoleRank(auth.RoleAdmin)
}

// listIssueLinks returns every issue that points at a case, in one read.
// Deliberately a plain SELECT filtered in Go, with no numeric WHERE clause:
// this is the only OQL shape already proven against this xolu version, and a
// wrong guess here would break propagation silently. Revisit if the issues
// table grows past a few thousand rows.
func (h *UIHandler) listIssueLinks(ctx context.Context) ([]Issue, error) {
	res, err := h.xoluClient.OQL(ctx, "SELECT id, title, state, machine_id, master_id FROM issues ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(res.Data))
	for _, row := range res.Data {
		masterID := toInt64(row["master_id"])
		if masterID <= 0 {
			continue
		}
		out = append(out, Issue{
			ID:        toInt64(row["id"]),
			Title:     toString(row["title"]),
			State:     toString(row["state"]),
			MachineID: toInt64(row["machine_id"]),
			MasterID:  masterID,
		})
	}
	return out, nil
}

// propagateIssueTransition replays a lifecycle input on every child of a
// case: each child walks its own machine with the same input (and payload),
// then denormalizes the resulting state. extra carries the fields that only
// some inputs write (reject_reason). A child whose machine refuses the walk
// is logged and left where it is -- the case stays the source of truth, and
// the history says what happened.
func (h *UIHandler) propagateIssueTransition(ctx context.Context, master Issue, input string, payload map[string]interface{}, extra map[string]any) {
	links, err := h.listIssueLinks(ctx)
	if err != nil {
		h.logger.Error("failed to load the children of a case", "id", master.ID, "error", err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	propagated := 0
	for _, child := range links {
		if child.MasterID != master.ID {
			continue
		}
		result, werr := h.xoluClient.WalkMachine(ctx, child.MachineID, xolu.WalkRequest{Input: input, Payload: payload})
		if werr != nil {
			h.logger.Info("issue child walk rejected", "id", child.ID, "case", master.ID, "input", input, "error", werr)
			continue
		}
		patch := map[string]any{"state": result.Current, "updated_at": now}
		for k, v := range extra {
			patch[k] = v
		}
		if _, perr := h.xoluClient.Patch(ctx, "issues", child.ID, patch); perr != nil {
			h.logger.Error("issue child walked but state patch failed", "id", child.ID, "case", master.ID, "error", perr)
			continue
		}
		propagated++
	}
	if propagated > 0 {
		h.logger.Info("issue transition propagated", "id", master.ID, "input", input, "children", propagated)
	}
}

// propagateIssueAssignment hands a case's new responsible to its children.
// state is the case's new state when the assignment also fired "triage" (nil
// otherwise): in that case each child is triaged too, so the group stays in
// one state. A child whose triage walk is refused keeps its state and only
// gets the responsible -- logged, never fatal.
func (h *UIHandler) propagateIssueAssignment(ctx context.Context, master Issue, assignee int64, state any) {
	links, err := h.listIssueLinks(ctx)
	if err != nil {
		h.logger.Error("failed to load the children of a case", "id", master.ID, "error", err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, child := range links {
		if child.MasterID != master.ID {
			continue
		}
		patch := map[string]any{"assignee": assignee, "updated_at": now}
		if state != nil {
			if result, werr := h.xoluClient.WalkMachine(ctx, child.MachineID, xolu.WalkRequest{Input: "triage"}); werr == nil {
				patch["state"] = result.Current
			} else {
				h.logger.Info("issue child triage walk rejected", "id", child.ID, "case", master.ID, "error", werr)
			}
		}
		if _, perr := h.xoluClient.Patch(ctx, "issues", child.ID, patch); perr != nil {
			h.logger.Error("issue child assignment patch failed", "id", child.ID, "case", master.ID, "error", perr)
		}
	}
}

// IssueGroupData holds data for the grouping page.
type IssueGroupData struct {
	Candidates []Issue
	CSRFToken  string
	Error      string
}

// groupableIssues lists the issues the picker may offer: not terminal, not
// already a child and not already a case.
func (h *UIHandler) groupableIssues(ctx context.Context) ([]Issue, error) {
	res, err := h.xoluClient.OQL(ctx, "SELECT TOP 200 id, title, state, asset_id, master_id FROM issues ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	links, err := h.listIssueLinks(ctx)
	if err != nil {
		return nil, err
	}
	cases := map[int64]bool{}
	for _, l := range links {
		cases[l.MasterID] = true
	}
	out := make([]Issue, 0, len(res.Data))
	for _, row := range res.Data {
		iss := Issue{
			ID:       toInt64(row["id"]),
			Title:    toString(row["title"]),
			State:    toString(row["state"]),
			AssetID:  toInt64(row["asset_id"]),
			MasterID: toInt64(row["master_id"]),
		}
		if iss.MasterID > 0 || cases[iss.ID] || isIssueTerminal(iss.State) {
			continue
		}
		out = append(out, iss)
	}
	return out, nil
}

// readIssueGroupIDs reads the selection. The picker submits one "ids" field
// per checked box; accepting a comma-separated value too costs three lines
// and makes the endpoint usable from a form built by hand.
func readIssueGroupIDs(r *http.Request) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, 4)
	for _, raw := range r.Form["ids"] {
		for _, part := range strings.Split(raw, ",") {
			id := toInt64FromString(strings.TrimSpace(part))
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// IssueGroupFormPage handles GET /issues/group -- the administrative picker.
// Admins only: grouping is the panel's action.
func (h *UIHandler) IssueGroupFormPage(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	if !h.canManageIssueGroups(r) {
		h.logger.Info("issue grouping page refused: not an admin")
		h.Error403(w, r)
		return
	}
	data := IssueGroupData{CSRFToken: h.GetCSRFToken(w, r)}
	candidates, err := h.groupableIssues(r.Context())
	if err != nil {
		h.logger.Error("failed to load groupable issues", "error", err)
	}
	data.Candidates = candidates
	h.page.Render(w, r, t("issues.group.title"), "/issues", IssueGroupPage(data, t))
}

// IssueGroup handles POST /issues/group -- validates the selection with
// planIssueGroup and, when it holds, links every joiner to the oldest issue
// of the group. A refused selection re-renders the picker with the reason;
// nothing is written.
func (h *UIHandler) IssueGroup(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	if !h.canManageIssueGroups(r) {
		h.logger.Info("issue grouping refused: not an admin")
		h.Error403(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	renderErr := func(msg string) {
		data := IssueGroupData{CSRFToken: h.GetCSRFToken(w, r), Error: msg}
		if candidates, err := h.groupableIssues(ctx); err == nil {
			data.Candidates = candidates
		}
		_ = h.page.Render(w, r, t("issues.group.title"), "/issues", IssueGroupPage(data, t))
	}

	ids := readIssueGroupIDs(r)
	selected := make([]Issue, 0, len(ids))
	for _, id := range ids {
		entity, err := h.xoluClient.Get(ctx, "issues", id)
		if err != nil {
			h.logger.Error("failed to load an issue for grouping", "id", id, "error", err)
			renderErr(t("issues.group.error.failed"))
			return
		}
		selected = append(selected, issueFromEntity(entity))
	}

	links, err := h.listIssueLinks(ctx)
	if err != nil {
		h.logger.Error("failed to load issue grouping links", "error", err)
		renderErr(t("issues.group.error.failed"))
		return
	}
	hasChildren := map[int64]bool{}
	for _, l := range links {
		hasChildren[l.MasterID] = true
	}

	plan := planIssueGroup(selected, hasChildren)
	if plan.Error != "" {
		h.logger.Info("issue grouping refused", "error", plan.Error)
		renderErr(t(plan.Error))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, child := range plan.Join {
		if _, err := h.xoluClient.Patch(ctx, "issues", child.ID, map[string]any{
			"master_id":  plan.Keep.ID,
			"updated_at": now,
		}); err != nil {
			h.logger.Error("issue grouping patch failed", "id", child.ID, "case", plan.Keep.ID, "error", err)
			renderErr(t("issues.group.error.failed"))
			return
		}
	}

	h.logger.Info("issues grouped", "case", plan.Keep.ID, "children", len(plan.Join))
	http.Redirect(w, r, "/issues/"+strconv.FormatInt(plan.Keep.ID, 10), http.StatusSeeOther)
}

// IssueUngroup handles POST /issues/{id}/ungroup -- detaches one child from
// its case, after which it is managed on its own again. Admins only.
func (h *UIHandler) IssueUngroup(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	if !h.canManageIssueGroups(r) {
		h.logger.Info("issue ungroup refused: not an admin", "id", id)
		h.Error403(w, r)
		return
	}
	entity, err := h.xoluClient.Get(ctx, "issues", id)
	if err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("error.not_found"))
		return
	}
	issue := issueFromEntity(entity)
	if issue.MasterID == 0 {
		// Already standalone: nothing to detach, and the request is
		// harmless, so it just goes back to the detail page.
		http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
		return
	}
	if _, err := h.xoluClient.Patch(ctx, "issues", id, map[string]any{
		"master_id":  int64(0),
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		h.logger.Error("issue ungroup patch failed", "id", id, "error", err)
		h.page.RenderError(w, http.StatusInternalServerError, t("issues.group.error.failed"), t("issues.group.error.failed"))
		return
	}
	h.logger.Info("issue detached from its case", "id", id, "case", issue.MasterID)
	http.Redirect(w, r, "/issues/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// IssueGroupPage renders the picker. Every checkbox posts the same "ids"
// field, so the handler reads the selection as a list.
func IssueGroupPage(data IssueGroupData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	return func(b *mi.Builder) mi.Node {
		var rows []interface{}
		for _, iss := range data.Candidates {
			assetLine := t("issues.no_asset")
			if iss.AssetID > 0 {
				assetLine = t("issues.linked_to") + ": #" + strconv.FormatInt(iss.AssetID, 10)
			}
			rows = append(rows, b.Label(
				mi.Class("flex items-start gap-3 px-4 py-3 border-b border-gray-100 dark:border-gray-700 last:border-b-0 cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-700/50"),
				b.Input(mi.Type("checkbox"), mi.Name("ids"), mi.Value(strconv.FormatInt(iss.ID, 10)), mi.Class("mt-1 h-4 w-4 text-indigo-600")),
				b.Span(mi.Class("flex-1"),
					b.Span(mi.Class("block text-sm font-medium text-gray-900 dark:text-white"),
						"#"+strconv.FormatInt(iss.ID, 10)+" · "+iss.Title),
					b.Span(mi.Class("block text-xs text-gray-500 dark:text-gray-400"), assetLine+" · "+t("issues.state."+iss.State)),
				),
				issueStatusBadge(iss.State, t)(b),
			))
		}

		var list mi.Node
		if len(rows) == 0 {
			list = b.Div(mi.Class("px-4 py-8 text-center text-gray-500 dark:text-gray-400"), t("issues.group.empty"))
		} else {
			list = b.Div(append([]interface{}{mi.Class("rounded-lg border border-gray-200 dark:border-gray-700")}, rows...)...)
		}

		body := []interface{}{
			b.P(mi.Class("text-sm text-gray-600 dark:text-gray-400"), t("issues.group.keep_note")),
		}
		if data.Error != "" {
			body = append(body, b.Div(
				mi.Class("px-3 py-2 rounded-lg bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-sm text-red-700 dark:text-red-400"),
				data.Error))
		}
		body = append(body, list)
		if len(rows) > 0 {
			body = append(body, b.Div(mi.Class("flex justify-end gap-3 pt-4"),
				b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
					t("issues.group.confirm")),
			))
		}

		return b.Div(mi.Class("space-y-6 max-w-3xl"),
			b.Div(mi.Class("flex items-center gap-4"),
				b.A(mi.Href("/issues"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.group.title")),
			),
			Card("", func(b *mi.Builder) mi.Node {
				return b.Form(mi.Method("post"), mi.Action("/issues/group"),
					b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
					b.Div(append([]interface{}{mi.Class("space-y-4")}, body...)...),
				)
			})(b),
		)
	}
}
