package ui

// QR images for the per-asset codes.
//
// The code itself lives in issue_qr_codes; the picture of it comes from an
// external 2D-code renderer (seam-2dcodes) and is stored in xolu's blob store,
// the same way gallery photos are (media_gallery.go): a key-addressed blob,
// streamed back through a proxy route so the browser never needs xolu's own
// address or credentials.
//
// Rendering is best-effort by design. A code whose image could not be produced
// is still valid and still resolves: the row is written first, the image is
// attached afterwards, and a renderer outage only costs the picture, never the
// reporting channel. The card then offers "Retry image".
//
// The renderer receives the URL the QR must encode, not the bare code. A phone
// scanning a QR that contains only the 16 base32 characters has nothing to
// open; the absolute URL is what makes the printed label work.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
)

const (
	// issueQRImageV1Path is the renderer's code endpoint.
	issueQRImageV1Path = "/v1/codes"
	// issueQRImageFormat is SVG: the label is printed, and a vector image
	// stays sharp at any size without re-rendering.
	issueQRImageFormat = "svg"
	// issueQRImageMIME is what the blob is stored and served as. Only this
	// one format is ever written by this module.
	issueQRImageMIME = "image/svg+xml"
	// issueQRImageLevel is the QR error-correction level. H survives a
	// scratched, folded or partly dirty label; the cost is a denser symbol.
	issueQRImageLevel = "H"
	// issueQRImageQuietZone is the white border around the symbol, in
	// pixels. Scanners need it, and printers tend to crop too close.
	issueQRImageQuietZone = 16
	// issueQRImageSize is the size the renderer is asked for. The SVG is
	// vectorial; this only fixes the viewport written into the file.
	issueQRImageSize = 600
	// issueQRImageCardSize is the preview size in the card, in pixels.
	issueQRImageCardSize = 192
	// issueQRImageTimeout bounds the renderer call so a hung service cannot
	// hold an asset page open.
	issueQRImageTimeout = 10 * time.Second
	// issueQRImageMaxBytes bounds what is accepted from the renderer. A QR
	// SVG of a URL is a few KB; anything near this ceiling is not a QR.
	issueQRImageMaxBytes = 2 << 20
)

var (
	// errIssueQRImageNotConfigured means features.qr_render_url is empty:
	// the module still mints codes, it just has no way to draw them.
	errIssueQRImageNotConfigured = errors.New("issue qr image renderer is not configured")
	// issueQRImageHTTPClient is the client used for every renderer call.
	// A package variable so tests can point the module at an httptest
	// server through Config.QRRenderURL without touching this.
	issueQRImageHTTPClient = &http.Client{Timeout: issueQRImageTimeout}
)

// issueQRImageBlobKey is the blob key of a code's image. Blob keys are flat --
// no "/" is allowed (xolu's own validateKey) -- so the code goes in the name
// instead of a folder.
func issueQRImageBlobKey(code string) string {
	return "qr-" + code + "." + issueQRImageFormat
}

// qrPublicLink joins the configured public prefix with a code. The config value
// means "base + code" (its documented use in the legacy generator is
// BaseURL + code), so "https://host", "https://host/", "https://host/s" and
// "https://host/s/" all produce the same link. The trailing "/s" is added when
// the configured value has no path of its own: a bare host is the mistake an
// operator is most likely to make, and a QR pointing at "https://host/CODE"
// would 404 on every scan.
func qrPublicLink(base, code string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return issueQRCodePrefix + code
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/s") {
		base += "/s"
	}
	return base + "/" + code
}

// issueQRImageContent is the absolute URL a code's QR encodes, or the in-app
// relative link when no public prefix is configured.
func (h *UIHandler) issueQRImageContent(code string) string {
	return qrPublicLink(h.config.QRBaseURL, code)
}

// issueQRImageRendererEnabled reports whether a renderer is configured.
func (h *UIHandler) issueQRImageRendererEnabled() bool {
	return strings.TrimSpace(h.config.QRRenderURL) != ""
}

// issueQRImageSrc is the card's <img> source. The code travels as a query value
// so the handler can find the row (and prove the blob belongs to this asset)
// without trusting a blob key sent by the browser.
func issueQRImageSrc(assetID int64, code string) string {
	return fmt.Sprintf("/assets/%d/qr/image?v=%s", assetID, code)
}

