package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	mi "github.com/ha1tch/minty"
)

const issueQRAdminPageSize = 25

// IssueQRCodeAdminFilters is the validated filter set for the admin QR list.
type IssueQRCodeAdminFilters struct {
	Search  string // q: asset name substring
	AssetID int64  // asset: specific asset id
	Status  string // active | revoked | all
	Page    int
}

// issueQRAdminRow is one rendered row.
type issueQRAdminRow struct {
	ID          int64
	Code        string
	AssetID     int64
	AssetName   string
	Active      bool
	CreatedBy   int64
	CreatorName string
	CreatedAt   string
	RevokedAt   string
}

type IssueQRCodesListData struct {
	Filters   IssueQRCodeAdminFilters
	Rows      []issueQRAdminRow
	Total     int
	CSRFToken string
	Notice    string
	Error     string
}

func parseIssueQRAdminFilters(q url.Values) IssueQRCodeAdminFilters {
	f := IssueQRCodeAdminFilters{
		Search: strings.TrimSpace(q.Get("q")),
		Status: q.Get("status"),
		Page:   1,
	}
	if f.Status != "active" && f.Status != "revoked" {
		f.Status = "all"
	}
	if v := strings.TrimSpace(q.Get("asset")); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			f.AssetID = id
		}
	}
	if v := strings.TrimSpace(q.Get("page")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			f.Page = p
		}
	}
	return f
}

func (f IssueQRCodeAdminFilters) link(overrides map[string]string) string {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("q", f.Search)
	if f.AssetID > 0 {
		set("asset", strconv.FormatInt(f.AssetID, 10))
	}
	if f.Status != "" && f.Status != "all" {
		set("status", f.Status)
	}
	if f.Page > 1 {
		set("page", strconv.Itoa(f.Page))
	}
	for k, v := range overrides {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return "/issue-qr-codes"
	}
	return "/issue-qr-codes?" + q.Encode()
}

func issueQRAdminTotalPages(total int) int {
	if total <= 0 {
		return 1
	}
	p := (total + issueQRAdminPageSize - 1) / issueQRAdminPageSize
	if p < 1 {
		return 1
	}
	return p
}

func (h *UIHandler) issueQRAdminListAll(ctx context.Context) ([]issueQRCode, error) {
	res, err := h.xoluClient.OQL(ctx, "SELECT * FROM "+issueQRCodesEntity+" ORDER BY id DESC")
	if err != nil {
		if entityNotYetCreated(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]issueQRCode, 0, len(res.Data))
	for _, row := range res.Data {
		out = append(out, issueQRCodeFromRow(row))
	}
	return out, nil
}

func (h *UIHandler) resolveAssetNames(ctx context.Context, ids []int64) map[int64]string {
	names := map[int64]string{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		if h.xoluClient == nil {
			names[id] = fmt.Sprintf("#%d", id)
			continue
		}
		asset, err := h.xoluClient.Get(ctx, "assets", id)
		if err != nil || asset == nil {
			names[id] = fmt.Sprintf("#%d", id)
			continue
		}
		name := toString(asset.Data["name"])
		if name == "" {
			name = fmt.Sprintf("#%d", id)
		}
		names[id] = name
	}
	return names
}

