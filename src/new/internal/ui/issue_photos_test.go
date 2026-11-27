package ui

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	heic "github.com/gen2brain/h265/heic"
)

const issuePhotoTestPNG = "\x89PNG\r\n\x1a\n" + "seam-photo-test"

func issuePhotoTestHEIC(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 24; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 10), G: uint8(y * 15), B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := heic.Encode(&buf, img, heic.EncodeOptions{Quality: 60}); err != nil {
		t.Fatalf("encode HEIC fixture: %v", err)
	}
	if !isHEICImage(buf.Bytes()) {
		t.Fatalf("encoded fixture is not detected as HEIC")
	}
	return buf.Bytes()
}

func photoTemplateRow() map[string]any {
	return map[string]any{
		"id": float64(9), "key": "pump_photo", "name": "Pump photo", "version": float64(1),
		"is_current": float64(1),
		"fields": []any{
			map[string]any{"key": "serial_no", "label": "Serial No", "type": "text", "required": false},
			map[string]any{"key": "photo", "label": "Asset photos", "type": "image", "required": false},
		},
	}
}

func issuePhotoResolver(query string) []map[string]any {
	switch {
	case strings.Contains(query, issueTemplateAssignmentsEntity):
		return []map[string]any{{"template_key": "pump_photo"}}
	case strings.Contains(query, issueTemplatesEntity):
		return []map[string]any{photoTemplateRow()}
	case strings.Contains(query, issuePhotosEntity):
		return []map[string]any{}
	default:
		return []map[string]any{}
	}
}

// newIssuePhotoUploadRequest builds the multipart body a browser sends once
// the report form mounts the native file input of an "image" field.
func newIssuePhotoUploadRequest(t *testing.T, fields map[string]string, field, filename string, data []byte) *http.Request {
	t.Helper()
	return newIssuePhotoUploadRequestAt(t, "/issues", fields, field, filename, data)
}

func newIssuePhotoUploadRequestAt(t *testing.T, target string, fields map[string]string, field, filename string, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("WriteField(%s): %v", k, err)
		}
	}
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write photo bytes: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest("POST", target, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// TestIssueTemplateFields_RendersNativePhotoInput checks the field is not
// routed through the formengine (which would render an edit-only widget) and
// that the native control submits all selected files without JavaScript.
func TestIssueTemplateFields_RendersNativePhotoInput(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/": mockEntityGet(map[string]any{"id": float64(7), "type_id": float64(3)}),
		"POST /api/v1/oql":    func(w http.ResponseWriter, r *http.Request) { templateOQL(w, r, issuePhotoResolver) },
	})
	defer server.Close()
	h := newHandler(t, server)

	req := httptest.NewRequest("GET", "/issue-templates/fields?asset_id=7", nil)
	rec := httptest.NewRecorder()
	h.IssueTemplateFields(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`name="issue_photo_photo"`,
		`type="file"`,
		`multiple`,
		`accept="image/*"`,
		`block w-full text-sm`,
		`file:rounded-lg`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected photo field markup to contain %q", want)
		}
	}
	// The control must be visible: a hidden input would again need script to
	// restore the selected files after a validation re-render.
	if strings.Contains(body, "sr-only") {
		t.Error("the photo input must be visible, not sr-only")
	}
	if strings.Contains(body, "issue-photo-field.js") {
		t.Error("the photo field must not depend on JavaScript")
	}
}

