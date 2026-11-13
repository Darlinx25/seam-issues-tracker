package ui

// The anonymous reporting channel of the per-asset QR codes.
//
// GET /s/{code} renders the form (issue_qr_landing.go); POST /s/{code}/report
// creates the issue and redirects to GET /track/{token}?new=1, the public
// tracking page. The token is generated here and stored on the issue; it is
// the only handle the anonymous reporter gets, so the redirect never carries
// the issue id.
//
// The code is the only identity the scanner presents: the asset, its category
// and the template assigned to its type all come from the stored row, never
// from the request. A tampered form cannot report against another asset.
//
// Two things differ from the in-app form on purpose (ROADMAP, "Formulario
// anónimo de incidencia desde QR"):
//
//	channel      forced to "qr"
//	reported_by  never written, and nothing is auto-assigned to a creator
//
// Priority and severity keep the in-app defaults (medium / minor): the
// anonymous reporter does not triage, and the triage step adjusts them later.

import (
	"context"
	"net/http"
	"strings"
	"time"

	xolu "github.com/ha1tch/xolu/pkg/client"
)

// Field names of the anonymous form. The contact block is stored in form_data:
// it is the only way to reach a reporter who has no account.
const (
	qrReportContactName     = "contact_name"
	qrReportContactDocument = "contact_document"
	qrReportContactPhone    = "contact_phone"
	qrReportContactEmail    = "contact_email"

	// qrReportDefaultPriority / qrReportDefaultSeverity mirror the defaults the
	// in-app form applies while the pickers are untouched.
	qrReportDefaultPriority = "medium"
	qrReportDefaultSeverity = "minor"
)

// issueQRReportCode is the code segment of /s/{code}/report. extractPathSegment
// stops at the next slash, so /s/ABC/report yields the same "ABC" the landing
// was rendered for.
func issueQRReportCode(r *http.Request) string {
	return extractPathSegment(r.URL.Path, "/s/")
}

// validIssueQRReportEmail rejects the addresses a report could never be
// answered at. It is deliberately small: the form only has to catch typos, and
// a stricter parser would reject addresses the reporter really owns.
func validIssueQRReportEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	if at <= 0 || at != strings.LastIndexByte(email, '@') {
		return false
	}
	domain := email[at+1:]
	return strings.Contains(domain, ".") &&
		!strings.HasPrefix(domain, ".") &&
		!strings.HasSuffix(domain, ".") &&
		!strings.ContainsAny(email, " \t\r\n")
}

// issueQRCodeStoredCategory returns the category frozen into a per-asset code
// when it was printed. It reads only the code row: resolveScanCode answers the
// asset (and whether the code is live), not the category.
//
// Legacy qr_codes rows and unknown codes return "": the caller falls back to
// the asset's current criticality, the same way the landing does.
func (h *UIHandler) issueQRCodeStoredCategory(ctx context.Context, code string) string {
	if !validIssueQRCode(code) {
		return ""
	}
	res, err := h.xoluClient.OQL(ctx,
		"SELECT TOP 1 asset_category FROM "+issueQRCodesEntity+
			" WHERE code = '"+escapeOQL(code)+"'")
	if err != nil || res == nil || len(res.Data) == 0 {
		return ""
	}
	return toString(res.Data[0]["asset_category"])
}

// issueQRCodeCategory is the canonical category of a code: the value frozen
// into the row when it is one of the two canonical tokens, otherwise the
// asset's current criticality. The landing and the report share it, so the
// badge a scanner sees and the category the issue stores are always the same.
func (h *UIHandler) issueQRCodeCategory(ctx context.Context, code string, asset *xolu.Entity) string {
	if c := h.issueQRCodeStoredCategory(ctx, code); c == "critico" || c == "normal" {
		return c
	}
	if asset == nil {
		return "normal"
	}
	return assetCategoryFromStoredCriticality(toString(asset.Data["criticality"]))
}

