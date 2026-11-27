package ui

// Photos of an issue template field.
//
// The bytes live in xolu's blob storage, exactly like the asset gallery
// (media_gallery.go): a flat, human-legible blob key plus one row in
// seam_media_items carrying the owner (the issue), the template field and the
// display order. Reusing that entity keeps both galleries on the same storage
// and metadata model; nothing here needs a second service or a new table.
//
// The upload happens with the report itself, not when the photo is picked:
// the anonymous QR channel has no session and no issue id until the report is
// created. Keeping the files in the same multipart POST also means a report
// never leaves orphan blobs behind when its validation fails.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	heic "github.com/gen2brain/h265/heic"
	mi "github.com/ha1tch/minty"
)

const (
	issuePhotosEntity      = "seam_media_items"
	issuePhotoOwnerType    = "issue"
	issuePhotoInputPrefix  = "issue_photo_"
	maxIssuePhotoBytes     = 20 << 20
	maxIssuePhotosPerField = 10
	issuePhotoMemoryLimit  = 32 << 20
	maxIssuePhotoPixels    = 80_000_000
	issuePhotoJPEGQuality  = 88
)

// issuePhotoFieldName is the multipart field carrying the files of one
// template field. The field key itself is kept so several photo fields on the
// same template stay independent.
func issuePhotoFieldName(key string) string {
	return issuePhotoInputPrefix + key
}

// isIssuePhotoField reports whether a template field is a photo gallery.
func isIssuePhotoField(f TemplateField) bool {
	return f.Type == "image"
}

// issuePhotoFields returns the template's photo fields, in order.
func issuePhotoFields(tpl *IssueTemplate) []TemplateField {
	if tpl == nil {
		return nil
	}
	var out []TemplateField
	for _, f := range tpl.Fields {
		if isIssuePhotoField(f) {
			out = append(out, f)
		}
	}
	return out
}

// parseIssueFormRequest parses either a plain form or the multipart body a
// report with photos sends. ParseMultipartForm parses the form values too, so
// the handlers can keep using FormValue for the fixed fields.
func parseIssueFormRequest(r *http.Request) error {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		return r.ParseMultipartForm(issuePhotoMemoryLimit)
	}
	return r.ParseForm()
}

// allowedIssuePhotoType is the small allowlist of formats a browser can show
// in the gallery and in the detail page. The content type comes from sniffing
// the bytes, not from the filename or the client's own header.
func allowedIssuePhotoType(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// isHEICImage recognises HEIC/HEIF files by their ISO BMFF "ftyp" brand. Go's
// DetectContentType reports them as octet-stream, so they need a separate
// check to give the user a useful error instead of "invalid type".
func isHEICImage(head []byte) bool {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return false
	}
	for i := 8; i+4 <= len(head) && i < 64; i += 4 {
		switch string(head[i : i+4]) {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "hevm", "hevs", "mif1", "msf1":
			return true
		}
	}
	return false
}

// preparedIssuePhoto is one accepted upload. Data is nil for formats that can
// be uploaded as received; for HEIC/HEIF it holds the JPEG bytes produced
// before the issue is created, so every byte stored later matches the exact
// validation and metadata written here.
type preparedIssuePhoto struct {
	Header      *multipart.FileHeader
	Data        []byte
	ContentType string
	Filename    string
}