// TestIssueCreate_WithPhoto_UploadsBlobAndMetadata is the end-to-end path:
// the report validates, creates the issue, puts the bytes in xolu's blob
// store and writes one seam_media_items row pointing at that blob.
func TestIssueCreate_WithPhoto_UploadsBlobAndMetadata(t *testing.T) {
	var createdIssue map[string]any
	var blobKey, blobType string
	var metadata map[string]any
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/": mockEntityGet(map[string]any{"id": float64(7), "type_id": float64(3)}),
		"POST /api/v1/oql":    func(w http.ResponseWriter, r *http.Request) { templateOQL(w, r, issuePhotoResolver) },
		"GET /api/v2/fsm/def": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"definitions": []map[string]any{{"id": float64(1), "name": "seam_issue_lifecycle"}}})
		},
		"POST /api/v2/fsm/machine": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "reported"})
		},
		"POST /api/v1/blob": func(w http.ResponseWriter, r *http.Request) {
			blobKey = r.Header.Get("X-Blob-Key")
			blobType = r.Header.Get("Content-Type")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"key": blobKey, "sha256": "abc"})
		},
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&createdIssue)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(42)})
		},
		"POST /api/v1/seam_media_items": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&metadata)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(77)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := newIssuePhotoUploadRequest(t, map[string]string{
		"title": "Pump leaking", "asset_id": "7", "serial_no": "SN-1",
	}, issuePhotoFieldName("photo"), "pump.png", []byte(issuePhotoTestPNG))
	rec := httptest.NewRecorder()
	h.IssueCreate(rec, req)

	assertRedirectsTo(t, rec, "/issues/42")
	if createdIssue == nil {
		t.Fatal("the issue was never created")
	}
	if !strings.HasPrefix(blobKey, "issue-42-photo-") {
		t.Errorf("expected a flat issue/photo blob key, got %q", blobKey)
	}
	if blobType != "image/png" {
		t.Errorf("expected the sniffed content type image/png, got %q", blobType)
	}
	if metadata == nil {
		t.Fatal("no seam_media_items row was written")
	}
	if metadata["owner_type"] != issuePhotoOwnerType {
		t.Errorf("expected owner_type=%q, got %v", issuePhotoOwnerType, metadata["owner_type"])
	}
	if metadata["owner_id"] != float64(42) {
		t.Errorf("expected owner_id=42, got %v", metadata["owner_id"])
	}
	if metadata["owner_field"] != "photo" {
		t.Errorf("expected owner_field=photo, got %v", metadata["owner_field"])
	}
	if metadata["blob_key"] != blobKey {
		t.Errorf("expected blob_key=%q, got %v", blobKey, metadata["blob_key"])
	}
}

// TestIssueCreate_WithHEIC_StoresJPEG checks the whole path stores the
// converted bytes, not the original HEIC carrier the browser detected.
func TestIssueCreate_WithHEIC_StoresJPEG(t *testing.T) {
	var blobType, metadataType string
	var blobBytes []byte
	server := mockOLU(t, map[string]http.HandlerFunc{
		"GET /api/v1/assets/": mockEntityGet(map[string]any{"id": float64(7), "type_id": float64(3)}),
		"POST /api/v1/oql":    func(w http.ResponseWriter, r *http.Request) { templateOQL(w, r, issuePhotoResolver) },
		"GET /api/v2/fsm/def": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"definitions": []map[string]any{{"id": float64(1), "name": "seam_issue_lifecycle"}}})
		},
		"POST /api/v2/fsm/machine": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "state": "reported"})
		},
		"POST /api/v1/blob": func(w http.ResponseWriter, r *http.Request) {
			blobType = r.Header.Get("Content-Type")
			blobBytes, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"key": "stored", "sha256": "abc"})
		},
		"POST /api/v1/issues": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(42)})
		},
		"POST /api/v1/seam_media_items": func(w http.ResponseWriter, r *http.Request) {
			var row map[string]any
			json.NewDecoder(r.Body).Decode(&row)
			metadataType = toString(row["content_type"])
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": float64(77)})
		},
	})
	defer server.Close()
	h := newHandler(t, server)

	req := newIssuePhotoUploadRequest(t, map[string]string{
		"title": "Pump leaking", "asset_id": "7",
	}, issuePhotoFieldName("photo"), "photo.heic", issuePhotoTestHEIC(t))
	rec := httptest.NewRecorder()
	h.IssueCreate(rec, req)

	assertRedirectsTo(t, rec, "/issues/42")
	if blobType != "image/jpeg" || metadataType != "image/jpeg" {
		t.Errorf("expected JPEG storage, got blob=%q metadata=%q", blobType, metadataType)
	}
	if len(blobBytes) == 0 || http.DetectContentType(blobBytes) != "image/jpeg" {
		t.Errorf("stored blob is not a JPEG: %d bytes", len(blobBytes))
	}
}

// TestPrepareIssuePhotos_RejectsNonImage keeps the allowlist server-side:
// the accept attribute is a hint, and a renamed text file must not land in
// the gallery.
func TestPrepareIssuePhotos_RejectsNonImage(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{})
	defer server.Close()
	h := newHandler(t, server)

	req := newIssuePhotoUploadRequest(t, nil, issuePhotoFieldName("photo"), "fake.png", []byte("not an image at all"))
	if err := parseIssueFormRequest(req); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	tpl := &IssueTemplate{Fields: []TemplateField{{Key: "photo", Label: "Photos", Type: "image"}}}
	files, errs := h.prepareIssuePhotos(req, tpl, h.translateFunc(nil))

	if len(files) != 0 {
		t.Errorf("expected no accepted files, got %d", len(files))
	}
	if errs["photo"] != "issues.photos.error.invalid_type" {
		t.Errorf("expected the invalid-type error, got %q", errs["photo"])
	}
}

