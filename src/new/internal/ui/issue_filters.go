package ui

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
)

// ─── Issue list filters ──────────────────────────────────────────────────────
//
// The issues list keeps its card layout (see mockups/01-listado-incidencias.html)
// but every filter is applied server-side, so filtering and pagination work
// together. This file owns the filter vocabulary, the OQL predicates, the
// query builders, the filter bar and the pagination; issues.go keeps the
// handler and the page skeleton.

// issueStateOrder lists the lifecycle states in display order. It is also the
// whitelist for the ?state= parameter.
var issueStateOrder = []string{
	"reported",
	"triaged",
	"in_progress",
	"on_hold",
	"resolved",
	"closed",
	"rejected",
	"duplicate",
}

// Closed vocabularies accepted by the list filters. Anything else is dropped
// by parseIssueListFilters, so these values are safe to embed in OQL.
var (
	issuePriorityFilterValues = []string{"critical", "high", "medium", "low"}
	issueSeverityFilterValues = []string{"critical", "major", "minor"}
	issueChannelFilterValues  = []string{"app", "qr", "api"}
	issueCategoryFilterValues = []string{"critico", "normal"}
)

// SLA bucket values accepted by ?sla=.
const (
	issueSLAOverdue = "overdue"
	issueSLADueSoon = "due_soon"
	issueSLAOnTime  = "on_time"
	issueSLANone    = "none"
)

// issueSLADueSoonWindow is how far ahead a deadline counts as due soon, both
// for the ?sla= filter and for the row badge.
const issueSLADueSoonWindow = 4 * time.Hour

// issueListPageSize is the number of rows shown per page.
const issueListPageSize = 25

// issueSLAClosedStates are the states where the resolution clock has stopped.
// Their deadline is neither filtered as overdue nor shown as a badge, since
// the row no longer has a running clock to report.
var issueSLAClosedStates = map[string]bool{
	"resolved":  true,
	"closed":    true,
	"rejected":  true,
	"duplicate": true,
}

// IssueListFilters is the validated, server-side filter set of the list. The
// zero value means "no filtering", which is what a bare /issues request gets.
type IssueListFilters struct {
	Search   string // ?q=, matched against the title and the linked asset name
	State    string // ?state=, one of issueStateOrder
	Priority string // ?priority=, one of issuePriorityFilterValues
	Severity string // ?severity=, one of issueSeverityFilterValues
	Channel  string // ?channel=, one of issueChannelFilterValues
	Category string // ?category=, one of issueCategoryFilterValues

	// Assignee and Reporter accept "none" (nobody), "me" (the current user)
	// or a numeric user id.
	Assignee string
	Reporter string

	// Asset accepts a numeric asset id or a name fragment, which the handler
	// resolves to ids before building the query.
	Asset string

	SLA  string // ?sla=, one of the issueSLA* constants
	From string // ?from=, inclusive creation date (YYYY-MM-DD)
	To   string // ?to=, inclusive creation date (YYYY-MM-DD)
	Page int    // 1-based; clamped by the handler once the total is known
}