// IssueQRCodesList handles GET /issue-qr-codes.
func (h *UIHandler) IssueQRCodesList(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	f := parseIssueQRAdminFilters(r.URL.Query())

	codes, err := h.issueQRAdminListAll(ctx)
	if err != nil {
		h.logger.Error("failed to list issue qr codes", "error", err)
		h.page.RenderError(w, http.StatusInternalServerError, t("error.internal"), t("issue_qr_admin.error.load_failed"))
		return
	}

	assetIDs := make([]int64, 0, len(codes))
	for _, c := range codes {
		assetIDs = append(assetIDs, c.AssetID)
	}
	assetNames := h.resolveAssetNames(ctx, assetIDs)

	searchLower := strings.ToLower(f.Search)
	filtered := make([]issueQRCode, 0, len(codes))
	for _, c := range codes {
		if f.AssetID > 0 && c.AssetID != f.AssetID {
			continue
		}
		switch f.Status {
		case "active":
			if !c.Active {
				continue
			}
		case "revoked":
			if c.Active {
				continue
			}
		}
		if searchLower != "" {
			name := strings.ToLower(assetNames[c.AssetID])
			if !strings.Contains(name, searchLower) && !strings.Contains(strings.ToLower(c.Code), searchLower) {
				continue
			}
		}
		filtered = append(filtered, c)
	}

	total := len(filtered)
	totalPages := issueQRAdminTotalPages(total)
	if f.Page > totalPages {
		f.Page = totalPages
	}
	start := (f.Page - 1) * issueQRAdminPageSize
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := start + issueQRAdminPageSize
	if end > total {
		end = total
	}
	pageCodes := filtered[start:end]

	creatorIDs := make([]int64, 0, len(pageCodes))
	for _, c := range pageCodes {
		creatorIDs = append(creatorIDs, c.CreatedBy)
	}
	creatorNames := h.resolveUserNames(ctx, creatorIDs)

	rows := make([]issueQRAdminRow, 0, len(pageCodes))
	for _, c := range pageCodes {
		rows = append(rows, issueQRAdminRow{
			ID:          c.ID,
			Code:        c.Code,
			AssetID:     c.AssetID,
			AssetName:   assetNames[c.AssetID],
			Active:      c.Active,
			CreatedBy:   c.CreatedBy,
			CreatorName: creatorNames[c.CreatedBy],
			CreatedAt:   c.CreatedAt,
			RevokedAt:   c.RevokedAt,
		})
	}

	data := IssueQRCodesListData{
		Filters:   f,
		Rows:      rows,
		Total:     total,
		CSRFToken: h.GetCSRFToken(w, r),
		Notice:    r.URL.Query().Get("notice"),
		Error:     r.URL.Query().Get("err"),
	}
	h.page.Render(w, r, t("issue_qr_admin.title"), "/issues", IssueQRCodesAdminPage(data, t))
}

// IssueQRCodeAdminRevoke handles POST /issue-qr-codes/{id}/revoke.
func (h *UIHandler) IssueQRCodeAdminRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	back := "/issue-qr-codes"
	if ref := r.Referer(); ref != "" {
		if u, perr := url.Parse(ref); perr == nil && strings.HasPrefix(u.Path, "/issue-qr-codes") {
			back = u.RequestURI()
		}
	}
	if err != nil || id <= 0 {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	ent, err := h.xoluClient.Get(ctx, issueQRCodesEntity, id)
	if err != nil || ent == nil {
		http.Redirect(w, r, appendQuery(back, "err", "not_found"), http.StatusSeeOther)
		return
	}
	code := issueQRCodeFromRow(ent.Data)
	if !code.Active {
		http.Redirect(w, r, appendQuery(back, "notice", "already_revoked"), http.StatusSeeOther)
		return
	}
	if err := h.revokeIssueQRCode(ctx, code); err != nil {
		h.logger.Error("failed to revoke qr code", "id", id, "error", err)
		http.Redirect(w, r, appendQuery(back, "err", "revoke_failed"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, appendQuery(back, "notice", "revoked"), http.StatusSeeOther)
}

// IssueQRCodesBulkRevoke handles POST /issue-qr-codes/revoke.
func (h *UIHandler) IssueQRCodesBulkRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/issue-qr-codes?err=revoke_failed", http.StatusSeeOther)
		return
	}
	back := "/issue-qr-codes"
	if v := strings.TrimSpace(r.FormValue("return_to")); v != "" && strings.HasPrefix(v, "/issue-qr-codes") {
		back = v
	} else if ref := r.Referer(); ref != "" {
		if u, perr := url.Parse(ref); perr == nil && strings.HasPrefix(u.Path, "/issue-qr-codes") {
			back = u.RequestURI()
		}
	}
	ids := r.Form["ids"]
	if len(ids) == 0 {
		http.Redirect(w, r, appendQuery(back, "notice", "none_selected"), http.StatusSeeOther)
		return
	}
	revoked := 0
	for _, s := range ids {
		id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		ent, err := h.xoluClient.Get(ctx, issueQRCodesEntity, id)
		if err != nil || ent == nil {
			continue
		}
		code := issueQRCodeFromRow(ent.Data)
		if !code.Active {
			continue
		}
		if err := h.revokeIssueQRCode(ctx, code); err != nil {
			h.logger.Error("failed to bulk revoke qr code", "id", id, "error", err)
			continue
		}
		revoked++
	}
	http.Redirect(w, r, appendQuery(back, "notice", "bulk_revoked", "count", strconv.Itoa(revoked)), http.StatusSeeOther)
}

