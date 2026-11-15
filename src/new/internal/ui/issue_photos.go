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
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
)

const (
	issuePhotosEntity      = "seam_media_items"
	issuePhotoOwnerType    = "issue"
	issuePhotoInputPrefix  = "issue_photo_"
	maxIssuePhotoBytes     = 20 << 20
	maxIssuePhotosPerField = 10
	issuePhotoMemoryLimit  = 32 << 20
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

// prepareIssuePhotos validates the photo fields of a report before the issue
// is created: required fields must carry at least one file, the per-field
// count is bounded, and every file must be a real image within the size
// limit. Files are not read into memory here -- only the first block needed
// for DetectContentType is read.
func (h *UIHandler) prepareIssuePhotos(r *http.Request, tpl *IssueTemplate, t func(key string, args ...any) string) (map[string][]*multipart.FileHeader, map[string]string) {
	files := map[string][]*multipart.FileHeader{}
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
			_ = file.Close()
			if !allowedIssuePhotoType(http.DetectContentType(head[:n])) {
				errors[f.Key] = t("issues.photos.error.invalid_type")
				valid = false
				break
			}
		}
		if valid {
			files[f.Key] = headers
		}
	}
	return files, errors
}

// storeIssuePhotos uploads every validated file to xolu and writes its
// metadata row. A failed file is logged and skipped: the issue already exists
// at this point, so losing the report over a storage hiccup would be worse
// than storing the remaining photos.
func (h *UIHandler) storeIssuePhotos(ctx context.Context, issueID int64, files map[string][]*multipart.FileHeader, uploadedBy string) map[string][]mediaItem {
	stored := map[string][]mediaItem{}
	if issueID <= 0 {
		return stored
	}
	for field, headers := range files {
		position := h.nextIssuePhotoPosition(ctx, issueID, field)
		for _, header := range headers {
			file, err := header.Open()
			if err != nil {
				h.logger.Error("issue photo open failed", "error", err, "issue_id", issueID, "field", field)
				continue
			}
			data, err := io.ReadAll(io.LimitReader(file, maxIssuePhotoBytes+1))
			_ = file.Close()
			if err != nil || len(data) == 0 || len(data) > maxIssuePhotoBytes {
				h.logger.Error("issue photo read failed", "error", err, "issue_id", issueID, "field", field)
				continue
			}

			contentType := http.DetectContentType(data)
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
