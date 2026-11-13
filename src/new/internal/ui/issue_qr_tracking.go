package ui

// Public tracking of an issue reported through a QR code.
//
// The anonymous report must not hand out the issue id: that would let anyone
// enumerate the table by changing a number. Instead the report stores two
// extra fields on the issue:
//
//	track_token      26 base32 chars (128 bits), the only secret of the link
//	track_expires_at RFC3339 deadline, written the first time the issue
//	                 reaches a closing state and never rewritten afterwards
//
// GET /track/{token} is the only way back into the record for a reporter
// without an account. It shows the state, a five-step progress bar and the
// machine history, and nothing else: no contact block, no reporter, no actor
// names, no internal reason.
//
// "Closing state" here means resolved, closed, rejected or duplicate. Resolved
// is not terminal in the FSM, but the report is already solved from the
// reporter's point of view, so the two-day window starts there. If the team
// later decides that only terminal states should start the clock, remove
// "resolved" from issueTracksFinalState and nothing else changes.
//
// The link stops working two days after that moment. The issue row itself is
// not deleted: expiry is an access rule, not a cleanup job.

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

const (
	// issueTrackTokenBytes is the amount of randomness behind a tracking link.
	// 16 bytes encode to 26 base32 chars (128 bits), so guessing a live token
	// is not a realistic attack.
	issueTrackTokenBytes = 16
	// issueTrackTokenLength is the exact length of the base32 encoding without
	// padding: ceil(16*8/5) = 26.
	issueTrackTokenLength = (issueTrackTokenBytes*8 + 4) / 5
	// issueTrackExpiryAfter is how long a tracking link stays valid after the
	// issue first reaches a closing state.
	issueTrackExpiryAfter = 48 * time.Hour
	// issueTrackLinkPrefix is the public path of the tracking page.
	issueTrackLinkPrefix = "/track/"
	// issueTrackTimeLayout is the date format used on the public page and in
	// the detail card. It keeps the day/month order the rest of the app uses.
	issueTrackTimeLayout = "02/01/2006 15:04"
)

// issueTrackSteps is the progress bar shown to the reporter. It deliberately
// omits on_hold, rejected and duplicate: on_hold maps to the in_progress step,
// and the terminal exceptions hide the bar and rely on the state badge.
var issueTrackSteps = []string{
	"reported",
	"triaged",
	"in_progress",
	"resolved",
	"closed",
}