func appendQuery(base, key, value string, extra ...string) string {
	u, err := url.Parse(base)
	if err != nil {
		if strings.Contains(base, "?") {
			return base + "&" + key + "=" + url.QueryEscape(value)
		}
		return base + "?" + key + "=" + url.QueryEscape(value)
	}
	q := u.Query()
	q.Set(key, value)
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ── Page ─────────────────────────────────────────────────────────────────────

func IssueQRCodesAdminPage(data IssueQRCodesListData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	return func(b *mi.Builder) mi.Node {
		totalPages := issueQRAdminTotalPages(data.Total)
		return b.Div(mi.Class("space-y-6"),
			b.Div(mi.Class("flex items-center justify-between"),
				b.Div(mi.Class("flex items-center gap-3"),
					b.A(mi.Href("/issue-templates"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
						b.Span(mi.Class("material-icons"), "arrow_back"),
					),
					b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issue_qr_admin.title")),
				),
			),
			issueQRAdminNotice(data, t)(b),
			issueQRAdminFiltersBar(data.Filters, t)(b),
			issueQRAdminTable(data, t)(b),
			issueQRAdminPagination(data.Filters, totalPages, data.Total, t)(b),
		)
	}
}

func issueQRAdminNotice(data IssueQRCodesListData, t func(string, ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		if data.Error != "" {
			msg := t("issue_qr_admin.error.revoke_failed")
			if data.Error == "not_found" {
				msg = t("issue_qr_admin.error.not_found")
			} else if data.Error == "load_failed" {
				msg = t("issue_qr_admin.error.load_failed")
			}
			return Alert(msg, "error", true)(b)
		}
		switch data.Notice {
		case "revoked":
			return Alert(t("issue_qr_admin.notice.revoked"), "success", true)(b)
		case "already_revoked":
			return Alert(t("issue_qr_admin.notice.already_revoked"), "info", true)(b)
		case "none_selected":
			return Alert(t("issue_qr_admin.notice.none_selected"), "info", true)(b)
		case "bulk_revoked":
			return Alert(t("issue_qr_admin.notice.bulk_revoked"), "success", true)(b)
		}
		return b.Span()
	}
}

func issueQRAdminFiltersBar(f IssueQRCodeAdminFilters, t func(string, ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		return b.Form(mi.Method("get"), mi.Action("/issue-qr-codes"),
			mi.Class("bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg p-4"),
			b.Div(mi.Class("flex flex-wrap items-end gap-3"),
				b.Div(mi.Class("flex-1 min-w-[200px]"),
					b.Label(mi.Class("block text-xs font-medium text-gray-600 dark:text-gray-300 mb-1"), t("issue_qr_admin.filter.search")),
					b.Input(mi.Type("text"), mi.Name("q"), mi.Value(f.Search),
						mi.Attr("placeholder", t("issue_qr_admin.filter.search_placeholder")),
						mi.Class("w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm"),
					),
				),
				b.Div(
					b.Label(mi.Class("block text-xs font-medium text-gray-600 dark:text-gray-300 mb-1"), t("issue_qr_admin.filter.asset")),
					b.Input(mi.Type("text"), mi.Name("asset"), mi.Value(func() string {
						if f.AssetID > 0 {
							return strconv.FormatInt(f.AssetID, 10)
						}
						return ""
					}()),
						mi.Attr("placeholder", t("issue_qr_admin.filter.asset_placeholder")),
						mi.Class("w-32 px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm"),
					),
				),
				b.Div(
					b.Label(mi.Class("block text-xs font-medium text-gray-600 dark:text-gray-300 mb-1"), t("issue_qr_admin.filter.status")),
					b.Select(mi.Name("status"),
						mi.Class("px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 text-sm"),
						func() mi.Node {
							if f.Status == "all" {
								return b.Option(mi.Value("all"), mi.Selected(), t("issue_qr_admin.filter.all"))
							}
							return b.Option(mi.Value("all"), t("issue_qr_admin.filter.all"))
						}(),
						func() mi.Node {
							if f.Status == "active" {
								return b.Option(mi.Value("active"), mi.Selected(), t("issue_qr_admin.status.active"))
							}
							return b.Option(mi.Value("active"), t("issue_qr_admin.status.active"))
						}(),
						func() mi.Node {
							if f.Status == "revoked" {
								return b.Option(mi.Value("revoked"), mi.Selected(), t("issue_qr_admin.status.revoked"))
							}
							return b.Option(mi.Value("revoked"), t("issue_qr_admin.status.revoked"))
						}(),
					),
				),
				b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 text-sm font-medium"), t("issue_qr_admin.filter.apply")),
				b.A(mi.Href("/issue-qr-codes"), mi.Class("px-3 py-2 text-sm text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200"), t("issue_qr_admin.filter.clear")),
			),
		)
	}
}

func issueQRAdminTable(data IssueQRCodesListData, t func(string, ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		if len(data.Rows) == 0 {
			return Card("", func(b *mi.Builder) mi.Node {
				return b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400 py-4"), t("issue_qr_admin.empty"))
			})(b)
		}
		returnTo := data.Filters.link(nil)
		rows := make([]interface{}, 0, len(data.Rows))
		for _, row := range data.Rows {
			rows = append(rows, issueQRAdminTableRow(row, data.CSRFToken, returnTo, t)(b))
		}
		return b.Form(mi.Method("post"), mi.Action("/issue-qr-codes/revoke"), mi.Class("space-y-3"),
			b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
			b.Input(mi.Type("hidden"), mi.Name("return_to"), mi.Value(returnTo)),
			b.Div(mi.Class("flex items-center justify-between"),
				b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("issue_qr_admin.total", data.Total)),
				b.Button(mi.Type("submit"),
					mi.Class("px-3 py-1.5 text-sm bg-red-600 text-white rounded-lg hover:bg-red-700 font-medium"),
					mi.Attr("onclick", "return confirm('"+jsEscape(t("issue_qr_admin.confirm_bulk"))+"')"),
					t("issue_qr_admin.revoke_selected"),
				),
			),
			b.Div(mi.Class("overflow-x-auto bg-white dark:bg-gray-800 border border-gray-200 dark:border-gray-700 rounded-lg"),
				b.Table(mi.Class("min-w-full divide-y divide-gray-200 dark:divide-gray-700 text-sm"),
					b.Thead(mi.Class("bg-gray-50 dark:bg-gray-900/40"),
						b.Tr(
							b.Th(mi.Class("px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase"), t("issue_qr_admin.col.select")),
							b.Th(mi.Class("px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase"), t("issue_qr_admin.col.asset")),
							b.Th(mi.Class("px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase"), t("issue_qr_admin.col.code")),
							b.Th(mi.Class("px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase"), t("issue_qr_admin.col.status")),
							b.Th(mi.Class("px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase"), t("issue_qr_admin.col.created_by")),
							b.Th(mi.Class("px-4 py-3 text-left text-xs font-medium text-gray-500 uppercase"), t("issue_qr_admin.col.created_at")),
							b.Th(mi.Class("px-4 py-3 text-right text-xs font-medium text-gray-500 uppercase"), t("issue_qr_admin.col.actions")),
						),
					),
					b.Tbody(append([]interface{}{mi.Class("divide-y divide-gray-200 dark:divide-gray-700")}, rows...)...),
				),
			),
		)
	}
}

func issueQRAdminTableRow(row issueQRAdminRow, csrfToken, returnTo string, t func(string, ...any) string) mi.H {
	_ = returnTo
	return func(b *mi.Builder) mi.Node {
		status := Badge(t("issue_qr_admin.status.revoked"), "error")
		if row.Active {
			status = Badge(t("issue_qr_admin.status.active"), "success")
		}
		creator := row.CreatorName
		if creator == "" {
			if row.CreatedBy > 0 {
				creator = fmt.Sprintf("#%d", row.CreatedBy)
			} else {
				creator = "-"
			}
		}
		selectCell := b.Td(mi.Class("px-4 py-3"),
			func() mi.Node {
				if !row.Active {
					return b.Span(mi.Class("text-gray-300"), "-")
				}
				return b.Input(mi.Type("checkbox"), mi.Name("ids"), mi.Value(strconv.FormatInt(row.ID, 10)),
					mi.Class("rounded border-gray-300 text-indigo-600 focus:ring-indigo-500"))
			}(),
		)
		actionCell := b.Td(mi.Class("px-4 py-3 text-right"),
			func() mi.Node {
				if !row.Active {
					return b.Span(mi.Class("text-xs text-gray-400"), "-")
				}
				return b.Form(mi.Method("post"), mi.Action("/issue-qr-codes/"+strconv.FormatInt(row.ID, 10)+"/revoke"), mi.Class("inline"),
					b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(csrfToken)),
					b.Button(mi.Type("submit"),
						mi.Class("px-2.5 py-1 text-xs font-medium text-red-700 bg-red-50 border border-red-200 rounded hover:bg-red-100 dark:bg-red-900/20 dark:text-red-300 dark:border-red-800"),
						mi.Attr("onclick", "return confirm('"+jsEscape(t("issue_qr_admin.confirm_revoke"))+"')"),
						t("issue_qr_admin.revoke"),
					),
				)
			}(),
		)
		assetName := row.AssetName
		if assetName == "" {
			assetName = fmt.Sprintf("#%d", row.AssetID)
		}
		createdAt := row.CreatedAt
		if len(createdAt) >= 10 {
			createdAt = createdAt[:10]
		} else if createdAt == "" {
			createdAt = "-"
		}
		return b.Tr(mi.Class("hover:bg-gray-50 dark:hover:bg-gray-900/30"),
			selectCell,
			b.Td(mi.Class("px-4 py-3"),
				b.A(mi.Href("/assets/"+strconv.FormatInt(row.AssetID, 10)), mi.Class("text-indigo-600 hover:underline dark:text-indigo-400"), assetName),
			),
			b.Td(mi.Class("px-4 py-3 font-mono text-xs text-gray-800 dark:text-gray-200"), row.Code),
			b.Td(mi.Class("px-4 py-3"), status(b)),
			b.Td(mi.Class("px-4 py-3 text-gray-700 dark:text-gray-300"), creator),
			b.Td(mi.Class("px-4 py-3 text-gray-600 dark:text-gray-400"), createdAt),
			actionCell,
		)
	}
}