// issueQRCodeByCode is the single-row lookup by code, used by the image route.
func (h *UIHandler) issueQRCodeByCode(ctx context.Context, code string) (*issueQRCode, error) {
	if !validIssueQRCode(code) {
		return nil, nil
	}
	res, err := h.xoluClient.OQL(ctx, "SELECT TOP 1 * FROM "+issueQRCodesEntity+
		" WHERE code = '"+escapeOQL(code)+"'")
	if err != nil {
		if entityNotYetCreated(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(res.Data) == 0 {
		return nil, nil
	}
	row := issueQRCodeFromRow(res.Data[0])
	return &row, nil
}

// renderIssueQRImage asks the renderer for the image of content and returns the
// decoded bytes. Only the inline base64 output is accepted: the renderer's own
// blob output would have it upload straight to xolu, which is the path its
// S2D-1 open item says was never verified, and this module already knows how to
// store a blob.
func (h *UIHandler) renderIssueQRImage(ctx context.Context, content string) ([]byte, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(h.config.QRRenderURL), "/")
	if endpoint == "" {
		return nil, errIssueQRImageNotConfigured
	}
	if content == "" {
		return nil, errors.New("issue qr image has no content to encode")
	}

	payload, err := json.Marshal(map[string]any{
		"symbology": "qr",
		"content":   content,
		"format":    issueQRImageFormat,
		"output":    "base64",
		"options": map[string]any{
			"level":     issueQRImageLevel,
			"quietZone": issueQRImageQuietZone,
			"width":     issueQRImageSize,
			"height":    issueQRImageSize,
		},
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, issueQRImageTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+issueQRImageV1Path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := issueQRImageHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, issueQRImageMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("renderer returned HTTP %d", resp.StatusCode)
	}
	if len(body) > issueQRImageMaxBytes {
		return nil, fmt.Errorf("renderer response exceeds %d bytes", issueQRImageMaxBytes)
	}

	var out struct {
		Format   string `json:"format"`
		Encoding string `json:"encoding"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("renderer response is not JSON: %w", err)
	}
	if out.Encoding != "base64" || out.Data == "" {
		return nil, errors.New("renderer did not return inline base64 data")
	}
	data, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return nil, fmt.Errorf("renderer returned invalid base64: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("renderer returned an empty image")
	}
	return data, nil
}

// attachIssueQRImage renders a code's image, stores it in xolu's blob store and
// records the blob key on the row. Returns the updated value so callers can log
// or render without re-reading. Every failure leaves the code untouched and
// still valid.
func (h *UIHandler) attachIssueQRImage(ctx context.Context, code issueQRCode) (issueQRCode, error) {
	if code.ID <= 0 {
		return code, errors.New("issue qr code has no id")
	}
	if !h.issueQRImageRendererEnabled() {
		return code, errIssueQRImageNotConfigured
	}

	content := h.issueQRImageContent(code.Code)
	data, err := h.renderIssueQRImage(ctx, content)
	if err != nil {
		return code, err
	}

	key := issueQRImageBlobKey(code.Code)
	if _, err := h.xoluClient.BlobPut(ctx, key, issueQRImageMIME, bytes.NewReader(data)); err != nil {
		return code, fmt.Errorf("store qr image: %w", err)
	}
	if _, err := h.xoluClient.Patch(ctx, issueQRCodesEntity, code.ID, map[string]any{
		"qr_blob_key": key,
		"qr_content":  content,
		"qr_format":   issueQRImageFormat,
	}); err != nil {
		return code, fmt.Errorf("record qr image: %w", err)
	}

	code.QRBlobKey = key
	code.QRContent = content
	code.QRFormat = issueQRImageFormat
	return code, nil
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

// AssetQRCodeImage handles GET /assets/{id}/qr/image?v={code}. It streams the
// stored image, either inline for the card's <img> or as a download. The row is
// read first so the blob key is server-side truth: a browser cannot ask this
// route for an arbitrary blob by guessing a key.
func (h *UIHandler) AssetQRCodeImage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := extractPathID(r.URL.Path, "/assets/")
	if id == 0 {
		http.NotFound(w, r)
		return
	}
	if !h.canViewAssetQRCodes(r) {
		h.Error403(w, r)
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("v"))
	if !validIssueQRCode(code) {
		http.NotFound(w, r)
		return
	}

	stored, err := h.issueQRCodeByCode(ctx, code)
	if err != nil {
		h.logger.Info("issue qr code lookup failed", "code", code, "error", err)
		http.NotFound(w, r)
		return
	}
	// A revoked code keeps its row and its blob, but its image is no longer
	// served: revocation must kill every way back to the asset.
	if stored == nil || stored.AssetID != id || !stored.Active || stored.QRBlobKey == "" {
		http.NotFound(w, r)
		return
	}

	result, err := h.xoluClient.BlobGet(ctx, stored.QRBlobKey)
	if err != nil {
		h.logger.Info("issue qr image missing from blob storage", "asset", id, "code", code, "error", err)
		http.NotFound(w, r)
		return
	}
	defer result.Body.Close()

	// Only SVG is ever stored under this key, so the type is fixed here
	// rather than echoed from storage: the endpoint can never turn into an
	// arbitrary-content proxy even if a blob were overwritten.
	w.Header().Set("Content-Type", issueQRImageMIME)
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition",
			"attachment; filename=\""+issueQRImageBlobKey(code)+"\"")
	} else {
		// The URL carries the code, and a new code is minted on every
		// regeneration, so this response can never go stale under its own
		// URL. "private" keeps it out of shared caches: the asset detail is
		// behind a session.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	if _, err := io.Copy(w, result.Body); err != nil {
		h.logger.Info("issue qr image stream failed", "asset", id, "code", code, "error", err)
	}
}

// AssetQRCodeRenderImage handles POST /assets/{id}/qr/image: the "Retry image"
// action. It renders the live code's image if it has none, then re-renders the
// card. The code itself is never touched, so a failed retry cannot invalidate a
// printed label.
func (h *UIHandler) AssetQRCodeRenderImage(w http.ResponseWriter, r *http.Request) {
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
	if active.QRBlobKey != "" {
		// Nothing to do: the image is already stored. Re-rendering the card
		// keeps the page truthful instead of failing a harmless request.
		h.renderAssetQRCard(w, r, id, "")
		return
	}

	updated, err := h.attachIssueQRImage(ctx, *active)
	if err != nil {
		h.logger.Error("issue qr image could not be rendered",
			"asset", id, "code", active.Code, "error", err)
		h.renderAssetQRCard(w, r, id, "issue_qr.error.render_failed")
		return
	}
	h.logger.Info("issue qr image rendered",
		"asset", id, "code", updated.Code, "blob", updated.QRBlobKey)
	h.renderAssetQRCard(w, r, id, "")
}

// -----------------------------------------------------------------------------
// Rendering
// -----------------------------------------------------------------------------

// issueQRImageBlock is the image, or the explanation and retry action when it
// could not be rendered. The caller only renders it when there is something to
// say: with no renderer configured the card looks exactly like it did before
// images existed, instead of promising a feature this deployment lacks.
func issueQRImageBlock(data issueQRCardData, code issueQRCode, t func(key string, args ...any) string) mi.H {
	return func(b *mi.Builder) mi.Node {
		src := issueQRImageSrc(data.AssetID, code.Code)
		if code.QRBlobKey != "" {
			return b.Div(mi.Class("flex flex-col items-center gap-2"),
				b.Img(
					mi.Src(src),
					mi.Alt(t("issue_qr.image_alt")),
					mi.Width(issueQRImageCardSize),
					mi.Height(issueQRImageCardSize),
					mi.Class("rounded border border-gray-200 bg-white p-2 dark:border-gray-700"),
				),
				b.A(
					mi.Href(src+"&download=1"),
					mi.Download(issueQRImageBlobKey(code.Code)),
					mi.Class("text-sm font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"),
					t("issue_qr.download"),
				),
			)
		}
		children := []interface{}{
			b.P(mi.Class("text-xs text-gray-500 dark:text-gray-400"), t("issue_qr.image_missing")),
		}
		if data.CanManage {
			action := fmt.Sprintf("/assets/%d/qr/image", data.AssetID)
			children = append(children, b.Form(
				mi.Method("post"),
				mi.Action(action),
				mi.Attr("hx-post", action),
				mi.Attr("hx-target", "#"+issueQRCardElementID),
				mi.Attr("hx-swap", "outerHTML"),
				CSRFField(data.CSRFToken)(b),
				Button(t("issue_qr.retry_image"), "secondary", mi.Type("submit"))(b),
			))
		}
		return b.Div(append([]interface{}{mi.Class("space-y-2")}, children...)...)
	}
}