// convertHEICToJPEG turns the format phones produce into the JPEG the gallery
// and every browser can display. The decoder is pure Go; AutoRotate applies
// the HEIF rotation/mirror boxes, and an EXIF orientation is applied as well
// when the file carries one.
func convertHEICToJPEG(data []byte) ([]byte, error) {
	cfg, err := heic.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxIssuePhotoPixels {
		return nil, fmt.Errorf("heic image too large: %dx%d", cfg.Width, cfg.Height)
	}
	img, err := heic.Decode(bytes.NewReader(data), heic.Options{
		AutoRotate:     true,
		FrameSizeLimit: maxIssuePhotoPixels,
	})
	if err != nil {
		return nil, err
	}
	if exif, exifErr := heic.DecodeExif(bytes.NewReader(data)); exifErr == nil && exif != nil {
		img = orientIssueImage(img, exif.Orientation)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: issuePhotoJPEGQuality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// orientIssueImage applies the EXIF orientation values 2-8. HEIF containers
// usually carry irot/imir boxes (handled by AutoRotate), but a camera can
// still write the legacy tag and the JPEG would otherwise come out sideways.
func orientIssueImage(src image.Image, orientation int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if orientation < 1 || orientation > 8 {
		return src
	}
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch orientation {
			case 1:
				sx, sy = x, y
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = h-1-y, x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = y, w-1-x
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// jpegIssuePhotoName keeps a useful file name when the original was HEIC; the
// blob key is content-addressed, but logs and metadata still show an .jpg.
func jpegIssuePhotoName(name string) string {
	base := name
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if base == "" {
		base = "photo"
	}
	return base + ".jpg"
}

// prepareIssuePhotos validates the photo fields of a report before the issue
// is created: required fields must carry at least one file, the per-field
// count is bounded, and every file must be a real image within the size
// limit. HEIC/HEIF files are converted here so a bad file cannot create an
// issue whose photo silently disappears afterwards.
func (h *UIHandler) prepareIssuePhotos(r *http.Request, tpl *IssueTemplate, t func(key string, args ...any) string) (map[string][]preparedIssuePhoto, map[string]string) {
	files := map[string][]preparedIssuePhoto{}
	errors := map[string]string{}
	if tpl == nil || r.MultipartForm == nil {
		for _, f := range issuePhotoFields(tpl) {
			if f.Required {
				errors[f.Key] = t("field.required")
			}
		}
		return files, errors
	}

	for _, f := range issuePhotoFields(tpl) {
		headers := r.MultipartForm.File[issuePhotoFieldName(f.Key)]
		if f.Required && len(headers) == 0 {
			errors[f.Key] = t("field.required")
			continue
		}
		if len(headers) == 0 {
			continue
		}
		if len(headers) > maxIssuePhotosPerField {
			errors[f.Key] = t("issues.photos.error.too_many")
			continue
		}
		valid := true
		accepted := make([]preparedIssuePhoto, 0, len(headers))
		for _, header := range headers {
			if header.Size > maxIssuePhotoBytes {
				errors[f.Key] = t("issues.photos.error.too_large")
				valid = false
				break
			}
			file, err := header.Open()
			if err != nil {
				errors[f.Key] = t("issues.photos.error.invalid")
				valid = false
				break
			}
			head := make([]byte, 512)
			n, _ := io.ReadFull(file, head)
			if isHEICImage(head[:n]) {
				rest, readErr := io.ReadAll(io.LimitReader(file, int64(maxIssuePhotoBytes-len(head)+1)))
				_ = file.Close()
				data := append(append([]byte{}, head[:n]...), rest...)
				if readErr != nil || len(data) > maxIssuePhotoBytes || len(data) == 0 {
					errors[f.Key] = t("issues.photos.error.too_large")
					valid = false
					break
				}
				jpg, convertErr := convertHEICToJPEG(data)
				if convertErr != nil || len(jpg) == 0 || len(jpg) > maxIssuePhotoBytes {
					errors[f.Key] = t("issues.photos.error.heic_conversion_failed")
					valid = false
					break
				}
				accepted = append(accepted, preparedIssuePhoto{
					Header:      header,
					Data:        jpg,
					ContentType: http.DetectContentType(jpg),
					Filename:    jpegIssuePhotoName(header.Filename),
				})
				continue
			}
			_ = file.Close()
			if !allowedIssuePhotoType(http.DetectContentType(head[:n])) {
				errors[f.Key] = t("issues.photos.error.invalid_type")
				valid = false
				break
			}
			accepted = append(accepted, preparedIssuePhoto{Header: header, Filename: header.Filename})
		}
		if valid {
			files[f.Key] = accepted
		}
	}
	return files, errors
}

// storeIssuePhotos uploads every validated file to xolu and writes its
// metadata row. A failed file is logged and skipped: the issue already exists
// at this point, so losing the report over a storage hiccup would be worse
// than storing the remaining photos.
func (h *UIHandler) storeIssuePhotos(ctx context.Context, issueID int64, files map[string][]preparedIssuePhoto, uploadedBy string) map[string][]mediaItem {
	stored := map[string][]mediaItem{}
	if issueID <= 0 {
		return stored
	}
	for field, photos := range files {
		position := h.nextIssuePhotoPosition(ctx, issueID, field)
		for _, photo := range photos {
			data := photo.Data
			if data == nil {
				file, err := photo.Header.Open()
				if err != nil {
					h.logger.Error("issue photo open failed", "error", err, "issue_id", issueID, "field", field)
					continue
				}
				data, err = io.ReadAll(io.LimitReader(file, maxIssuePhotoBytes+1))
				_ = file.Close()
				if err != nil || len(data) == 0 || len(data) > maxIssuePhotoBytes {
					h.logger.Error("issue photo read failed", "error", err, "issue_id", issueID, "field", field)
					continue
				}
			}
			if len(data) == 0 || len(data) > maxIssuePhotoBytes {
				h.logger.Error("issue photo bytes rejected", "issue_id", issueID, "field", field)
				continue
			}

			contentType := photo.ContentType
			if contentType == "" {
				contentType = http.DetectContentType(data)
			}
			if !allowedIssuePhotoType(contentType) {
				h.logger.Error("issue photo content type rejected", "issue_id", issueID, "field", field, "content_type", contentType)
				continue
			}
			sum := sha256.Sum256(data)
			hash := hex.EncodeToString(sum[:])
			blobKey := fmt.Sprintf("issue-%d-%s-%s", issueID, issuePhotoKeySegment(field), hash)

			if _, err := h.xoluClient.BlobPut(ctx, blobKey, contentType, bytes.NewReader(data)); err != nil {
				h.logger.Error("issue photo blob upload failed", "error", err, "issue_id", issueID, "field", field)
				continue
			}

			uploadedAt := time.Now().UTC().Format(time.RFC3339)
			created, err := h.xoluClient.Create(ctx, issuePhotosEntity, map[string]any{
				"owner_type":   issuePhotoOwnerType,
				"owner_id":     issueID,
				"owner_field":  field,
				"blob_key":     blobKey,
				"content_type": contentType,
				"size_bytes":   int64(len(data)),
				"sha256":       hash,
				"position":     position,
				"uploaded_by":  uploadedBy,
				"uploaded_at":  uploadedAt,
			})
			if err != nil {
				h.logger.Error("issue photo metadata create failed", "error", err, "issue_id", issueID, "field", field)
				continue
			}

			itemID := int64(0)
			if created != nil {
				itemID = created.ID
			}
			stored[field] = append(stored[field], mediaItem{
				ID:          itemID,
				OwnerType:   issuePhotoOwnerType,
				OwnerID:     issueID,
				OwnerField:  field,
				BlobKey:     blobKey,
				ContentType: contentType,
				SizeBytes:   int64(len(data)),
				SHA256:      hash,
				Position:    position,
				UploadedBy:  uploadedBy,
				UploadedAt:  uploadedAt,
				URL:         galleryBlobURL(blobKey),
			})
			position++
		}
	}
	return stored
}

// issuePhotoKeySegment turns a template field key into a safe segment of a
// flat blob key. xolu's blob keys reject slashes, and a key built from admin
// input must not be able to change shape.
func issuePhotoKeySegment(field string) string {
	segment := slugify(field)
	if segment == "" {
		return "photo"
	}
	return segment
}

// nextIssuePhotoPosition returns how many photos already exist for this field.
// Like the asset gallery's own helper it is only a display-order hint, not a
// uniqueness constraint.
func (h *UIHandler) nextIssuePhotoPosition(ctx context.Context, issueID int64, field string) int {
	items, err := h.listIssuePhotosByField(ctx, issueID, field)
	if err != nil {
		return 0
	}
	return len(items)
}

// listIssuePhotosByField reads one field's photos. owner_id is an int64 and
// the field key is escaped before it reaches OQL, so neither can alter the
// query.
func (h *UIHandler) listIssuePhotosByField(ctx context.Context, issueID int64, field string) ([]mediaItem, error) {
	query := fmt.Sprintf(
		"SELECT * FROM %s WHERE owner_type = '%s' AND owner_id = %d AND owner_field = '%s' ORDER BY position ASC",
		issuePhotosEntity, issuePhotoOwnerType, issueID, escapeOQL(field),
	)
	result, err := h.xoluClient.OQL(ctx, query)
	if err != nil {
		if entityNotYetCreated(err) {
			return []mediaItem{}, nil
		}
		return nil, err
	}
	return issuePhotoItemsFromRows(result.Data), nil
}

// listIssuePhotos reads every photo of an issue in a single query, grouped by
// the template field they belong to, for the detail page.
func (h *UIHandler) listIssuePhotos(ctx context.Context, issueID int64) map[string][]mediaItem {
	out := map[string][]mediaItem{}
	if issueID <= 0 {
		return out
	}
	query := fmt.Sprintf(
		"SELECT * FROM %s WHERE owner_type = '%s' AND owner_id = %d ORDER BY owner_field ASC, position ASC",
		issuePhotosEntity, issuePhotoOwnerType, issueID,
	)
	result, err := h.xoluClient.OQL(ctx, query)
	if err != nil {
		if !entityNotYetCreated(err) {
			h.logger.Error("issue photo list failed", "error", err, "issue_id", issueID)
		}
		return out
	}
	for _, item := range issuePhotoItemsFromRows(result.Data) {
		out[item.OwnerField] = append(out[item.OwnerField], item)
	}
	return out
}

func issuePhotoItemsFromRows(rows []map[string]any) []mediaItem {
	items := make([]mediaItem, 0, len(rows))
	for _, row := range rows {
		blobKey := toString(row["blob_key"])
		items = append(items, mediaItem{
			ID:          toInt64(row["id"]),
			OwnerType:   toString(row["owner_type"]),
			OwnerID:     toInt64(row["owner_id"]),
			OwnerField:  toString(row["owner_field"]),
			BlobKey:     blobKey,
			ContentType: toString(row["content_type"]),
			SizeBytes:   toInt64(row["size_bytes"]),
			SHA256:      toString(row["sha256"]),
			Position:    int(toInt64(row["position"])),
			UploadedBy:  toString(row["uploaded_by"]),
			UploadedAt:  toString(row["uploaded_at"]),
			URL:         galleryBlobURL(blobKey),
		})
	}
	return items
}

// renderIssuePhotoThumbnails is the read-only gallery shown on the issue
// detail page for one photo field. Each thumbnail opens the full image in a
// new tab through the authenticated /gallery/blob proxy, the same route the
// asset gallery uses.
func renderIssuePhotoThumbnails(b *mi.Builder, photos []mediaItem, label string, t func(key string, args ...any) string) mi.Node {
	if len(photos) == 0 {
		return b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("issues.photos.empty"))
	}
	var images []interface{}
	for _, photo := range photos {
		images = append(images, b.A(
			mi.Href(photo.URL),
			mi.Attr("target", "_blank"),
			mi.Rel("noopener"),
			mi.Class("block"),
			b.Img(
				mi.Src(photo.URL),
				mi.Alt(label),
				mi.Class("w-24 h-24 object-cover rounded-lg border border-gray-200 dark:border-gray-700"),
			),
		))
	}
	return b.Div(append([]interface{}{mi.Class("flex flex-wrap gap-2 mt-1")}, images...)...)
}