// issueValidValue reports whether v is one of allowed.
func issueValidValue(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// parseIssueListFilters reads the query string and keeps only values from the
// whitelists above, so a hand-edited URL can never reach the OQL builder.
func parseIssueListFilters(q url.Values) IssueListFilters {
	f := IssueListFilters{
		Search:   strings.TrimSpace(q.Get("q")),
		State:    q.Get("state"),
		Priority: q.Get("priority"),
		Severity: q.Get("severity"),
		Channel:  q.Get("channel"),
		Category: q.Get("category"),
		Assignee: q.Get("assignee"),
		Reporter: q.Get("reporter"),
		Asset:    strings.TrimSpace(q.Get("asset")),
		SLA:      q.Get("sla"),
		From:     q.Get("from"),
		To:       q.Get("to"),
		Page:     1,
	}

	// A long search would only inflate the LIKE pattern; nothing legitimate
	// needs more than a couple of hundred characters.
	if len(f.Search) > 200 {
		f.Search = f.Search[:200]
	}
	if len(f.Asset) > 100 {
		f.Asset = f.Asset[:100]
	}

	if !issueValidValue(f.State, issueStateOrder) {
		f.State = ""
	}
	if !issueValidValue(f.Priority, issuePriorityFilterValues) {
		f.Priority = ""
	}
	if !issueValidValue(f.Severity, issueSeverityFilterValues) {
		f.Severity = ""
	}
	if !issueValidValue(f.Channel, issueChannelFilterValues) {
		f.Channel = ""
	}
	if !issueValidValue(f.Category, issueCategoryFilterValues) {
		f.Category = ""
	}
	switch f.SLA {
	case issueSLAOverdue, issueSLADueSoon, issueSLAOnTime, issueSLANone:
	default:
		f.SLA = ""
	}

	f.Assignee = parseIssueActorFilter(f.Assignee)
	f.Reporter = parseIssueActorFilter(f.Reporter)
	// A non-numeric asset value stays as a name fragment for the handler to
	// resolve to ids; the OQL builder only uses it after that resolution.
	f.From = parseIssueDateFilter(f.From)
	f.To = parseIssueDateFilter(f.To)

	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		f.Page = p
	}
	return f
}

// parseIssueActorFilter keeps the three accepted shapes for a user filter
// ("none", "me" or a numeric id) and drops anything else.
func parseIssueActorFilter(v string) string {
	switch v {
	case "", "none", "me":
		return v
	}
	if validOQLIntFilter(v) {
		return v
	}
	return ""
}

// parseIssueDateFilter keeps a valid YYYY-MM-DD date and drops the rest, so
// the value can be concatenated into the OQL literal safely.
func parseIssueDateFilter(v string) string {
	if v == "" {
		return ""
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return ""
	}
	return v
}

// issueChannelLabelKey maps a stored channel to its i18n label key, falling
// back to the app channel for rows created before the field existed.
func issueChannelLabelKey(channel string) string {
	if issueValidValue(channel, issueChannelFilterValues) {
		return "issues.channel." + channel
	}
	return "issues.channel.app"
}

// Active reports whether any filter is narrowing the list.
func (f IssueListFilters) Active() bool {
	return f.Search != "" || f.State != "" || f.Priority != "" ||
		f.Severity != "" || f.Channel != "" || f.Category != "" ||
		f.Assignee != "" || f.Reporter != "" || f.Asset != "" ||
		f.SLA != "" || f.From != "" || f.To != ""
}

// HasAdvanced reports whether a filter living in the "more filters" panel is
// set, so the page can render that panel open after a submit.
func (f IssueListFilters) HasAdvanced() bool {
	return f.Severity != "" || f.Category != "" ||
		f.Assignee != "" || f.Reporter != "" || f.Asset != "" ||
		f.SLA != "" || f.From != "" || f.To != ""
}

// values turns the filters back into query parameters, leaving out the empty
// ones and the page (pagination re-adds it).
func (f IssueListFilters) values() url.Values {
	v := url.Values{}
	set := func(key, value string) {
		if value != "" {
			v.Set(key, value)
		}
	}
	set("q", f.Search)
	set("state", f.State)
	set("priority", f.Priority)
	set("severity", f.Severity)
	set("channel", f.Channel)
	set("category", f.Category)
	set("assignee", f.Assignee)
	set("reporter", f.Reporter)
	set("asset", f.Asset)
	set("sla", f.SLA)
	set("from", f.From)
	set("to", f.To)
	return v
}

// link builds /issues with the current filters plus overrides. An empty
// override value removes that parameter, which is how a state tab, a quick
// view or the pagination re-targets the list without losing the rest.
func (f IssueListFilters) link(overrides map[string]string) string {
	v := f.values()
	for key, value := range overrides {
		if value == "" {
			v.Del(key)
		} else {
			v.Set(key, value)
		}
	}
	if len(v) == 0 {
		return "/issues"
	}
	return "/issues?" + v.Encode()
}