// newIssueTrackingToken returns a fresh opaque token.
func newIssueTrackingToken() (string, error) {
	raw := make([]byte, issueTrackTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// validIssueTrackingToken enforces the exact shape before touching the
// database. It is not a security check by itself; it keeps malformed paths out
// of OQL and gives them the same 404 as an unknown token.
func validIssueTrackingToken(token string) bool {
	if len(token) != issueTrackTokenLength {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if (c >= 'A' && c <= 'Z') || (c >= '2' && c <= '7') {
			continue
		}
		return false
	}
	return true
}

// issueTracksFinalState reports whether the state starts the expiry clock.
func issueTracksFinalState(state string) bool {
	switch state {
	case "resolved", "closed", "rejected", "duplicate":
		return true
	default:
		return false
	}
}

// issueTrackExpiry returns the deadline to store when an issue enters state at
// time now, or "" when the state does not start the clock.
func issueTrackExpiry(state string, now time.Time) string {
	if !issueTracksFinalState(state) {
		return ""
	}
	return now.UTC().Add(issueTrackExpiryAfter).Format(time.RFC3339)
}

// issueTrackExpired reports whether a stored deadline has passed. An empty or
// malformed value is treated as "not expired": a corrupted timestamp must not
// lock a reporter out of a report that was never closed.
func issueTrackExpired(expiresAt string, now time.Time) bool {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return now.After(deadline)
}

// issueTrackProgressIndex maps a state to the five-step bar. on_hold sits on
// the in_progress step because work is still owned by the team. rejected and
// duplicate return -1: the badge explains the terminal exception, and a
// progress bar would suggest a path that no longer exists.
func issueTrackProgressIndex(state string) int {
	switch state {
	case "reported":
		return 0
	case "triaged":
		return 1
	case "in_progress", "on_hold":
		return 2
	case "resolved":
		return 3
	case "closed":
		return 4
	case "rejected", "duplicate":
		return -1
	default:
		return 0
	}
}

// issueTrackingBase strips the "/s" public prefix used by the asset QR codes,
// so the same configured base produces both the report URL and the tracking
// URL. "https://host", "https://host/" and "https://host/s/" all become
// "https://host".
func issueTrackingBase(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/s")
	return strings.TrimRight(base, "/")
}

// issueTrackingPublicLink builds the link the reporter must save.
func issueTrackingPublicLink(base, token string) string {
	root := issueTrackingBase(base)
	if root == "" {
		return issueTrackLinkPrefix + token
	}
	return root + issueTrackLinkPrefix + token
}

// issueTrackingLink is the tracking link for this deployment.
func (h *UIHandler) issueTrackingLink(token string) string {
	return issueTrackingPublicLink(h.config.QRBaseURL, token)
}

// issueTrackActivityKnown filters the machine history down to the labels the
// public page has translations for. Unknown future inputs are simply not
// rendered instead of leaking a raw key.
func issueTrackActivityKnown(action string) bool {
	switch action {
	case "created", "triage", "start", "hold", "resume",
		"resolve", "close", "reject", "merge":
		return true
	default:
		return false
	}
}

// IssueTrackActivity is one step shown in the public Activity card.
type IssueTrackActivity struct {
	Action string
	At     string
}

// buildIssueTrackActivity converts xolu history entries into public entries.
// The initial entry has no input and is reported as "created".
func buildIssueTrackActivity(entries []xolu.HistoryEntry) []IssueTrackActivity {
	out := make([]IssueTrackActivity, 0, len(entries))
	for _, e := range entries {
		action := "created"
		if e.Input != nil {
			action = *e.Input
		}
		if !issueTrackActivityKnown(action) {
			continue
		}
		out = append(out, IssueTrackActivity{Action: action, At: e.At})
	}
	return out
}

// issueTrackCanonicalID returns the canonical issue id stored by a merge walk,
// if the history has one. It scans backwards because the last merge is the one
// that matters, and it ignores payloads that do not carry canonical_id.
func issueTrackCanonicalID(entries []xolu.HistoryEntry) int64 {
	for i := len(entries) - 1; i >= 0; i-- {
		if len(entries[i].Payload) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(entries[i].Payload, &payload); err != nil {
			continue
		}
		if id := toInt64(payload["canonical_id"]); id > 0 {
			return id
		}
	}
	return 0
}

// issueTrackDuplicateLink returns the tracking link of the canonical issue, or
// "" when that issue has no token or its link has already expired.
func (h *UIHandler) issueTrackDuplicateLink(ctx context.Context, canonicalID int64) string {
	if canonicalID <= 0 {
		return ""
	}
	entity, err := h.xoluClient.Get(ctx, "issues", canonicalID)
	if err != nil {
		return ""
	}
	canonical := issueFromEntity(entity)
	if canonical.TrackToken == "" {
		return ""
	}
	if issueTrackExpired(canonical.TrackExpiresAt, time.Now().UTC()) {
		return ""
	}
	return h.issueTrackingLink(canonical.TrackToken)
}

// formatIssueTrackTime renders an RFC3339 timestamp for the public page. It
// uses the browser-local zone, unlike the operator views, because the reporter
// reads this page on their own phone. Non-RFC3339 values are returned as-is.
func formatIssueTrackTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format(issueTrackTimeLayout)
}

// IssueTrackData is everything the public tracking page renders.
type IssueTrackData struct {
	Issue        Issue
	AssetName    string
	IsNew        bool
	TrackingURL  string
	Activity     []IssueTrackActivity
	DuplicateURL string
}

// IssueTrack handles GET /track/{token}. The token is the only identity the
// page accepts: no session, no issue id, no code.
func (h *UIHandler) IssueTrack(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	token := strings.TrimSpace(r.PathValue("token"))

	if !validIssueTrackingToken(token) {
		h.page.RenderPublicError(w, http.StatusNotFound,
			t("qr.track.error.not_found_title"), t("qr.track.error.not_found_message"))
		return
	}

	res, err := h.xoluClient.OQL(ctx,
		"SELECT TOP 1 * FROM issues WHERE track_token = '"+escapeOQL(token)+"'")
	if err != nil {
		if entityNotYetCreated(err) {
			h.page.RenderPublicError(w, http.StatusNotFound,
				t("qr.track.error.not_found_title"), t("qr.track.error.not_found_message"))
			return
		}
		h.logger.Error("issue tracking lookup failed", "error", err)
		h.page.RenderPublicError(w, http.StatusInternalServerError,
			t("error.server_error"), t("error.server_error"))
		return
	}
	if len(res.Data) == 0 {
		h.page.RenderPublicError(w, http.StatusNotFound,
			t("qr.track.error.not_found_title"), t("qr.track.error.not_found_message"))
		return
	}

	row := res.Data[0]
	issue := issueFromEntity(&xolu.Entity{
		ID:   toInt64(row["id"]),
		Data: row,
	})

	now := time.Now().UTC()
	if issueTrackExpired(issue.TrackExpiresAt, now) {
		h.page.RenderPublicError(w, http.StatusGone,
			t("qr.track.error.expired_title"), t("qr.track.error.expired_message"))
		return
	}

	data := IssueTrackData{
		Issue:       issue,
		IsNew:       r.URL.Query().Get("new") == "1",
		TrackingURL: h.issueTrackingLink(issue.TrackToken),
	}

	if issue.AssetID > 0 {
		if asset, aerr := h.xoluClient.Get(ctx, "assets", issue.AssetID); aerr == nil {
			data.AssetName = toString(asset.Data["name"])
		}
	}

	var entries []xolu.HistoryEntry
	if issue.MachineID > 0 {
		hist, herr := h.xoluClient.GetMachineHistory(ctx, issue.MachineID)
		if herr != nil {
			h.logger.Error("failed to load the public history of an issue", "id", issue.ID, "error", herr)
		} else {
			entries = hist
			data.Activity = buildIssueTrackActivity(hist)
		}
	}

	if issue.State == "duplicate" {
		data.DuplicateURL = h.issueTrackDuplicateLink(ctx, issueTrackCanonicalID(entries))
	}

	h.page.RenderPublic(w, t("qr.track.title"), issueTrackPage(data, t))
}

// issueTrackStepper renders the five-step progress bar.
func issueTrackStepper(current int, t func(key string, args ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		items := make([]interface{}, 0, len(issueTrackSteps))
		for i, step := range issueTrackSteps {
			circleClass := "w-8 h-8 rounded-full flex items-center justify-center bg-gray-200 dark:bg-gray-700 text-gray-400"
			labelClass := "text-xs text-center text-gray-500 dark:text-gray-400"
			icon := ""
			switch {
			case i < current:
				circleClass = "w-8 h-8 rounded-full flex items-center justify-center bg-indigo-600 text-white"
				labelClass = "text-xs text-center text-gray-900 dark:text-white"
				icon = "check"
			case i == current:
				circleClass = "w-8 h-8 rounded-full flex items-center justify-center bg-indigo-600 text-white ring-4 ring-indigo-200 dark:ring-indigo-900/50"
				labelClass = "text-xs text-center text-indigo-600 dark:text-indigo-400 font-semibold"
				icon = "build"
			}

			circle := []interface{}{mi.Class(circleClass)}
			if icon != "" {
				circle = append(circle, b.Span(mi.Class("material-icons text-base"), icon))
			}

			items = append(items, b.Div(
				mi.Class("flex flex-col items-center gap-2 flex-1"),
				b.Div(circle...),
				b.Span(mi.Class(labelClass), t("qr.track.step."+step)),
			))
		}
		return b.Div(append([]interface{}{mi.Class("flex items-start")}, items...)...)
	}
}

// issueTrackPage renders the public page of mockup 06.
func issueTrackPage(data IssueTrackData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	iss := data.Issue

	return func(b *mi.Builder) mi.Node {
		children := []interface{}{}

		if data.IsNew {
			children = append(children, b.Div(
				mi.Class("px-4 py-3 rounded-lg bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 flex items-start gap-3"),
				b.Span(mi.Class("material-icons text-green-600 dark:text-green-400"), "check_circle"),
				b.Div(mi.Class("min-w-0"),
					b.Div(mi.Class("text-sm font-semibold text-green-800 dark:text-green-300"),
						t("qr.track.new_title")),
					b.Div(mi.Class("text-sm text-green-700 dark:text-green-400"),
						t("qr.track.issue_number")+": #"+strconv.FormatInt(iss.ID, 10)),
					b.P(mi.Class("mt-1 text-xs text-green-700 dark:text-green-400"),
						t("qr.track.new_hint")),
				),
			))
		}

		metaParts := []string{}
		if data.AssetName != "" {
			metaParts = append(metaParts, data.AssetName)
		}
		if at := formatIssueTrackTime(iss.CreatedAt); at != "" {
			metaParts = append(metaParts, t("qr.track.reported_at")+" "+at)
		}
		meta := strings.Join(metaParts, " · ")

		details := []interface{}{
			b.Div(mi.Class("flex items-start justify-between gap-3"),
				b.Div(mi.Class("min-w-0"),
					b.Div(mi.Class("font-medium text-gray-900 dark:text-white"), iss.Title),
					b.Div(mi.Class("text-sm text-gray-500 dark:text-gray-400"), meta),
				),
				issueStatusBadge(iss.State, t)(b),
			),
		}

		if idx := issueTrackProgressIndex(iss.State); idx >= 0 {
			details = append(details, b.Div(mi.Class("mt-6"), issueTrackStepper(idx, t)(b)))
		}

		if iss.State == "duplicate" {
			dup := []interface{}{
				mi.Class("mt-4 rounded-lg border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-900/20 p-3"),
				b.P(mi.Class("text-sm text-amber-800 dark:text-amber-300"),
					t("qr.track.duplicate_note")),
			}
			if data.DuplicateURL != "" {
				dup = append(dup, b.A(
					mi.Href(data.DuplicateURL),
					mi.Class("mt-2 inline-block text-sm font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"),
					t("qr.track.duplicate_open"),
				))
			}
			details = append(details, b.Div(dup...))
		}

		children = append(children, Card(t("qr.track.card_title"), func(b *mi.Builder) mi.Node {
			return b.Div(details...)
		})(b))

		activityCard := Card(t("qr.track.activity.title"), func(b *mi.Builder) mi.Node {
			if len(data.Activity) == 0 {
				return b.P(mi.Class("text-sm text-gray-400 italic"), t("qr.track.activity.empty"))
			}
			rows := make([]interface{}, 0, len(data.Activity))
			for _, a := range data.Activity {
				rows = append(rows, b.Div(
					mi.Class("flex items-center justify-between gap-3 py-2 border-b border-gray-100 dark:border-gray-700 last:border-b-0"),
					b.Span(mi.Class("text-sm font-medium text-gray-900 dark:text-white"),
						t("qr.track.activity."+a.Action)),
					b.Span(mi.Class("text-xs text-gray-500 dark:text-gray-400 whitespace-nowrap"),
						formatIssueTrackTime(a.At)),
				))
			}
			return b.Div(rows...)
		})(b)
		children = append(children, activityCard)

		linkCard := Card(t("qr.track.link.title"), func(b *mi.Builder) mi.Node {
			return b.Div(
				b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("qr.track.link.hint")),
				b.Div(mi.Class("mt-3 flex items-center gap-2"),
					b.Input(
						mi.ID("issue-track-link"),
						mi.Type("text"),
						mi.Value(data.TrackingURL),
						mi.Attr("readonly", "readonly"),
						mi.Class("flex-1 min-w-0 px-3 py-2 rounded-lg border border-gray-300 dark:border-gray-600 bg-gray-50 dark:bg-gray-700 text-sm text-gray-700 dark:text-gray-300"),
					),
					Button(t("qr.track.link.copy"), "secondary",
						mi.Type("button"),
						mi.ID("issue-track-copy"),
					)(b),
				),
				b.P(mi.Class("mt-3 text-xs text-amber-700 dark:text-amber-400"),
					t("qr.track.link.warning")),
				b.Script(mi.Raw(issueTrackCopyScript(t("qr.track.link.copied")))),
			)
		})(b)
		children = append(children, linkCard)

		return b.Div(append([]interface{}{mi.Class("space-y-4")}, children...)...)
	}
}

// issueTrackCopyScript copies the value of the tracking input. PublicLayout
// does not load app.js, so Seam.Utils.copyToClipboard does not exist here: the
// script is self-contained, with a fallback for browsers without the async
// clipboard API.
func issueTrackCopyScript(copiedLabel string) string {
	return "(function(){" +
		"var i=document.getElementById('issue-track-link');" +
		"var b=document.getElementById('issue-track-copy');" +
		"if(!i||!b){return}" +
		"var l=i.value;" +
		"if(l.indexOf('/')===0){l=window.location.origin+l}" +
		"var done=function(){var o=b.textContent;b.textContent=" + jsQuote(copiedLabel) +
		";window.setTimeout(function(){b.textContent=o},1500)};" +
		"var fallback=function(){i.focus();i.select();try{document.execCommand('copy');done()}catch(e){}};" +
		"b.addEventListener('click',function(){" +
		"if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(l).then(done,fallback)}else{fallback()}" +
		"})" +
		"})()"
}
