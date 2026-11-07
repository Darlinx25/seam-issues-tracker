package ui

// Tests for the rendered QR image (the image half of the per-asset QR card):
// the renderer call, the blob stored in xolu, the proxied image route and the
// "Retry image" action.
//
// The renderer is a local httptest server, so these tests pin the wire contract
// the external seam-2dcodes service exposes -- the same contract the real
// service implements -- and, more importantly, the failure paths: a renderer
// outage must never cost the code itself, because the code is the reporting
// channel and the picture is only the label.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ha1tch/seam-ui/internal/auth"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// qrImageTestSVG is the image the stub renderer returns. Real SVG, so the
// bytes can be compared verbatim through the blob and the proxy.
const qrImageTestSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="21" height="21"><rect width="21" height="21" fill="#fff"/></svg>`

// qrImageTestCode is a valid code: 16 base32 chars, the shape newIssueQRCode
// produces. The image route rejects anything else before touching xolu.
const qrImageTestCode = "ABCDEFGHIJKLMNOP"

// ── Helpers ──────────────────────────────────────────────────────────────────

// qrImageTestRenderer is the stub 2D-code service. seen collects every request
// body so a test can assert what would have been encoded; status != 200 makes
// it fail the way an unavailable or erroring service does.
func qrImageTestRenderer(t *testing.T, status int, seen *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != issueQRImageV1Path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the renderer received a non-JSON body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if seen != nil {
			*seen = append(*seen, body)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"format":   issueQRImageFormat,
			"encoding": "base64",
			"data":     base64.StdEncoding.EncodeToString([]byte(qrImageTestSVG)),
		})
	}))
}

// qrImageTestBlob captures what was uploaded to xolu's blob store.
type qrImageTestBlob struct {
	key         string
	contentType string
	data        []byte
}

// qrImageTestBlobRoute answers POST /api/v1/blob and records the upload.
func qrImageTestBlobRoute(t *testing.T, captured *qrImageTestBlob) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		captured.key = r.Header.Get("X-Blob-Key")
		captured.contentType = r.Header.Get("Content-Type")
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("could not read the uploaded blob: %v", err)
		}
		captured.data = data
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"key":     captured.key,
			"sha256":  "0000000000000000000000000000000000000000000000000000000000000000",
			"created": true,
		})
	}
}

// qrImageTestHandler builds a handler with the given image settings. Tests that
// do not care about images use its sibling newHandler (empty Config), which
// leaves the renderer disabled.
func qrImageTestHandler(t *testing.T, server *httptest.Server, cfg Config) *UIHandler {
	t.Helper()
	if cfg.AppName == "" {
		cfg.AppName = "Test"
		cfg.Version = "0.0.0"
	}
	return NewUIHandler(xolu.New(server.URL), cfg, nil, nil, nil, nil, nil, nil, nil)
}

// qrImageTestRow is an issue_qr_codes row as OQL returns it.
func qrImageTestRow(id int64, code string, assetID int64, blobKey string, active bool) map[string]any {
	row := map[string]any{
		"id":             float64(id),
		"code":           code,
		"asset_id":       float64(assetID),
		"asset_category": "normal",
		"active":         active,
		"created_at":     "2026-11-01T10:00:00Z",
		"created_by":     float64(1),
	}
	if blobKey != "" {
		row["qr_blob_key"] = blobKey
		row["qr_format"] = issueQRImageFormat
	}
	return row
}

// ── Generation ───────────────────────────────────────────────────────────────

// TestIssueQRImage_GenerateRendersAndStoresImage covers the happy path: the
// renderer is asked for the absolute URL of the code, the returned image lands
// in xolu's blob store, the row records the key, and the card shows the image
// with its download link.
func TestIssueQRImage_GenerateRendersAndStoresImage(t *testing.T) {
	var seen []map[string]any
	renderer := qrImageTestRenderer(t, http.StatusOK, &seen)
	defer renderer.Close()

	var created map[string]any
	var patched map[string]any
	var blob qrImageTestBlob

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5, "name": "Pump", "criticality": "Media"})
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 42)
		},
		"PATCH /api/v1/issue_qr_codes/": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			w.WriteHeader(http.StatusOK)
		},
		"POST /api/v1/blob": qrImageTestBlobRoute(t, &blob),
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if created != nil && strings.Contains(q, "SELECT *") &&
				strings.Contains(q, "FROM "+issueQRCodesEntity) {
				row := qrTestStoredRow(42, created, true, "")
				if patched != nil {
					row["qr_blob_key"] = patched["qr_blob_key"]
					row["qr_content"] = patched["qr_content"]
					row["qr_format"] = patched["qr_format"]
				}
				qrTestRows(w, []map[string]any{row})
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{
		QRRenderURL: renderer.URL,
		QRBaseURL:   "https://seam.example",
	})
	rec := qrTestPost(t, h.AssetQRCodeGenerate, "/assets/5/qr/generate", auth.RoleAdmin)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for the card fragment, got %d", rec.Code)
	}
	if created == nil {
		t.Fatal("no code was created")
	}
	code := toString(created["code"])

	// The renderer must be asked for the URL a phone can open, not the bare
	// code: a QR with only 16 base32 chars scans but goes nowhere.
	if len(seen) != 1 {
		t.Fatalf("the renderer must be called once, got %d calls", len(seen))
	}
	req := seen[0]
	wantContent := "https://seam.example/s/" + code
	if got := toString(req["content"]); got != wantContent {
		t.Errorf("the renderer must encode the public URL: want %q, got %q", wantContent, got)
	}
	if got := toString(req["symbology"]); got != "qr" {
		t.Errorf("want symbology qr, got %q", got)
	}
	if got := toString(req["format"]); got != issueQRImageFormat {
		t.Errorf("want format %q, got %q", issueQRImageFormat, got)
	}
	if got := toString(req["output"]); got != "base64" {
		t.Errorf("want the inline base64 output, got %q", got)
	}
	options, _ := req["options"].(map[string]any)
	if got := toString(options["level"]); got != issueQRImageLevel {
		t.Errorf("want error correction %q, got %q", issueQRImageLevel, got)
	}
	if got := toInt64(options["quietZone"]); got != issueQRImageQuietZone {
		t.Errorf("want quiet zone %d, got %d", issueQRImageQuietZone, got)
	}

	if blob.key != issueQRImageBlobKey(code) {
		t.Errorf("blob key must be %q, got %q", issueQRImageBlobKey(code), blob.key)
	}
	if blob.contentType != issueQRImageMIME {
		t.Errorf("want the SVG content type, got %q", blob.contentType)
	}
	if string(blob.data) != qrImageTestSVG {
		t.Errorf("the stored blob must be the rendered image, got %q", string(blob.data))
	}
	if patched == nil {
		t.Fatal("the row must record the stored image")
	}
	if got := toString(patched["qr_blob_key"]); got != issueQRImageBlobKey(code) {
		t.Errorf("row must store the blob key, got %q", got)
	}
	if got := toString(patched["qr_content"]); got != wantContent {
		t.Errorf("row must record the encoded URL, want %q, got %q", wantContent, got)
	}
	if got := toString(patched["qr_format"]); got != issueQRImageFormat {
		t.Errorf("row must record the format, want %q, got %q", issueQRImageFormat, got)
	}

	body := rec.Body.String()
	if !strings.Contains(body, issueQRImageSrc(5, code)) {
		t.Errorf("the card must show the image of the code")
	}
	if !strings.Contains(body, "download=1") {
		t.Errorf("the card must offer the image download")
	}
	if !strings.Contains(body, wantContent) {
		t.Errorf("the card must show the absolute public link, want %q", wantContent)
	}
}

// TestIssueQRImage_RendererFailureKeepsCode is the rule the whole design hangs
// on: the code is written before the image is attempted, so a renderer outage
// leaves a working code and only costs the picture. The card says so and offers
// the retry.
func TestIssueQRImage_RendererFailureKeepsCode(t *testing.T) {
	var seen []map[string]any
	renderer := qrImageTestRenderer(t, http.StatusInternalServerError, &seen)
	defer renderer.Close()

	var created map[string]any
	blobCalled := false
	patchCalled := false

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 42)
		},
		"POST /api/v1/blob": func(w http.ResponseWriter, r *http.Request) {
			blobCalled = true
			w.WriteHeader(http.StatusInternalServerError)
		},
		"PATCH /api/v1/issue_qr_codes/": func(w http.ResponseWriter, r *http.Request) {
			patchCalled = true
			w.WriteHeader(http.StatusOK)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if created != nil && strings.Contains(q, "SELECT *") &&
				strings.Contains(q, "FROM "+issueQRCodesEntity) {
				qrTestRows(w, []map[string]any{qrTestStoredRow(42, created, true, "")})
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{QRRenderURL: renderer.URL})
	rec := qrTestPost(t, h.AssetQRCodeGenerate, "/assets/5/qr/generate", auth.RoleAdmin)

	if created == nil {
		t.Fatal("the code must be created even when the renderer fails")
	}
	if blobCalled {
		t.Error("nothing must be stored when the renderer returned nothing")
	}
	if patchCalled {
		t.Error("the row must not be patched when there is no image")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/s/"+toString(created["code"])) {
		t.Error("the card must still show the working link")
	}
	if !strings.Contains(body, "issue_qr.image_missing") {
		t.Error("the card must say the image is missing")
	}
	if !strings.Contains(body, "issue_qr.retry_image") {
		t.Error("the card must offer to retry the image")
	}
}

// TestIssueQRImage_NoRendererLeavesCardUnchanged covers the deployment that has
// no renderer configured: the card must look exactly like it did before images
// existed, with no promise of a retry that could only fail.
func TestIssueQRImage_NoRendererLeavesCardUnchanged(t *testing.T) {
	var created map[string]any
	blobCalled := false

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
		"POST /api/v1/issue_qr_codes": func(w http.ResponseWriter, r *http.Request) {
			created = qrTestDecodeBody(t, r)
			oqlCreated(w, 42)
		},
		"POST /api/v1/blob": func(w http.ResponseWriter, r *http.Request) {
			blobCalled = true
			w.WriteHeader(http.StatusOK)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			q := qrTestQuery(t, r)
			if created != nil && strings.Contains(q, "SELECT *") &&
				strings.Contains(q, "FROM "+issueQRCodesEntity) {
				qrTestRows(w, []map[string]any{qrTestStoredRow(42, created, true, "")})
				return
			}
			qrTestRows(w, nil)
		},
	})
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{})
	rec := qrTestPost(t, h.AssetQRCodeGenerate, "/assets/5/qr/generate", auth.RoleAdmin)

	if created == nil {
		t.Fatal("no code was created")
	}
	if blobCalled {
		t.Error("no blob must be stored when no renderer is configured")
	}
	body := rec.Body.String()
	if strings.Contains(body, "issue_qr.image_missing") ||
		strings.Contains(body, "issue_qr.retry_image") {
		t.Error("an unconfigured renderer must not advertise the image feature")
	}
	if !strings.Contains(body, "/s/"+toString(created["code"])) {
		t.Error("the card must still show the working link")
	}
}

// ── Image route ──────────────────────────────────────────────────────────────

// TestIssueQRCodeImage_ServesStoredImage covers the inline case: the bytes come
// from the blob, the type is fixed (never echoed), and the response is private
// and immutable while the code remains the one in its own URL.
func TestIssueQRCodeImage_ServesStoredImage(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/blob/": func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, issueQRImageBlobKey(qrImageTestCode)) {
				t.Errorf("the handler asked for the wrong blob: %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(qrImageTestSVG))
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{
				qrImageTestRow(7, qrImageTestCode, 5, issueQRImageBlobKey(qrImageTestCode), true),
			})
		},
	})
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{})
	rec := qrTestGet(t, h.AssetQRCodeImage,
		"/assets/5/qr/image?v="+qrImageTestCode, auth.RoleViewer)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != issueQRImageMIME {
		t.Errorf("want content type %q, got %q", issueQRImageMIME, got)
	}
	if got := rec.Body.String(); got != qrImageTestSVG {
		t.Errorf("the served body must be the stored image, got %q", got)
	}
	cache := rec.Header().Get("Cache-Control")
	if !strings.Contains(cache, "immutable") || !strings.Contains(cache, "private") {
		t.Errorf("the inline image must be private and immutable, got %q", cache)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("the inline image must not be an attachment, got %q", got)
	}
}

// TestIssueQRCodeImage_Download covers the download case: an attachment with a
// filename derived from the code, and no long-lived cache entry.
func TestIssueQRCodeImage_Download(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/blob/": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(qrImageTestSVG))
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			qrTestRows(w, []map[string]any{
				qrImageTestRow(7, qrImageTestCode, 5, issueQRImageBlobKey(qrImageTestCode), true),
			})
		},
	})
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{})
	rec := qrTestGet(t, h.AssetQRCodeImage,
		"/assets/5/qr/image?download=1&v="+qrImageTestCode, auth.RoleViewer)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	want := `attachment; filename="` + issueQRImageBlobKey(qrImageTestCode) + `"`
	if got := rec.Header().Get("Content-Disposition"); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
	if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Errorf("a download must not be cached as immutable, got %q", got)
	}
}

// TestIssueQRCodeImage_NotFound covers every reason the image route must 404:
// an unknown or malformed code, a revoked code, a code that belongs to another
// asset, a code with no image yet, and an image the blob store lost.
func TestIssueQRCodeImage_NotFound(t *testing.T) {
	cases := []struct {
		name string
		code string
		rows []map[string]any
		blob int
	}{
		{
			name: "malformed code",
			code: "not-a-code",
			blob: http.StatusOK,
		},
		{
			name: "unknown code",
			code: qrImageTestCode,
			blob: http.StatusOK,
		},
		{
			name: "revoked code",
			code: qrImageTestCode,
			rows: []map[string]any{
				qrImageTestRow(7, qrImageTestCode, 5, issueQRImageBlobKey(qrImageTestCode), false),
			},
			blob: http.StatusOK,
		},
		{
			name: "other asset",
			code: qrImageTestCode,
			rows: []map[string]any{
				qrImageTestRow(7, qrImageTestCode, 9, issueQRImageBlobKey(qrImageTestCode), true),
			},
			blob: http.StatusOK,
		},
		{
			name: "no image yet",
			code: qrImageTestCode,
			rows: []map[string]any{
				qrImageTestRow(7, qrImageTestCode, 5, "", true),
			},
			blob: http.StatusOK,
		},
		{
			name: "blob gone",
			code: qrImageTestCode,
			rows: []map[string]any{
				qrImageTestRow(7, qrImageTestCode, 5, issueQRImageBlobKey(qrImageTestCode), true),
			},
			blob: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := mockOLU(t, map[string]http.HandlerFunc{
				"GET /api/v1/blob/": func(w http.ResponseWriter, r *http.Request) {
					if tc.blob == http.StatusOK {
						_, _ = w.Write([]byte(qrImageTestSVG))
						return
					}
					w.WriteHeader(tc.blob)
				},
				"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
					qrTestRows(w, tc.rows)
				},
			})
			defer server.Close()

			h := qrImageTestHandler(t, server, Config{})
			rec := qrTestGet(t, h.AssetQRCodeImage,
				"/assets/5/qr/image?v="+tc.code, auth.RoleViewer)

			if rec.Code != http.StatusNotFound {
				t.Errorf("expected 404, got %d", rec.Code)
			}
			if strings.Contains(rec.Body.String(), qrImageTestSVG) {
				t.Error("no image bytes must leak on a rejected request")
			}
		})
	}
}

// TestIssueQRCodeImage_RequiresViewPermission covers the gate: the image sits
// behind the asset view permission like the rest of the asset detail.
func TestIssueQRCodeImage_RequiresViewPermission(t *testing.T) {
	server := mockOLU(t, nil)
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{})
	// No claims at all: the auth middleware never ran, so there is no user.
	rec := httptest.NewRecorder()
	h.AssetQRCodeImage(rec, httptest.NewRequest(http.MethodGet,
		"/assets/5/qr/image?v="+qrImageTestCode, nil))

	if rec.Code != http.StatusForbidden {
		t.Errorf("an anonymous request must be refused, got %d", rec.Code)
	}
}

// ── Retry ────────────────────────────────────────────────────────────────────

// TestIssueQRCodeRenderImage_RetryRendersMissingImage covers the retry action:
// the code is not touched, only its image is rendered and recorded, and the
// card comes back showing it.
func TestIssueQRCodeRenderImage_RetryRendersMissingImage(t *testing.T) {
	var seen []map[string]any
	renderer := qrImageTestRenderer(t, http.StatusOK, &seen)
	defer renderer.Close()

	var patched map[string]any
	var blob qrImageTestBlob

	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/5": func(w http.ResponseWriter, r *http.Request) {
			qrTestJSON(w, map[string]any{"id": 5})
		},
		"POST /api/v1/blob": qrImageTestBlobRoute(t, &blob),
		"PATCH /api/v1/issue_qr_codes/": func(w http.ResponseWriter, r *http.Request) {
			patched = qrTestDecodeBody(t, r)
			w.WriteHeader(http.StatusOK)
		},
		"POST /api/v1/oql/query": func(w http.ResponseWriter, r *http.Request) {
			row := qrImageTestRow(7, qrImageTestCode, 5, "", true)
			if patched != nil {
				row["qr_blob_key"] = patched["qr_blob_key"]
			}
			qrTestRows(w, []map[string]any{row})
		},
	})
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{
		QRRenderURL: renderer.URL,
		QRBaseURL:   "https://seam.example",
	})
	rec := qrTestPost(t, h.AssetQRCodeRenderImage, "/assets/5/qr/image", auth.RoleAdmin)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for the card fragment, got %d", rec.Code)
	}
	if len(seen) != 1 {
		t.Fatalf("the renderer must be called once, got %d", len(seen))
	}
	if blob.key != issueQRImageBlobKey(qrImageTestCode) {
		t.Errorf("want the code's blob key, got %q", blob.key)
	}
	if patched == nil {
		t.Fatal("the row must record the image")
	}
	if got := toString(patched["qr_content"]); got != "https://seam.example/s/"+qrImageTestCode {
		t.Errorf("the retry must encode the public URL, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), issueQRImageSrc(5, qrImageTestCode)) {
		t.Error("the card must show the image after a successful retry")
	}
}

// TestIssueQRCodeRenderImage_ForbiddenForViewer covers the write gate: retrying
// writes a blob and patches the row, so it needs the asset edit permission.
func TestIssueQRCodeRenderImage_ForbiddenForViewer(t *testing.T) {
	rendererCalled := false
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rendererCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer renderer.Close()

	patchCalled := false
	server := mockOLU(t, map[string]http.HandlerFunc{
		"PATCH /api/v1/issue_qr_codes/": func(w http.ResponseWriter, r *http.Request) {
			patchCalled = true
			w.WriteHeader(http.StatusOK)
		},
	})
	defer server.Close()

	h := qrImageTestHandler(t, server, Config{QRRenderURL: renderer.URL})
	rec := qrTestPost(t, h.AssetQRCodeRenderImage, "/assets/5/qr/image", auth.RoleViewer)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a viewer must not render images, got %d", rec.Code)
	}
	if rendererCalled {
		t.Error("the renderer must not be called for a refused request")
	}
	if patchCalled {
		t.Error("nothing must be written for a refused request")
	}
}

// ── Public link ──────────────────────────────────────────────────────────────

// TestQRPublicLink covers the prefix normalisation. Every shape an operator is
// likely to put in features.qr_base_url must produce the same working URL: a
// printed label cannot be fixed once it is on a machine.
func TestQRPublicLink(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"", "/s/" + qrImageTestCode},
		{"https://seam.example", "https://seam.example/s/" + qrImageTestCode},
		{"https://seam.example/", "https://seam.example/s/" + qrImageTestCode},
		{"https://seam.example/s", "https://seam.example/s/" + qrImageTestCode},
		{"https://seam.example/s/", "https://seam.example/s/" + qrImageTestCode},
		{"  https://seam.example/s/  ", "https://seam.example/s/" + qrImageTestCode},
	}
	for _, tc := range cases {
		if got := qrPublicLink(tc.base, qrImageTestCode); got != tc.want {
			t.Errorf("qrPublicLink(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}