func issueQRAdminPagination(f IssueQRCodeAdminFilters, totalPages, total int, t func(string, ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		if totalPages <= 1 {
			return b.Div()
		}
		prev := b.Span(mi.Class("px-3 py-1.5 text-sm text-gray-400"), "‹")
		if f.Page > 1 {
			prev = b.A(mi.Href(f.link(map[string]string{"page": strconv.Itoa(f.Page - 1)})),
				mi.Class("px-3 py-1.5 text-sm rounded border border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700"), "‹")
		}
		next := b.Span(mi.Class("px-3 py-1.5 text-sm text-gray-400"), "›")
		if f.Page < totalPages {
			next = b.A(mi.Href(f.link(map[string]string{"page": strconv.Itoa(f.Page + 1)})),
				mi.Class("px-3 py-1.5 text-sm rounded border border-gray-300 dark:border-gray-600 hover:bg-gray-50 dark:hover:bg-gray-700"), "›")
		}
		start := (f.Page - 1) * issueQRAdminPageSize
		if start < 0 {
			start = 0
		}
		showing := 0
		if total > 0 && start < total {
			showing = issueQRAdminPageSize
			if start+showing > total {
				showing = total - start
			}
		}
		return b.Div(mi.Class("flex items-center justify-between"),
			b.Div(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("pagination.showing", showing, total, t("issue_qr_admin.title"))),
			b.Div(mi.Class("flex items-center gap-2"), prev, b.Span(mi.Class("text-sm text-gray-600 dark:text-gray-300"), strconv.Itoa(f.Page)+"/"+strconv.Itoa(totalPages)), next),
		)
	}
}

func jsEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