// RenderIssueQRReportLanding renders the anonymous report form for a code that
// already resolved to an asset. It lives on the handler because the template
// assigned to the asset's type has to be resolved and rendered on the server:
// PublicLayout does not load HTMX, so there is no partial to swap it in later.
func (h *UIHandler) RenderIssueQRReportLanding(w http.ResponseWriter, r *http.Request, data QRScanData, assetCategory string) {
	t := h.translateFunc(r)
	ctx := r.Context()

	pageData := issueQRReportPageData{
		Code:          data.Code,
		AssetName:     data.AssetName,
		AssetType:     data.AssetType,
		AssetLocation: data.Location,
		AssetCategory: assetCategory,
	}
	if tpl := h.resolveIssueTemplate(ctx, data.AssetID); tpl != nil && len(tpl.Fields) > 0 {
		pageData.TemplateFields = h.renderTemplateFields(ctx, tpl,
			map[string]any{}, map[string]string{}, "")
	}
	_ = h.page.RenderPublic(w, t("qr.report.title"), issueQRReportPage(pageData, t))
}

// IssueQRReport handles POST /s/{code}/report -- the anonymous report itself.
func (h *UIHandler) IssueQRReport(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()

	code := issueQRReportCode(r)
	if code == "" {
		h.page.RenderPublicError(w, http.StatusNotFound, t("qr.invalid_title"), t("qr.invalid_message"))
		return
	}

	// The code is the only identity of the report. A revoked code answers with
	// the same explanation the landing gives, so a scanner who refreshed the
	// page after the code was deactivated understands what happened.
	assetID, found, revoked := h.resolveScanCode(ctx, code)
	if revoked {
		h.page.RenderPublicError(w, http.StatusGone, t("qr.revoked_title"), t("qr.revoked_message"))
		return
	}
	if !found {
		h.page.RenderPublicError(w, http.StatusNotFound, t("qr.not_found_title"), t("qr.not_found_message"))
		return
	}
	if assetID == 0 {
		h.page.RenderPublicError(w, http.StatusNotFound, t("qr.asset_not_found_title"), t("qr.asset_not_found_message"))
		return
	}
	asset, err := h.xoluClient.Get(ctx, "assets", assetID)
	if err != nil {
		h.page.RenderPublicError(w, http.StatusNotFound, t("qr.asset_not_found_title"), t("qr.asset_not_found_message"))
		return
	}

	if err := r.ParseForm(); err != nil {
		h.page.RenderPublicError(w, http.StatusBadRequest, t("error.bad_request"), t("error.invalid_form_data"))
		return
	}

	values := issueQRReportPageData{
		Code:            code,
		AssetName:       toString(asset.Data["name"]),
		AssetType:       toString(asset.Data["type_name"]),
		AssetLocation:   toString(asset.Data["location"]),
		AssetCategory:   h.issueQRCodeCategory(ctx, code, asset),
		ContactName:     strings.TrimSpace(r.FormValue(qrReportContactName)),
		ContactDocument: strings.TrimSpace(r.FormValue(qrReportContactDocument)),
		ContactPhone:    strings.TrimSpace(r.FormValue(qrReportContactPhone)),
		ContactEmail:    strings.TrimSpace(r.FormValue(qrReportContactEmail)),
		Title:           strings.TrimSpace(r.FormValue("title")),
		Description:     strings.TrimSpace(r.FormValue("description")),
		Errors:          map[string]string{},
	}

	// The template is resolved from the asset, exactly as the in-app form does
	// when the asset picker changes: the request never names the template.
	tpl := h.resolveIssueTemplate(ctx, assetID)
	formData := map[string]any{}
	if tpl != nil {
		for _, f := range tpl.Fields {
			v := strings.TrimSpace(r.FormValue(f.Key))
			formData[f.Key] = v
			if f.Required && v == "" {
				values.Errors[f.Key] = t("field.required")
			}
		}
	}

	if values.ContactName == "" {
		values.Errors[qrReportContactName] = t("qr.report.error.contact_required")
	}
	if values.ContactDocument == "" {
		values.Errors[qrReportContactDocument] = t("qr.report.error.contact_required")
	}
	if values.ContactPhone == "" {
		values.Errors[qrReportContactPhone] = t("qr.report.error.contact_required")
	}
	switch {
	case values.ContactEmail == "":
		values.Errors[qrReportContactEmail] = t("qr.report.error.contact_required")
	case !validIssueQRReportEmail(values.ContactEmail):
		values.Errors[qrReportContactEmail] = t("error.invalid_email")
	}
	if values.Title == "" {
		values.Errors["title"] = t("issues.error.title_required")
	}
	if values.Description == "" {
		values.Errors["description"] = t("qr.report.error.description_required")
	}

	if len(values.Errors) > 0 {
		// The template fields are re-rendered with what was typed, so a rejected
		// report never makes the scanner lose their answers.
		if tpl != nil && len(tpl.Fields) > 0 {
			values.TemplateFields = h.renderTemplateFields(ctx, tpl, formData, values.Errors, "")
		}
		_ = h.page.RenderPublic(w, t("qr.report.title"), issueQRReportPage(values, t))
		return
	}

	// The token is generated before the machine: if the CSPRNG ever failed,
	// the report would fail before creating an orphan machine.
	trackToken, err := newIssueTrackingToken()
	if err != nil {
		h.logger.Error("failed to generate the tracking token", "error", err)
		h.page.RenderPublicError(w, http.StatusInternalServerError,
			t("error.server_error"), t("qr.report.error.create_failed"))
		return
	}

	// The definition is bootstrapped lazily, the same call the in-app create
	// makes, so a report from a cold install still gets its machine.
	defID, err := h.ensureIssueFSMDefinition(ctx)

	if err != nil {
		h.logger.Error("failed to bootstrap issue FSM definition", "error", err)
		h.page.RenderPublicError(w, http.StatusInternalServerError, t("error.server_error"), t("qr.report.error.create_failed"))
		return
	}
	machine, err := h.xoluClient.CreateMachine(ctx, xolu.CreateMachineRequest{Definition: defID})
	if err != nil {
		h.logger.Error("failed to create issue FSM machine", "error", err)
		h.page.RenderPublicError(w, http.StatusInternalServerError, t("error.server_error"), t("qr.report.error.create_failed"))
		return
	}

	now := time.Now().UTC()
	issueData := map[string]any{
		"title":          values.Title,
		"description":    values.Description,
		"priority":       qrReportDefaultPriority,
		"severity":       qrReportDefaultSeverity,
		"state":          machine.State,
		"machine_id":     machine.ID,
		"channel":        "qr",
		"asset_id":       assetID,
		"asset_category": values.AssetCategory,
		"created_at":     now.Format(time.RFC3339),
		"track_token":    trackToken,
	}

	// The SLA is the same pair the in-app create stores: the template assigned
	// to the asset wins, otherwise the fixed table for the frozen category.
	firstRespMin := slaMinutesFromTemplate(tpl, "first_response", qrReportDefaultPriority)
	if firstRespMin <= 0 {
		firstRespMin = defaultSLAFirstResponseMinutes(qrReportDefaultPriority, values.AssetCategory)
	}
	resMin := issueResolutionMinutes(tpl, qrReportDefaultPriority, values.AssetCategory)
	if d := calculateSLADeadlineFromMinutes(firstRespMin, now); d != "" {
		issueData["sla_first_response_deadline"] = d
	}
	if d := calculateSLADeadlineFromMinutes(resMin, now); d != "" {
		issueData["sla_resolution_deadline"] = d
	}

	// The contact block is part of the report, not of a users row: it is stored
	// in form_data next to the template's own answers.
	formData[qrReportContactName] = values.ContactName
	formData[qrReportContactDocument] = values.ContactDocument
	formData[qrReportContactPhone] = values.ContactPhone
	formData[qrReportContactEmail] = values.ContactEmail
	issueData["form_data"] = formData
	if tpl != nil {
		issueData["template_id"] = tpl.ID
	}

	if _, err := h.xoluClient.Create(ctx, "issues", issueData); err != nil {
		h.logger.Error("failed to create the issue of an anonymous QR report", "asset_id", assetID, "error", err)
		h.page.RenderPublicError(w, http.StatusInternalServerError, t("error.server_error"), t("qr.report.error.create_failed"))
		return
	}

	// The tracking page is addressed by the opaque token, never by the issue
	// id: the anonymous channel does not hand out a handle on the record.
	http.Redirect(w, r, issueTrackLinkPrefix+trackToken+"?new=1", http.StatusSeeOther)
}

// MountIssueQRReportRoute registers the public routes of the QR channel.
// GET /s/{code} is already mounted by the base router; these are not wrapped
// in requireAuth, which is the whole point: the reporter has no account, so
// there is no session to require.
func (r *Router) MountIssueQRReportRoute(mux *http.ServeMux) {
	mux.HandleFunc("POST /s/{code}/report", r.handler.IssueQRReport)
	mux.HandleFunc("GET /track/{token}", r.handler.IssueTrack)
}
