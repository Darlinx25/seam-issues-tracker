package ui

// Per-asset QR codes for issue reporting (card "Generar y gestionar códigos QR
// por activo").
//
// The link on a printed QR is /s/{code}; QRScan resolves it. Each code is a
// row in the issue_qr_codes entity:
//
//	code            unique, 16 uppercase base32 chars (crypto/rand, 80 bits)
//	asset_id        the asset the link points at
//	asset_category  copied from the asset at generation time, same mapping as
//	                the issue report (criticality -> critico/normal)
//	active          false once revoked
//	created_at      RFC3339 UTC
//	created_by      user id of the operator who generated it
//	revoked_at      RFC3339 UTC, empty until revoked
//	qr_blob_key     xolu blob holding the rendered image (empty: no image yet)
//	qr_content      the URL the image encodes (audit: which host it was made for)
//	qr_format       stored image format ("svg")
//
// An asset has at most one ACTIVE code: generating is refused while one is
// live and the card offers regeneration instead. Regeneration revokes the old
// row (it is kept for the audit trail) and creates a new one, so an old printed
// QR stops working. Revoking leaves the asset without an active code, which is
// the state that makes the card offer "generate" again.
//
// Managing codes is an asset-editing action: auth.PermAssetsAssetEdit gates
// generating, regenerating and revoking, and auth.PermAssetsAssetView gates the
// read-only card. Issues have no permission namespace of their own in
// iteration 1, so the asset permission is the closest real one.

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
	"github.com/ha1tch/seam-ui/internal/auth"
)

const (
	// issueQRCodesEntity is the xolu entity holding the per-asset codes.
	issueQRCodesEntity = "issue_qr_codes"
	// issueQRCardElementID is the id of the card fragment root. The htmx
	// mutations target it by id and swap it whole.
	issueQRCardElementID = "issue-qr-card"
	// issueQRCodeBytes is the amount of randomness per code: 10 bytes encode
	// to 16 base32 chars (80 bits), far beyond accidental collisions.
	issueQRCodeBytes = 10
	// issueQRCodeAttempts bounds the collision retries. Hitting it is
	// practically impossible (80 bits), but the loop keeps the "unique and
	// unrepeatable" requirement enforced rather than assumed.
	issueQRCodeAttempts = 5
	// issueQRCodePrefix is the public path the QR encodes.
	issueQRCodePrefix = "/s/"
)

// issueQRCode is one row of issue_qr_codes.
type issueQRCode struct {
	ID            int64
	Code          string
	AssetID       int64
	AssetCategory string
	Active        bool
	CreatedAt     string
	CreatedBy     int64
	RevokedAt     string
	// QRBlobKey is the xolu blob holding the rendered image; empty when the
	// renderer was down or unconfigured at generation time. The code is
	// valid either way -- the image is a convenience, not the link.
	QRBlobKey string
	// QRContent is the URL that was encoded, kept for the audit trail: it
	// shows which public host a printed label was made for.
	QRContent string
	// QRFormat is the stored image format ("svg" today).
	QRFormat string
}

// issueQRCardData is what the card fragment needs to render.
type issueQRCardData struct {
	AssetID     int64
	Active      *issueQRCode
	LastRevoked *issueQRCode
	CanManage   bool
	// PublicLink is the absolute URL the QR encodes when a public prefix is
	// configured, the in-app /s/{code} otherwise. Shown and copied.
	PublicLink string
	// RendererEnabled reports whether an image renderer is configured, so
	// the card only offers "Retry image" where retrying can actually work.
	RendererEnabled bool
	// CSRFToken travels inside the fragment. The layout's csrf-token meta tag
	// is empty on the asset detail page (the page data never carries a token),
	// so htmx has nothing to send and every mutation from the card would be
	// rejected with 403. The card carries the token itself instead of
	// depending on a tag it does not own.
	CSRFToken string
	// ErrorKey is an i18n key shown as an alert above the state; "" for none.
	ErrorKey string
}