// pageLink is link for a specific page; page 1 drops the parameter entirely.
func (f IssueListFilters) pageLink(page int) string {
	if page <= 1 {
		return f.link(map[string]string{"page": ""})
	}
	return f.link(map[string]string{"page": strconv.Itoa(page)})
}

// withState adds the state predicate to a condition list. The state is kept
// apart because the tabs count per state with every other filter applied, so
// their query must not carry it.
func (f IssueListFilters) withState(conds []string) []string {
	if f.State == "" {
		return conds
	}
	return append(conds, "state = '"+escapeOQL(f.State)+"'")
}

// conditions turns the filters into OQL predicates, state excluded. meID is
// the current user id (for "me"); assetIDs are the ids resolved from a name
// fragment in f.Asset and searchAssetIDs those resolved from f.Search.
func (f IssueListFilters) conditions(meID int64, assetIDs, searchAssetIDs []int64) []string {
	var conds []string

	if f.Priority != "" {
		conds = append(conds, "priority = '"+escapeOQL(f.Priority)+"'")
	}
	if f.Severity != "" {
		conds = append(conds, "severity = '"+escapeOQL(f.Severity)+"'")
	}
	if f.Category != "" {
		conds = append(conds, "asset_category = '"+escapeOQL(f.Category)+"'")
	}
	if f.Channel != "" {
		conds = append(conds, issueChannelCondition(f.Channel))
	}
	conds = append(conds, issueActorCondition("assignee", f.Assignee, meID)...)
	conds = append(conds, issueActorCondition("reported_by", f.Reporter, meID)...)

	if f.Asset != "" {
		if validOQLIntFilter(f.Asset) {
			conds = append(conds, "asset_id = "+f.Asset)
		} else {
			conds = append(conds, issueAssetIDsCondition(assetIDs))
		}
	}
	if c := issueSLACondition(f.SLA, time.Now().UTC()); c != "" {
		conds = append(conds, c)
	}
	if f.From != "" {
		conds = append(conds, "created_at >= '"+f.From+"T00:00:00Z'")
	}
	if f.To != "" {
		conds = append(conds, "created_at <= '"+f.To+"T23:59:59Z'")
	}
	if f.Search != "" {
		like := "title LIKE '%" + escapeOQL(f.Search) + "%'"
		if len(searchAssetIDs) > 0 {
			like = "(" + like + " OR " + issueAssetIDsCondition(searchAssetIDs) + ")"
		}
		conds = append(conds, like)
	}
	return conds
}

// issueChannelCondition matches a channel. Rows created before the field
// existed have no channel and are treated as coming from the app.
func issueChannelCondition(channel string) string {
	if channel == "app" {
		return "(channel = 'app' OR channel IS NULL OR channel = '')"
	}
	return "channel = '" + escapeOQL(channel) + "'"
}

// issueActorCondition builds the predicate for a user filter. "none" covers
// both a missing field and a zero id, since entities leave unused fields out.
func issueActorCondition(column, value string, meID int64) []string {
	switch value {
	case "":
		return nil
	case "none":
		return []string{"(" + column + " IS NULL OR " + column + " = 0)"}
	case "me":
		if meID <= 0 {
			// No session user: match nothing rather than filtering by 0,
			// which would silently list the unassigned ones.
			return []string{column + " = -1"}
		}
		return []string{column + " = " + strconv.FormatInt(meID, 10)}
	default:
		return []string{column + " = " + value}
	}
}