// TestPrepareIssuePhotos_RequiredFieldEmpty produces the same required error
// the other custom fields use, so the re-rendered form keeps one convention.
func TestPrepareIssuePhotos_RequiredFieldEmpty(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{})
	defer server.Close()
	h := newHandler(t, server)

	req := newIssuePhotoUploadRequest(t, map[string]string{"title": "x"}, issuePhotoFieldName("serial_no"), "not-used.txt", []byte("x"))
	if err := parseIssueFormRequest(req); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	tpl := &IssueTemplate{Fields: []TemplateField{{Key: "photo", Label: "Photos", Type: "image", Required: true}}}
	_, errs := h.prepareIssuePhotos(req, tpl, h.translateFunc(nil))

	if errs["photo"] != "field.required" {
		t.Errorf("expected field.required, got %q", errs["photo"])
	}
}

// TestPrepareIssuePhotos_ConvertsHEIC accepts the format phones actually
// send and stores JPEG bytes, so the gallery never gets an octet-stream blob.
func TestPrepareIssuePhotos_ConvertsHEIC(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{})
	defer server.Close()
	h := newHandler(t, server)

	req := newIssuePhotoUploadRequest(t, nil, issuePhotoFieldName("photo"), "photo.heic", issuePhotoTestHEIC(t))
	if err := parseIssueFormRequest(req); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	tpl := &IssueTemplate{Fields: []TemplateField{{Key: "photo", Label: "Photos", Type: "image"}}}
	files, errs := h.prepareIssuePhotos(req, tpl, h.translateFunc(nil))

	if errs["photo"] != "" {
		t.Fatalf("expected HEIC to be accepted, got error %q", errs["photo"])
	}
	photos := files["photo"]
	if len(photos) != 1 {
		t.Fatalf("expected one accepted photo, got %d", len(photos))
	}
	if len(photos[0].Data) == 0 {
		t.Fatal("expected converted JPEG bytes")
	}
	if got := http.DetectContentType(photos[0].Data); got != "image/jpeg" {
		t.Errorf("expected converted bytes to sniff as image/jpeg, got %q", got)
	}
	if photos[0].Filename != "photo.jpg" {
		t.Errorf("expected a .jpg name, got %q", photos[0].Filename)
	}
}

// TestPrepareIssuePhotos_InvalidHEIC reports a conversion failure before the
// issue is created: a truncated carrier must not become a missing photo.
func TestPrepareIssuePhotos_InvalidHEIC(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{})
	defer server.Close()
	h := newHandler(t, server)

	heic := []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00heicmif1")
	req := newIssuePhotoUploadRequest(t, nil, issuePhotoFieldName("photo"), "photo.heic", heic)
	if err := parseIssueFormRequest(req); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	tpl := &IssueTemplate{Fields: []TemplateField{{Key: "photo", Label: "Photos", Type: "image"}}}
	files, errs := h.prepareIssuePhotos(req, tpl, h.translateFunc(nil))

	if len(files) != 0 {
		t.Errorf("expected no accepted files, got %d", len(files))
	}
	if errs["photo"] != "issues.photos.error.heic_conversion_failed" {
		t.Errorf("expected the HEIC conversion error, got %q", errs["photo"])
	}
}

// TestPrepareIssuePhotos_EnforcesTenPerField is the server-side half of the
// limit: no browser attribute can enforce it, so the report must be refused.
func TestPrepareIssuePhotos_EnforcesTenPerField(t *testing.T) {
	server := mockOLU(t, map[string]http.HandlerFunc{})
	defer server.Close()
	h := newHandler(t, server)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for i := 0; i < maxIssuePhotosPerField+1; i++ {
		part, err := w.CreateFormFile(issuePhotoFieldName("photo"), "photo-"+strconv.Itoa(i)+".png")
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write([]byte(issuePhotoTestPNG)); err != nil {
			t.Fatalf("write photo bytes: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest("POST", "/issues", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := parseIssueFormRequest(req); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	tpl := &IssueTemplate{Fields: []TemplateField{{Key: "photo", Label: "Photos", Type: "image"}}}
	files, errs := h.prepareIssuePhotos(req, tpl, h.translateFunc(nil))

	if len(files) != 0 {
		t.Errorf("expected no accepted files, got %d", len(files))
	}
	if errs["photo"] != "issues.photos.error.too_many" {
		t.Errorf("expected the too-many error, got %q", errs["photo"])
	}
}