// newIssueQRCode returns a fresh random code. base32's alphabet is A-Z plus
// 2-7, so the code survives being read aloud or typed without the usual 0/O
// and 1/I confusion; it is uppercased because the link is fixed once printed.
func newIssueQRCode() (string, error) {
	buf := make([]byte, issueQRCodeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return code, nil
}

// validIssueQRCode reports whether s has the shape newIssueQRCode produces.
// Used to skip the issue_qr_codes lookup for legacy codes (e.g. "QR-ABC123")
// that could never live in the new table.
func validIssueQRCode(s string) bool {
	if len(s) != issueQRCodeBytes*8/5 {
		return false
	}
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// issueQRCodeIsActive normalises the stored active flag. xolu returns JSON
// booleans as bool, but a row written by hand (or by a seed) may carry a
// string, so both shapes are accepted.
func issueQRCodeIsActive(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return x != 0
	case int64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}

// issueQRCodeFromRow maps an OQL row (or an entity's Data map) to the card's
// value type.
func issueQRCodeFromRow(row map[string]any) issueQRCode {
	return issueQRCode{
		ID:            toInt64(row["id"]),
		Code:          toString(row["code"]),
		AssetID:       toInt64(row["asset_id"]),
		AssetCategory: toString(row["asset_category"]),
		Active:        issueQRCodeIsActive(row["active"]),
		CreatedAt:     toString(row["created_at"]),
		CreatedBy:     toInt64(row["created_by"]),
		RevokedAt:     toString(row["revoked_at"]),
		QRBlobKey:     toString(row["qr_blob_key"]),
		QRContent:     toString(row["qr_content"]),
		QRFormat:      toString(row["qr_format"]),
	}
}

// -----------------------------------------------------------------------------
// Permission gates
// -----------------------------------------------------------------------------

// canViewAssetQRCodes allows anyone who may see an asset to see its code.
func (h *UIHandler) canViewAssetQRCodes(r *http.Request) bool {
	u := h.getCurrentUser(r)
	if u == nil || u.ID == 0 {
		return false
	}
	return auth.HasPermission(u.Role, auth.PermAssetsAssetView)
}

// canManageAssetQRCodes allows generating, regenerating and revoking codes:
// the same permission that lets a user edit the asset itself.
func (h *UIHandler) canManageAssetQRCodes(r *http.Request) bool {
	u := h.getCurrentUser(r)
	if u == nil || u.ID == 0 {
		return false
	}
	return auth.HasPermission(u.Role, auth.PermAssetsAssetEdit)
}

// -----------------------------------------------------------------------------
// Reads
// -----------------------------------------------------------------------------

// issueQRCodesForAsset returns every code of an asset, newest first. A missing
// table (no code was ever generated) is not an error: it means "none".
func (h *UIHandler) issueQRCodesForAsset(ctx context.Context, assetID int64) ([]issueQRCode, error) {
	if assetID <= 0 {
		return nil, nil
	}
	res, err := h.xoluClient.OQL(ctx, "SELECT * FROM "+issueQRCodesEntity+
		" WHERE asset_id = "+strconv.FormatInt(assetID, 10)+" ORDER BY id DESC")
	if err != nil {
		if entityNotYetCreated(err) {
			return nil, nil
		}
		return nil, err
	}
	codes := make([]issueQRCode, 0, len(res.Data))
	for _, row := range res.Data {
		codes = append(codes, issueQRCodeFromRow(row))
	}
	return codes, nil
}

// latestIssueQRCodes picks the active code and the last revoked one out of the
// asset's history. Both are needed by the card: the active code is what the
// asset exposes today, the last revoked one explains why a code disappeared.
func latestIssueQRCodes(codes []issueQRCode) (*issueQRCode, *issueQRCode) {
	var active, revoked *issueQRCode
	for i := range codes {
		code := codes[i]
		if code.Active {
			if active == nil {
				c := code
				active = &c
			}
			continue
		}
		if revoked == nil {
			c := code
			revoked = &c
		}
	}
	return active, revoked
}

// issueQRCodeExists checks a candidate code against both tables: the new one
// and the legacy qr_codes table, whose codes are scanned by the same route.
func (h *UIHandler) issueQRCodeExists(ctx context.Context, code string) (bool, error) {
	q := escapeOQL(code)
	for _, table := range []string{issueQRCodesEntity, "qr_codes"} {
		res, err := h.xoluClient.OQL(ctx, "SELECT TOP 1 id FROM "+table+" WHERE code = '"+q+"'")
		if err != nil {
			if entityNotYetCreated(err) {
				continue
			}
			return false, err
		}
		if len(res.Data) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// -----------------------------------------------------------------------------
// Writes
// -----------------------------------------------------------------------------

// createIssueQRCode generates a code and stores it as the asset's active one.
// The category is copied here, at generation time, with the same helper the
// issue report uses, so the code keeps the category the asset had when the
// printed QR was made.
func (h *UIHandler) createIssueQRCode(ctx context.Context, assetID, createdBy int64, category string) (issueQRCode, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	for attempt := 0; attempt < issueQRCodeAttempts; attempt++ {
		code, err := newIssueQRCode()
		if err != nil {
			return issueQRCode{}, err
		}
		taken, err := h.issueQRCodeExists(ctx, code)
		if err != nil {
			return issueQRCode{}, err
		}
		if taken {
			h.logger.Info("issue qr code collision, retrying", "asset", assetID, "code", code)
			continue
		}
		created, err := h.xoluClient.Create(ctx, issueQRCodesEntity, map[string]any{
			"code":           code,
			"asset_id":       assetID,
			"asset_category": category,
			"active":         true,
			"created_at":     now,
			"created_by":     createdBy,
		})
		if err != nil {
			return issueQRCode{}, err
		}
		out := issueQRCode{
			Code:          code,
			AssetID:       assetID,
			AssetCategory: category,
			Active:        true,
			CreatedAt:     now,
			CreatedBy:     createdBy,
		}
		if created != nil {
			out.ID = created.ID
		}
		// The row exists from here on: the code is usable even if the image
		// below cannot be produced. Best-effort, never fatal (a renderer
		// outage must not close the reporting channel).
		if out.ID > 0 && h.issueQRImageRendererEnabled() {
			rendered, err := h.attachIssueQRImage(ctx, out)
			if err != nil {
				h.logger.Warn("issue qr image could not be rendered",
					"asset", assetID, "code", code, "error", err)
			} else {
				out = rendered
			}
		}
		return out, nil
	}
	return issueQRCode{}, fmt.Errorf("issue qr code could not be generated")
}

// revokeIssueQRCode deactivates a code and stamps when it happened. The row is
// never deleted: it is the record of the printed link that stopped working.
func (h *UIHandler) revokeIssueQRCode(ctx context.Context, code issueQRCode) error {
	if code.ID <= 0 {
		return fmt.Errorf("issue qr code has no id")
	}
	_, err := h.xoluClient.Patch(ctx, issueQRCodesEntity, code.ID, map[string]any{
		"active":     false,
		"revoked_at": time.Now().UTC().Format(time.RFC3339),
	})
	return err
}

// resolveScanCode resolves the code a QR link carries to an asset. The new
// per-asset codes win over the legacy qr_codes table, whose rows keep working
// unchanged. revoked is true when the code exists but was revoked (the public
// page then explains why it no longer works). found is false when no table
// knows the code.
func (h *UIHandler) resolveScanCode(ctx context.Context, code string) (assetID int64, found bool, revoked bool) {
	if code == "" {
		return 0, false, false
	}
	q := escapeOQL(code)
	if validIssueQRCode(code) {
		res, err := h.xoluClient.OQL(ctx, "SELECT TOP 1 * FROM "+issueQRCodesEntity+" WHERE code = '"+q+"'")
		if err == nil && len(res.Data) > 0 {
			row := res.Data[0]
			if !issueQRCodeIsActive(row["active"]) {
				return 0, true, true
			}
			return toInt64(row["asset_id"]), true, false
		}
		if err != nil && !entityNotYetCreated(err) {
			h.logger.Info("issue qr code lookup failed", "code", code, "error", err)
		}
	}

	res, err := h.xoluClient.OQL(ctx, "SELECT TOP 1 * FROM qr_codes WHERE code = '"+q+"'")
	if err != nil || len(res.Data) == 0 {
		return 0, false, false
	}
	return toInt64(res.Data[0]["asset_id"]), true, false
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

// AssetQRCodeCard handles GET /assets/{id}/qr/card, the fragment the asset
// detail page loads into its QR card. Read-only for viewers.
func (h *UIHandler) AssetQRCodeCard(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id := extractPathID(r.URL.Path, "/assets/")
	if id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	if !h.canViewAssetQRCodes(r) {
		h.Error403(w, r)
		return
	}
	if _, err := h.xoluClient.Get(ctx, "assets", id); err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	h.renderAssetQRCard(w, r, id, "")
}

// AssetQRCodeGenerate handles POST /assets/{id}/qr. An asset with a live code
// is refused: regeneration is the explicit action for that case.
func (h *UIHandler) AssetQRCodeGenerate(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id := extractPathID(r.URL.Path, "/assets/")
	if id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	if !h.canManageAssetQRCodes(r) {
		h.Error403(w, r)
		return
	}
	if _, err := h.xoluClient.Get(ctx, "assets", id); err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	active, err := h.activeIssueQRCode(ctx, id)
	if err != nil {
		h.logger.Error("issue qr codes could not be read", "asset", id, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.read_failed")
		return
	}
	if active != nil {
		h.renderAssetQRCard(w, r, id, "issue_qr.error.already_active")
		return
	}
	category := h.resolveAssetCategory(ctx, id)
	created, err := h.createIssueQRCode(ctx, id, h.getCurrentUserID(r), category)
	if err != nil {
		h.logger.Error("issue qr code could not be created", "asset", id, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.generate_failed")
		return
	}
	h.logger.Info("issue qr code generated", "asset", id, "code", created.Code, "category", created.AssetCategory)
	h.renderAssetQRCard(w, r, id, "")
}

// AssetQRCodeRegenerate handles POST /assets/{id}/qr/regenerate: revoke the
// live code, then create a replacement. If the revoke fails the new code is
// not created, so the asset is never left with two live codes.
func (h *UIHandler) AssetQRCodeRegenerate(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id := extractPathID(r.URL.Path, "/assets/")
	if id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	if !h.canManageAssetQRCodes(r) {
		h.Error403(w, r)
		return
	}
	if _, err := h.xoluClient.Get(ctx, "assets", id); err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	active, err := h.activeIssueQRCode(ctx, id)
	if err != nil {
		h.logger.Error("issue qr codes could not be read", "asset", id, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.read_failed")
		return
	}
	if active == nil {
		h.renderAssetQRCard(w, r, id, "issue_qr.error.none_active")
		return
	}
	if err := h.revokeIssueQRCode(ctx, *active); err != nil {
		h.logger.Error("issue qr code could not be revoked", "asset", id, "code", active.Code, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.revoke_failed")
		return
	}
	category := h.resolveAssetCategory(ctx, id)
	created, err := h.createIssueQRCode(ctx, id, h.getCurrentUserID(r), category)
	if err != nil {
		h.logger.Error("issue qr code could not be regenerated", "asset", id, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.generate_failed")
		return
	}
	h.logger.Info("issue qr code regenerated", "asset", id, "old", active.Code, "new", created.Code)
	h.renderAssetQRCard(w, r, id, "")
}

// AssetQRCodeRevoke handles POST /assets/{id}/qr/revoke: deactivate the live
// code and stop there. The card then offers generating a new one.
func (h *UIHandler) AssetQRCodeRevoke(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc(r)
	ctx := r.Context()
	id := extractPathID(r.URL.Path, "/assets/")
	if id == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	if !h.canManageAssetQRCodes(r) {
		h.Error403(w, r)
		return
	}
	if _, err := h.xoluClient.Get(ctx, "assets", id); err != nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("asset.not_found"))
		return
	}
	active, err := h.activeIssueQRCode(ctx, id)
	if err != nil {
		h.logger.Error("issue qr codes could not be read", "asset", id, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.read_failed")
		return
	}
	if active == nil {
		h.renderAssetQRCard(w, r, id, "issue_qr.error.none_active")
		return
	}
	if err := h.revokeIssueQRCode(ctx, *active); err != nil {
		h.logger.Error("issue qr code could not be revoked", "asset", id, "code", active.Code, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.revoke_failed")
		return
	}
	h.logger.Info("issue qr code revoked", "asset", id, "code", active.Code)
	h.renderAssetQRCard(w, r, id, "")
}

// activeIssueQRCode is the single live code of an asset, if any.
func (h *UIHandler) activeIssueQRCode(ctx context.Context, assetID int64) (*issueQRCode, error) {
	codes, err := h.issueQRCodesForAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	active, _ := latestIssueQRCodes(codes)
	return active, nil
}

// renderAssetQRCard reloads the asset's codes and renders the card fragment.
// Every mutation ends here, so the card always reflects what xolu stores.
func (h *UIHandler) renderAssetQRCard(w http.ResponseWriter, r *http.Request, assetID int64, errorKey string) {
	t := h.translateFunc(r)
	data := issueQRCardData{
		AssetID:         assetID,
		CanManage:       h.canManageAssetQRCodes(r),
		CSRFToken:       h.GetCSRFToken(w, r),
		ErrorKey:        errorKey,
		RendererEnabled: h.issueQRImageRendererEnabled(),
	}
	codes, err := h.issueQRCodesForAsset(r.Context(), assetID)
	if err != nil {
		h.logger.Error("issue qr codes could not be read", "asset", assetID, "error", err)
		if data.ErrorKey == "" {
			data.ErrorKey = "issue_qr.error.read_failed"
		}
	} else {
		data.Active, data.LastRevoked = latestIssueQRCodes(codes)
	}
	if data.Active != nil {
		data.PublicLink = h.issueQRImageContent(data.Active.Code)
	}
	_ = h.page.RenderPartial(w, issueQRCodeCard(data, t))
}

// -----------------------------------------------------------------------------
// Rendering
// -----------------------------------------------------------------------------

// issueQRCardLoader is what the asset detail page embeds. It renders a small
// placeholder that replaces itself with the card fragment on load, so the
// detail page does not need the codes before it can be shown.
func issueQRCardLoader(assetID int64, t func(key string, args ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		return b.Div(
			mi.ID(issueQRCardElementID),
			mi.Attr("hx-get", fmt.Sprintf("/assets/%d/qr/card", assetID)),
			mi.Attr("hx-trigger", "load"),
			mi.Attr("hx-swap", "outerHTML"),
			mi.Class("flex items-center justify-center gap-2 py-4 text-sm text-gray-500 dark:text-gray-400"),
			LoadingSpinner("sm")(b),
			b.Span(t("issue_qr.loading")),
		)
	}
}

// issueQRCodeCard renders the card fragment. The root keeps
// issueQRCardElementID so the mutations, which swap the root whole, leave a
// valid target behind.
func issueQRCodeCard(data issueQRCardData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	return func(b *mi.Builder) mi.Node {
		children := []interface{}{
			mi.ID(issueQRCardElementID),
			mi.Class("space-y-3"),
		}
		if data.ErrorKey != "" {
			children = append(children, Alert(t(data.ErrorKey), "danger", false)(b))
		}
		if data.Active != nil {
			children = append(children, issueQRCodeActiveContent(data, t)(b))
		} else {
			children = append(children, issueQRCodeEmptyContent(data, t)(b))
		}
		return b.Div(children...)
	}
}

// issueQRCodeActiveContent renders the live code: the link, its category, the
// copy button and, for managers, regenerate and revoke.
func issueQRCodeActiveContent(data issueQRCardData, t func(key string, args ...any) string) mi.H {
	code := data.Active
	link := data.PublicLink
	if link == "" {
		link = issueQRCodePrefix + code.Code
	}
	return func(b *mi.Builder) mi.Node {
		children := []interface{}{
			b.Div(mi.Class("flex items-center justify-between gap-2"),
				Badge(t("issue_qr.active"), "success")(b),
				b.Span(mi.Class("text-xs text-gray-500 dark:text-gray-400"), code.CreatedAt),
			),
		}
		// The printed image and the link are the two halves of the same
		// thing: show the image when it exists, and when a renderer is
		// configured but produced nothing, say so and offer the retry.
		if code.QRBlobKey != "" || data.RendererEnabled {
			children = append(children, issueQRImageBlock(data, *code, t)(b))
		}
		children = append(children,
			b.A(
				mi.Href(link),
				mi.Target("_blank"),
				mi.Class("block truncate font-mono text-sm text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"),
				link,
			),
			b.Div(mi.Class("flex flex-wrap items-center gap-2"),
				Button(t("issue_qr.copy"), "secondary",
					mi.Type("button"),
					mi.Attr("onclick", issueQRCodeCopyScript(link)),
				)(b),
			),
			b.P(mi.Class("text-xs text-gray-500 dark:text-gray-400"),
				t("issue_qr.category")+": "+t("issues.category."+code.AssetCategory),
			),
		)
		if data.CanManage {
			children = append(children,
				b.Div(mi.Class("flex flex-wrap items-center gap-2"),
					Button(t("issue_qr.regenerate"), "secondary",
						mi.Type("button"),
						mi.Attr("onclick", issueQRCodeConfirmScript(
							data.AssetID,
							"regenerate",
							t("issue_qr.confirm_regenerate"),
							t("issue_qr.confirm_regenerate_title"),
							t("issue_qr.confirm_regenerate_ok"),
							data.CSRFToken,
						)),
					)(b),
					Button(t("issue_qr.revoke"), "danger",
						mi.Type("button"),
						mi.Attr("onclick", issueQRCodeConfirmScript(
							data.AssetID,
							"revoke",
							t("issue_qr.confirm_revoke"),
							t("issue_qr.confirm_revoke_title"),
							t("issue_qr.confirm_revoke_ok"),
							data.CSRFToken,
						)),
					)(b),
				),
			)
		}
		return b.Div(children...)
	}
}

// issueQRCodeEmptyContent renders the no-live-code state: the generate form
// for managers, a hint for everyone else. When a code was revoked the card
// says so instead of pretending nothing ever existed.
func issueQRCodeEmptyContent(data issueQRCardData, t func(key string, args ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		children := []interface{}{}
		if data.LastRevoked != nil {
			note := t("issue_qr.revoked_note")
			if data.LastRevoked.RevokedAt != "" {
				note = note + " (" + data.LastRevoked.RevokedAt + ")"
			}
			children = append(children, b.P(mi.Class("text-xs text-gray-500 dark:text-gray-400"), note))
		}
		children = append(children, b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("issue_qr.none")))
		if data.CanManage {
			children = append(children, b.Form(
				mi.Method("post"),
				mi.Action(fmt.Sprintf("/assets/%d/qr/generate", data.AssetID)),
				mi.Attr("hx-post", fmt.Sprintf("/assets/%d/qr/generate", data.AssetID)),
				mi.Attr("hx-target", "#"+issueQRCardElementID),
				mi.Attr("hx-swap", "outerHTML"),
				CSRFField(data.CSRFToken)(b),
				Button(t("issue_qr.generate"), "primary", mi.Type("submit"))(b),
			))
		} else {
			children = append(children, b.P(mi.Class("text-xs text-gray-500 dark:text-gray-400"), t("issue_qr.read_only")))
		}
		return b.Div(append([]interface{}{mi.Class("space-y-2")}, children...)...)
	}
}

// -----------------------------------------------------------------------------
// Inline scripts
// -----------------------------------------------------------------------------

// jsQuote builds a single-quoted JavaScript string for an inline handler. The
// double quote and the angle brackets are hex-escaped so the result can sit
// inside an HTML attribute without needing the markup layer to help.
func jsQuote(s string) string {
	var sb strings.Builder
	sb.WriteByte('\'')
	for _, c := range s {
		switch c {
		case '\\':
			sb.WriteString(`\\`)
		case '\'':
			sb.WriteString(`\'`)
		case '"':
			sb.WriteString(`\x22`)
		case '<':
			sb.WriteString(`\x3c`)
		case '>':
			sb.WriteString(`\x3e`)
		case '\n', '\r':
			sb.WriteString(`\n`)
		default:
			sb.WriteRune(c)
		}
	}
	sb.WriteByte('\'')
	return sb.String()
}

// issueQRCodeCopyScript copies the link the operator needs to paste into a chat
// or a ticket. With a public prefix configured the link is already absolute (the
// same URL the QR encodes); otherwise the browser origin is prepended so the
// pasted link never points at "/s/{code}" on someone else's machine.
func issueQRCodeCopyScript(link string) string {
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return "Seam.Utils.copyToClipboard(" + jsQuote(link) + ")"
	}
	return "Seam.Utils.copyToClipboard(window.location.origin+" + jsQuote(link) + ")"
}

// issueQRCodeConfirmScript asks before a destructive change and then posts it,
// swapping the card with the response. Regenerate and revoke both end the life
// of a printed link, so neither runs on a single click. The CSRF token is sent
// as an explicit header: htmx.ajax posts outside the element tree, so it does
// not inherit anything from the card.
func issueQRCodeConfirmScript(assetID int64, action, message, title, okLabel, csrfToken string) string {
	url := fmt.Sprintf("/assets/%d/qr/%s", assetID, action)
	headers := ""
	if csrfToken != "" {
		headers = ",headers:{'X-CSRF-Token':" + jsQuote(csrfToken) + "}"
	}
	return "SeamConfirm.showDanger(" + jsQuote(message) +
		",function(){htmx.ajax('POST'," + jsQuote(url) +
		",{target:" + jsQuote("#"+issueQRCardElementID) + ",swap:'outerHTML'" + headers + "})}" +
		",{title:" + jsQuote(title) + ",okLabel:" + jsQuote(okLabel) + "})"
}