// issueAssetIDsCondition matches any of the given asset ids; an empty set
// matches nothing (the name fragment resolved to no asset).
func issueAssetIDsCondition(ids []int64) string {
	if len(ids) == 0 {
		return "asset_id = -1"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return "asset_id IN (" + strings.Join(parts, ", ") + ")"
}

// issueSLACondition builds the predicate for a SLA bucket. Deadlines are
// RFC3339 UTC strings, so they compare lexicographically against "now".
func issueSLACondition(bucket string, now time.Time) string {
	hasDeadline := "sla_resolution_deadline IS NOT NULL AND sla_resolution_deadline <> ''"
	open := "state NOT IN ('resolved', 'closed', 'rejected', 'duplicate')"
	nowStr := now.Format(time.RFC3339)
	soonStr := now.Add(issueSLADueSoonWindow).Format(time.RFC3339)

	switch bucket {
	case issueSLANone:
		return "(sla_resolution_deadline IS NULL OR sla_resolution_deadline = '')"
	case issueSLAOverdue:
		return hasDeadline + " AND sla_resolution_deadline < '" + nowStr + "' AND " + open
	case issueSLADueSoon:
		return hasDeadline + " AND sla_resolution_deadline >= '" + nowStr + "' AND sla_resolution_deadline < '" + soonStr + "' AND " + open
	case issueSLAOnTime:
		return hasDeadline + " AND sla_resolution_deadline >= '" + soonStr + "' AND " + open
	}
	return ""
}

// issueSLAStatus classifies a deadline for the row badge: "", overdue,
// due_soon or on_time. Closed issues get no badge: their clock has stopped.
func issueSLAStatus(deadline, state string, now time.Time) string {
	deadline = strings.TrimSpace(deadline)
	if deadline == "" || issueSLAClosedStates[state] {
		return ""
	}
	due, err := time.Parse(time.RFC3339, deadline)
	if err != nil {
		return ""
	}
	switch {
	case due.Before(now):
		return issueSLAOverdue
	case due.Sub(now) <= issueSLADueSoonWindow:
		return issueSLADueSoon
	default:
		return issueSLAOnTime
	}
}

// issueSLABadge renders the SLA state of a row, or nil when the issue has no
// deadline to show.
func issueSLABadge(deadline, state string, now time.Time, t func(key string, args ...any) string) mi.H {
	status := issueSLAStatus(deadline, state, now)
	if status == "" {
		return nil
	}
	variant := "success"
	switch status {
	case issueSLAOverdue:
		variant = "danger"
	case issueSLADueSoon:
		variant = "warning"
	}
	return Badge(t("issues.sla."+status), variant)
}

// issuePriorityVariant maps a priority to a Badge variant.
func issuePriorityVariant(priority string) string {
	switch priority {
	case "critical":
		return "danger"
	case "high":
		return "warning"
	case "medium":
		return "info"
	default:
		return "default"
	}
}

// issueListWhere renders a WHERE clause from the conditions, or "" when
// there is nothing to filter.
func issueListWhere(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// issueStateCountsQuery counts issues per state with every filter except the
// state itself applied, so the tabs keep showing how many issues are behind
// each one under the current filters.
func issueStateCountsQuery(conds []string) string {
	return "SELECT state, COUNT(*) AS c FROM issues" + issueListWhere(conds) + " GROUP BY state"
}

// issueListCountQuery counts every issue matching the filters, for the page
// count and the "showing N of M" line.
func issueListCountQuery(conds []string) string {
	return "SELECT COUNT(*) AS c FROM issues" + issueListWhere(conds)
}

// issueListPageQuery fetches one page of rows, newest first.
func issueListPageQuery(conds []string, page, pageSize int) string {
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize
	return "SELECT * FROM issues" + issueListWhere(conds) +
		" ORDER BY id DESC" +
		" OFFSET " + strconv.Itoa(offset) + " ROWS" +
		" FETCH NEXT " + strconv.Itoa(pageSize) + " ROWS ONLY"
}

// issueListTotalPages is the number of pages for a total, at least 1 so the
// page never divides by zero.
func issueListTotalPages(total, pageSize int) int {
	if pageSize <= 0 || total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

// ─── Filter bar ──────────────────────────────────────────────────────────────

// issueSelectOptions builds a select's options from a label key prefix plus
// a list of values, marking the current one and adding a "no filter" first
// option whose label comes from anyLabelKey.
func issueSelectOptions(values []string, current, labelPrefix, anyLabelKey string, t func(key string, args ...any) string) []SelectOption {
	opts := []SelectOption{{Value: "", Label: t(anyLabelKey), Selected: current == ""}}
	for _, v := range values {
		opts = append(opts, SelectOption{
			Value:    v,
			Label:    t(labelPrefix + v),
			Selected: current == v,
		})
	}
	return opts
}

// issueFilterSelect renders one labelled <select> that submits the form as
// soon as it changes, the same behaviour the shared filter bar uses.
func issueFilterSelect(name, label string, options []SelectOption) mi.H {
	return func(b *mi.Builder) mi.Node {
		attrs := []interface{}{
			mi.Name(name),
			mi.ID("issue-filter-" + name),
			mi.Class("mt-1 w-full rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 px-3 py-2 text-sm"),
			mi.Attr("onchange", "this.form.submit()"),
		}
		attrs = append(attrs, selectOptions(b, options)...)
		return b.Div(
			b.Label(mi.For("issue-filter-"+name), mi.Class("block text-xs font-medium text-gray-500 dark:text-gray-400"), label),
			b.Select(attrs...),
		)
	}
}

// issueFilterInput renders one labelled text/date input of the panel.
func issueFilterInput(name, label, value, inputType, placeholder string) mi.H {
	return func(b *mi.Builder) mi.Node {
		return b.Div(
			b.Label(mi.For("issue-filter-"+name), mi.Class("block text-xs font-medium text-gray-500 dark:text-gray-400"), label),
			b.Input(
				mi.Type(inputType),
				mi.Name(name),
				mi.ID("issue-filter-"+name),
				mi.Value(value),
				mi.Placeholder(placeholder),
				mi.Class("mt-1 w-full rounded-lg border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-700 px-3 py-2 text-sm"),
			),
		)
	}
}

// issueQuickView is one of the predefined views above the list. Each one is
// just a URL that sets (or clears) a filter, so there is no extra screen.
type issueQuickView struct {
	LabelKey string
	Param    string
	Value    string
	Active   bool
}

// issueQuickViews returns the predefined views for the current filter set.
func issueQuickViews(f IssueListFilters) []issueQuickView {
	return []issueQuickView{
		{LabelKey: "issues.view.mine", Param: "assignee", Value: "me", Active: f.Assignee == "me"},
		{LabelKey: "issues.view.unassigned", Param: "assignee", Value: "none", Active: f.Assignee == "none"},
		{LabelKey: "issues.view.overdue", Param: "sla", Value: issueSLAOverdue, Active: f.SLA == issueSLAOverdue},
		{LabelKey: "issues.view.high_priority", Param: "priority", Value: "high", Active: f.Priority == "high"},
	}
}

// issueListFilterBar renders the GET form: the search box with the two
// top-level filters, the collapsible advanced panel, the state tabs and the
// quick views. Every control keeps the rest of the filters because the form
// submits the whole set and the links are built from it.
func issueListFilterBar(f IssueListFilters, counts map[string]int, t func(key string, args ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		active := f.Active()

		// Top row: search + priority + channel, mirroring the mockup.
		top := []interface{}{
			mi.Method("get"),
			mi.Action("/issues"),
			mi.Class("flex flex-wrap items-end gap-4"),
		}
		// The state lives in the tabs, not in a control of this form, so it
		// rides along as a hidden field: changing a select must not drop the
		// active tab.
		if f.State != "" {
			top = append(top, b.Input(mi.Type("hidden"), mi.Name("state"), mi.Value(f.State)))
		}
		searchChildren := []interface{}{
			mi.Class("relative flex-1 min-w-[16rem] max-w-md"),
		}
		searchChildren = append(searchChildren,
			SearchInput("q", t("issues.search_placeholder"), f.Search,
				mi.ID("issue-search"),
				mi.Attr("style", "padding-right:2.5rem"),
			)(b),
		)
		if f.Search != "" {
			searchChildren = append(searchChildren,
				b.A(mi.Href(f.link(map[string]string{"q": ""})),
					mi.Class("absolute right-3 top-1/2 -translate-y-1/2 material-icons text-lg text-gray-400 hover:text-gray-600 dark:hover:text-gray-200"),
					mi.Attr("aria-label", t("issues.search_clear")),
					"close",
				),
			)
		}
		top = append(top, b.Div(searchChildren...))
		top = append(top,
			issueFilterSelect("priority", t("issues.priority.label"),
				issueSelectOptions(issuePriorityFilterValues, f.Priority, "issues.priority.", "issues.filter.priority_all", t))(b),
			issueFilterSelect("channel", t("issues.filter.channel"),
				issueSelectOptions(issueChannelFilterValues, f.Channel, "issues.channel.", "issues.filter.channel_all", t))(b),
		)

		// Advanced panel: the rest of the filters, collapsed unless one is
		// in use.
		panelFields := []interface{}{
			issueFilterSelect("assignee", t("issues.filter.assignee"), []SelectOption{
				{Value: "", Label: t("issues.filter.assignee_all"), Selected: f.Assignee == ""},
				{Value: "me", Label: t("issues.filter.me"), Selected: f.Assignee == "me"},
				{Value: "none", Label: t("issues.unassigned"), Selected: f.Assignee == "none"},
			})(b),
		}
		panelFields = append(panelFields,
			issueFilterSelect("reporter", t("issues.filter.reporter"), []SelectOption{
				{Value: "", Label: t("issues.filter.reporter_all"), Selected: f.Reporter == ""},
				{Value: "me", Label: t("issues.filter.me"), Selected: f.Reporter == "me"},
			})(b),
			issueFilterInput("asset", t("issues.filter.asset"), f.Asset, "text", t("issues.filter.asset_hint"))(b),
			issueFilterSelect("severity", t("issues.severity.label"),
				issueSelectOptions(issueSeverityFilterValues, f.Severity, "issues.severity.", "issues.filter.severity_all", t))(b),
			issueFilterSelect("category", t("issues.filter.category"),
				issueSelectOptions(issueCategoryFilterValues, f.Category, "issues.category.", "issues.filter.category_all", t))(b),
			issueFilterSelect("sla", t("issues.filter.sla"), []SelectOption{
				{Value: "", Label: t("issues.filter.sla_all"), Selected: f.SLA == ""},
				{Value: issueSLAOverdue, Label: t("issues.sla.overdue"), Selected: f.SLA == issueSLAOverdue},
				{Value: issueSLADueSoon, Label: t("issues.sla.due_soon"), Selected: f.SLA == issueSLADueSoon},
				{Value: issueSLAOnTime, Label: t("issues.sla.on_time"), Selected: f.SLA == issueSLAOnTime},
				{Value: issueSLANone, Label: t("issues.filter.sla_none"), Selected: f.SLA == issueSLANone},
			})(b),
			issueFilterInput("from", t("issues.filter.created_from"), f.From, "date", "")(b),
			issueFilterInput("to", t("issues.filter.created_to"), f.To, "date", "")(b),
		)
		panelFields = append(panelFields,
			b.Div(mi.Class("flex items-end"),
				b.Button(mi.Type("submit"),
					mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 text-sm font-medium"),
					t("action.filter"),
				),
			),
		)

		panelAttrs := []interface{}{
			mi.Class("mt-4 rounded-lg border border-gray-200 dark:border-gray-700 p-4"),
		}
		if f.HasAdvanced() {
			panelAttrs = append(panelAttrs, mi.Attr("open", "open"))
		}
		panelAttrs = append(panelAttrs,
			b.Summary(mi.Class("cursor-pointer text-sm font-medium text-gray-600 dark:text-gray-300"), t("issues.filter.more")),
			b.Div(append([]interface{}{mi.Class("mt-4 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4")}, panelFields...)...),
		)

		formChildren := []interface{}{}
		formChildren = append(formChildren, top...)
		formChildren = append(formChildren, b.Details(panelAttrs...))

		// State tabs: same counts as before, but every link now carries the
		// active filters.
		tabs := []interface{}{mi.ID("issue-tabs"), mi.Class("flex flex-wrap gap-2")}
		allCount := 0
		for _, c := range counts {
			allCount += c
		}
		tabLink := func(state, label string, count int, active bool) mi.Node {
			cls := "px-3 py-1.5 text-sm rounded-lg border transition-colors "
			if active {
				cls += "bg-indigo-600 text-white border-indigo-600"
			} else {
				cls += "bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700"
			}
			return b.A(mi.Href(f.link(map[string]string{"state": state})), mi.Class(cls),
				label, " (", strconv.Itoa(count), ")")
		}
		tabs = append(tabs, tabLink("", t("issues.all"), allCount, f.State == ""))
		for _, st := range issueStateOrder {
			tabs = append(tabs, tabLink(st, t("issues.state."+st), counts[st], f.State == st))
		}

		quick := []interface{}{mi.Class("flex flex-wrap items-center gap-2")}
		for _, view := range issueQuickViews(f) {
			cls := "px-3 py-1.5 text-sm rounded-full border transition-colors "
			if view.Active {
				cls += "bg-indigo-600 text-white border-indigo-600"
			} else {
				cls += "bg-white dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700"
			}
			// Clicking an active chip clears its filter.
			value := view.Value
			if view.Active {
				value = ""
			}
			quick = append(quick,
				b.A(mi.Href(f.link(map[string]string{view.Param: value})), mi.Class(cls), t(view.LabelKey)),
			)
		}
		if active {
			quick = append(quick,
				b.A(mi.Href("/issues"), mi.Class("ml-auto text-sm text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200 underline"),
					t("issues.filter.clear"),
				),
			)
		}

		return b.Div(mi.Class("space-y-4"),
			b.Form(formChildren...),
			b.Div(tabs...),
			b.Div(quick...),
		)
	}
}

// issueListPagination renders the page links below the list, preserving the
// filters (the shared Pagination helper always appends ?page=N, which would
// drop them).
func issueListPagination(f IssueListFilters, visible, total int, t func(key string, args ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		totalPages := issueListTotalPages(total, issueListPageSize)
		countLine := b.Div(mi.Class("text-sm text-gray-500 dark:text-gray-400"),
			t("pagination.showing", visible, total, t("issues.list_title")),
		)
		if totalPages <= 1 {
			return b.Div(mi.Class("flex items-center justify-between"), countLine)
		}

		pages := []interface{}{mi.Class("flex items-center gap-1")}
		prevClass := "px-3 py-2 rounded-lg text-sm font-medium"
		if f.Page <= 1 {
			pages = append(pages, b.Span(mi.Class(prevClass+" text-gray-400 cursor-not-allowed"), t("action.previous")))
		} else {
			pages = append(pages,
				b.A(mi.Href(f.pageLink(f.Page-1)), mi.Class(prevClass+" text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-700"), t("action.previous")),
			)
		}
		for i := 1; i <= totalPages; i++ {
			switch {
			case i == f.Page:
				pages = append(pages, b.Span(mi.Class("px-3 py-2 rounded-lg text-sm font-medium bg-indigo-600 text-white"), strconv.Itoa(i)))
			case i == 1 || i == totalPages || (i >= f.Page-2 && i <= f.Page+2):
				pages = append(pages,
					b.A(mi.Href(f.pageLink(i)), mi.Class("px-3 py-2 rounded-lg text-sm font-medium text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-700"), strconv.Itoa(i)),
				)
			case i == f.Page-3 || i == f.Page+3:
				pages = append(pages, b.Span(mi.Class("px-2"), "..."))
			}
		}
		nextClass := "px-3 py-2 rounded-lg text-sm font-medium"
		if f.Page >= totalPages {
			pages = append(pages, b.Span(mi.Class(nextClass+" text-gray-400 cursor-not-allowed"), t("action.next")))
		} else {
			pages = append(pages,
				b.A(mi.Href(f.pageLink(f.Page+1)), mi.Class(nextClass+" text-gray-700 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-gray-700"), t("action.next")),
			)
		}

		return b.Div(mi.Class("flex flex-wrap items-center justify-between gap-4"),
			countLine,
			b.Div(pages...),
		)
	}
}
