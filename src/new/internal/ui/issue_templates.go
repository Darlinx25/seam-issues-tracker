package ui

// Issue templates (2026-10-10) -- versioned, reusable field sets that can
// be assigned to asset types. When a user creates an issue and selects an
// asset, the template assigned to that asset's type is resolved and its
// custom fields are rendered into the create form; the submitted values are
// stored on the issue as form_data.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	mi "github.com/ha1tch/minty"
	fe "github.com/ha1tch/seam-ui/internal/formengine"
)

// TemplateField is one custom field definition in a template version.
type TemplateField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// IssueTemplate is one version of an issue template.
type IssueTemplate struct {
	ID          int64
	Key         string
	Name        string
	Description string
	Version     int
	Fields      []TemplateField
	IsCurrent   bool
	CreatedAt   string
}

const issueTemplatesEntity = "issue_templates"
const issueTemplateAssignmentsEntity = "issue_template_assignments"

// templateFieldTypes are the types offered by the row editor. Kept to
// scalar widgets the formengine renders without extra configuration.
func templateFieldTypes() []string {
	return []string{"text", "textarea", "number", "boolean", "date", "datetime"}
}

// toMap normalises a stored JSON object into map[string]any, returning nil
// for anything else.
func toMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func issueTemplateFromRow(row map[string]any) IssueTemplate {
	return IssueTemplate{
		ID:          toInt64(row["id"]),
		Key:         toString(row["key"]),
		Name:        toString(row["name"]),
		Description: toString(row["description"]),
		Version:     int(toInt64(row["version"])),
		Fields:      templateFieldsFromValue(row["fields"]),
		IsCurrent:   toBool(row["is_current"]),
		CreatedAt:   toString(row["created_at"]),
	}
}

// templateFieldsFromValue normalises a stored fields value (an ordered
// JSON array of objects) into []TemplateField, preserving order.
func templateFieldsFromValue(v any) []TemplateField {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]TemplateField, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, TemplateField{
			Key:      toString(m["key"]),
			Label:    toString(m["label"]),
			Type:     toString(m["type"]),
			Required: toBool(m["required"]),
		})
	}
	return out
}

func templateFieldsToValue(fields []TemplateField) []any {
	out := make([]any, 0, len(fields))
	for _, f := range fields {
		out = append(out, map[string]any{
			"key":      f.Key,
			"label":    f.Label,
			"type":     f.Type,
			"required": f.Required,
		})
	}
	return out
}

// parseTemplateFieldsFromForm reads the row-editor arrays. Each row emits a
// hidden index (tpl_field_idx[]) that the required checkbox name is keyed
// on (tpl_field_required_<idx>), so required detection does not depend on
// checkbox submission order.
func parseTemplateFieldsFromForm(r *http.Request) []TemplateField {
	if err := r.ParseForm(); err != nil {
		return nil
	}
	keys := r.Form["tpl_field_key[]"]
	labels := r.Form["tpl_field_label[]"]
	types := r.Form["tpl_field_type[]"]
	idxs := r.Form["tpl_field_idx[]"]

	var out []TemplateField
	for i, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		f := TemplateField{Key: key, Type: "text"}
		if i < len(labels) {
			f.Label = strings.TrimSpace(labels[i])
		}
		if i < len(types) && types[i] != "" {
			f.Type = types[i]
		}
		idx := ""
		if i < len(idxs) {
			idx = idxs[i]
		}
		f.Required = r.FormValue("tpl_field_required_"+idx) != ""
		out = append(out, f)
	}
	return out
}

// templateSchemaType maps a template field type to its JSON Schema type,
// optional format, and formengine widget name.
func templateSchemaType(t string) (jsonType, format, input string) {
	switch t {
	case "textarea":
		return "string", "", "textarea"
	case "number":
		return "number", "", "number"
	case "boolean":
		return "boolean", "", "checkbox"
	case "date":
		return "string", "date", "date"
	case "datetime":
		return "string", "date-time", "datetime-local"
	default:
		return "string", "", "text"
	}
}

// buildTemplateSchema renders an ordered JSON Schema document for the
// formengine. Properties are emitted in field order (manual buffer, not a
// map, so the order survives encoding).
func buildTemplateSchema(fields []TemplateField) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"type":"object","properties":{`)
	first := true
	var required []string
	for _, f := range fields {
		if f.Key == "" {
			continue
		}
		jsonType, format, input := templateSchemaType(f.Type)
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(f.Key)
		lb, _ := json.Marshal(f.Label)
		jb, _ := json.Marshal(jsonType)
		ib, _ := json.Marshal(input)
		buf.Write(kb)
		buf.WriteString(`:{"type":`)
		buf.Write(jb)
		if format != "" {
			fb, _ := json.Marshal(format)
			buf.WriteString(`,"format":`)
			buf.Write(fb)
		}
		buf.WriteString(`,"title":`)
		buf.Write(lb)
		buf.WriteString(`,"x-seam-input":`)
		buf.Write(ib)
		buf.WriteByte('}')
		if f.Required {
			required = append(required, f.Key)
		}
	}
	buf.WriteString(`},"required":[`)
	for i, k := range required {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}

// renderTemplateFields renders a resolved template's custom fields with the
// formengine. values/errors may be nil (the HTMX partial, and first render).
func (h *UIHandler) renderTemplateFields(ctx context.Context, tpl *IssueTemplate, values map[string]any, errors map[string]string, csrf string) mi.H {
	formEng := &fe.FormEngine{Translator: h.formEngineTranslator()}
	rc := fe.RenderContext{Locale: h.currentLocale(), CSRFToken: csrf, Module: "issues", Form: "issue-template"}
	return formEng.Render(ctx, buildTemplateSchema(tpl.Fields), values, errors, rc)
}

// loadCurrentTemplate loads the current (is_current=1, not deleted) version
// for a template key.
func (h *UIHandler) loadCurrentTemplate(ctx context.Context, key string) *IssueTemplate {
	if key == "" {
		return nil
	}
	res, err := h.xoluClient.OQL(ctx,
		"SELECT * FROM "+issueTemplatesEntity+" WHERE key = '"+escapeOQL(key)+"' AND is_current = 1 AND deleted = 0")
	if err != nil || res == nil || len(res.Data) == 0 {
		return nil
	}
	tpl := issueTemplateFromRow(res.Data[0])
	return &tpl
}

// resolveIssueTemplate finds the template assigned to the asset type of
// assetID, returning its current version.
func (h *UIHandler) resolveIssueTemplate(ctx context.Context, assetID int64) *IssueTemplate {
	if assetID <= 0 {
		return nil
	}
	asset, err := h.xoluClient.Get(ctx, "assets", assetID)
	if err != nil {
		return nil
	}
	typeID := toInt64(asset.Data["type_id"])
	if typeID <= 0 {
		return nil
	}
	res, err := h.xoluClient.OQL(ctx,
		"SELECT template_key FROM "+issueTemplateAssignmentsEntity+" WHERE asset_type_id = "+strconv.FormatInt(typeID, 10))
	if err != nil || res == nil || len(res.Data) == 0 {
		return nil
	}
	key := toString(res.Data[0]["template_key"])
	return h.loadCurrentTemplate(ctx, key)
}

// IssueTemplateFields handles GET /issue-templates/fields -- the HTMX
// partial that swaps in the assigned template's fields when the asset
// picker changes.
func (h *UIHandler) IssueTemplateFields(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	assetID := toInt64FromString(r.URL.Query().Get("asset_id"))
	if assetID <= 0 {
		_ = h.page.RenderPartial(w, func(b *mi.Builder) mi.Node { return b.Span() })
		return
	}
	tpl := h.resolveIssueTemplate(ctx, assetID)
	if tpl == nil || len(tpl.Fields) == 0 {
		_ = h.page.RenderPartial(w, func(b *mi.Builder) mi.Node { return b.Span() })
		return
	}
	_ = h.page.RenderPartial(w, h.renderTemplateFields(ctx, tpl, nil, nil, ""))
}

// IssueTemplatesList handles GET /issue-templates.
func (h *UIHandler) IssueTemplatesList(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()

	var tpls []IssueTemplate
	res, err := h.xoluClient.OQL(ctx, "SELECT * FROM "+issueTemplatesEntity+" WHERE is_current = 1 AND deleted = 0 ORDER BY name")
	if err == nil {
		for _, row := range res.Data {
			tpls = append(tpls, issueTemplateFromRow(row))
		}
	} else {
		h.logger.Error("failed to list issue templates", "error", err)
	}

	h.page.Render(w, r, t("issues.templates.list_title"), "/issues", IssueTemplatesListPage(IssueTemplatesListData{Templates: tpls}, t))
}

// IssueTemplateNew handles GET /issue-templates/new.
func (h *UIHandler) IssueTemplateNew(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	data := IssueTemplateFormData{CSRFToken: h.GetCSRFToken(w, r), FieldTypes: templateFieldTypes()}
	h.page.Render(w, r, t("issues.templates.new"), "/issues", IssueTemplateFormPage(data, t))
}

// IssueTemplateCreate handles POST /issue-templates -- creates version 1.
func (h *UIHandler) IssueTemplateCreate(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()
	csrfToken := h.GetCSRFToken(w, r)

	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	fields := parseTemplateFieldsFromForm(r)

	renderErr := func(msg string) {
		data := IssueTemplateFormData{
			CSRFToken:   csrfToken,
			Name:        name,
			Description: description,
			Fields:      fields,
			FieldTypes:  templateFieldTypes(),
			Error:       msg,
		}
		_ = h.page.Render(w, r, t("issues.templates.new"), "/issues", IssueTemplateFormPage(data, t))
	}

	if name == "" {
		renderErr(t("issues.templates.error.name_required"))
		return
	}
	key := slugify(name)
	if key == "" {
		renderErr(t("issues.templates.error.name_required"))
		return
	}

	existing, err := h.xoluClient.OQL(ctx, "SELECT id FROM "+issueTemplatesEntity+" WHERE key = '"+escapeOQL(key)+"' AND deleted = 0")
	if err == nil && existing != nil && len(existing.Data) > 0 {
		renderErr(t("issues.templates.error.duplicate_key"))
		return
	}

	_, err = h.xoluClient.Create(ctx, issueTemplatesEntity, map[string]any{
		"key":         key,
		"name":        name,
		"description": description,
		"version":     1,
		"fields":      templateFieldsToValue(fields),
		"is_current":  1,
		"deleted":     0,
		"created_at":  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		h.logger.Error("failed to create issue template", "error", err)
		renderErr(t("error.create_failed"))
		return
	}

	http.Redirect(w, r, "/issue-templates", http.StatusSeeOther)
}

// IssueTemplateDetail handles GET /issue-templates/{key}.
func (h *UIHandler) IssueTemplateDetail(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()
	key := r.PathValue("key")

	res, err := h.xoluClient.OQL(ctx,
		"SELECT * FROM "+issueTemplatesEntity+" WHERE key = '"+escapeOQL(key)+"' AND deleted = 0 ORDER BY version DESC")
	if err != nil || res == nil || len(res.Data) == 0 {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("issues.templates.not_found"))
		return
	}

	var versions []IssueTemplate
	var current *IssueTemplate
	for _, row := range res.Data {
		v := issueTemplateFromRow(row)
		versions = append(versions, v)
		if v.IsCurrent && current == nil {
			c := v
			current = &c
		}
	}
	if current == nil {
		current = &versions[0]
	}

	assetTypes, assignments := h.loadAssignments(ctx)

	data := IssueTemplateDetailData{
		Key:         key,
		Name:        current.Name,
		Description: current.Description,
		Version:     current.Version,
		Fields:      current.Fields,
		Versions:    versions,
		AssetTypes:  assetTypes,
		Assigned:    assignments,
		CSRFToken:   h.GetCSRFToken(w, r),
	}
	h.page.Render(w, r, t("issues.templates.detail_title")+": "+current.Name, "/issues", IssueTemplateDetailPage(data, t))
}

// TemplateAssetType is one asset type row in the assignment form.
type TemplateAssetType struct {
	ID   int64
	Name string
}

// loadAssignments loads every asset type and the template key currently
// assigned to each (map keyed by asset type id).
func (h *UIHandler) loadAssignments(ctx context.Context) ([]TemplateAssetType, map[int64]string) {
	var types []TemplateAssetType
	if res, err := h.xoluClient.OQL(ctx, "SELECT * FROM asset_types ORDER BY name"); err == nil && res != nil {
		for _, row := range res.Data {
			types = append(types, TemplateAssetType{ID: toInt64(row["id"]), Name: toString(row["name"])})
		}
	}
	assignments := map[int64]string{}
	if res, err := h.xoluClient.OQL(ctx, "SELECT * FROM "+issueTemplateAssignmentsEntity); err == nil && res != nil {
		for _, row := range res.Data {
			assignments[toInt64(row["asset_type_id"])] = toString(row["template_key"])
		}
	}
	return types, assignments
}

// IssueTemplateEdit handles GET /issue-templates/{key}/edit -- the editor
// preloaded with the current version's fields.
func (h *UIHandler) IssueTemplateEdit(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()
	key := r.PathValue("key")

	current := h.loadCurrentTemplate(ctx, key)
	if current == nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("issues.templates.not_found"))
		return
	}

	data := IssueTemplateFormData{
		IsEdit:      true,
		Key:         key,
		Name:        current.Name,
		Description: current.Description,
		Fields:      current.Fields,
		FieldTypes:  templateFieldTypes(),
		CSRFToken:   h.GetCSRFToken(w, r),
	}
	h.page.Render(w, r, t("issues.templates.edit_title")+": "+current.Name, "/issues", IssueTemplateFormPage(data, t))
}

// IssueTemplateUpdate handles POST /issue-templates/{key}/edit -- writes a
// new version (current version bumped) rather than editing in place.
func (h *UIHandler) IssueTemplateUpdate(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()
	key := r.PathValue("key")
	csrfToken := h.GetCSRFToken(w, r)

	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	fields := parseTemplateFieldsFromForm(r)

	renderErr := func(msg string) {
		data := IssueTemplateFormData{
			IsEdit:      true,
			Key:         key,
			Name:        name,
			Description: description,
			Fields:      fields,
			FieldTypes:  templateFieldTypes(),
			CSRFToken:   csrfToken,
			Error:       msg,
		}
		_ = h.page.Render(w, r, t("issues.templates.edit_title"), "/issues", IssueTemplateFormPage(data, t))
	}

	if name == "" {
		renderErr(t("issues.templates.error.name_required"))
		return
	}

	current := h.loadCurrentTemplate(ctx, key)
	if current == nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("issues.templates.not_found"))
		return
	}

	_, err := h.xoluClient.Create(ctx, issueTemplatesEntity, map[string]any{
		"key":         key,
		"name":        name,
		"description": description,
		"version":     current.Version + 1,
		"fields":      templateFieldsToValue(fields),
		"is_current":  1,
		"deleted":     0,
		"created_at":  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		h.logger.Error("failed to create new issue template version", "error", err)
		renderErr(t("error.create_failed"))
		return
	}
	if _, err := h.xoluClient.Patch(ctx, issueTemplatesEntity, current.ID, map[string]any{"is_current": 0}); err != nil {
		h.logger.Error("failed to demote previous issue template version", "error", err)
	}

	http.Redirect(w, r, "/issue-templates/"+key, http.StatusSeeOther)
}

// IssueTemplateAssign handles POST /issue-templates/{key}/assign -- upserts
// one assignment per checked asset type, clearing this template from any
// unchecked type it was previously assigned to.
func (h *UIHandler) IssueTemplateAssign(w http.ResponseWriter, r *http.Request) {
	t := h.translateFunc()
	ctx := r.Context()
	key := r.PathValue("key")

	current := h.loadCurrentTemplate(ctx, key)
	if current == nil {
		h.page.RenderError(w, http.StatusNotFound, t("error.not_found"), t("issues.templates.not_found"))
		return
	}

	if err := r.ParseForm(); err != nil {
		h.page.RenderError(w, http.StatusBadRequest, t("error.bad_request"), t("error.invalid_form_data"))
		return
	}

	assetTypes, assignments := h.loadAssignments(ctx)
	now := time.Now().UTC().Format(time.RFC3339)

	for _, at := range assetTypes {
		checked := r.FormValue("asset_type_"+strconv.FormatInt(at.ID, 10)) != ""
		existingKey, has := assignments[at.ID]
		switch {
		case checked && !has:
			if _, err := h.xoluClient.Create(ctx, issueTemplateAssignmentsEntity, map[string]any{
				"asset_type_id": at.ID,
				"template_key":  key,
				"updated_at":    now,
			}); err != nil {
				h.logger.Error("failed to create template assignment", "asset_type_id", at.ID, "error", err)
			}
		case checked && has && existingKey != key:
			if res, err := h.xoluClient.OQL(ctx,
				"SELECT id FROM "+issueTemplateAssignmentsEntity+" WHERE asset_type_id = "+strconv.FormatInt(at.ID, 10)); err == nil && res != nil && len(res.Data) > 0 {
				id := toInt64(res.Data[0]["id"])
				if _, err := h.xoluClient.Patch(ctx, issueTemplateAssignmentsEntity, id, map[string]any{"template_key": key, "updated_at": now}); err != nil {
					h.logger.Error("failed to update template assignment", "asset_type_id", at.ID, "error", err)
				}
			}
		case !checked && has && existingKey == key:
			if res, err := h.xoluClient.OQL(ctx,
				"SELECT id FROM "+issueTemplateAssignmentsEntity+" WHERE asset_type_id = "+strconv.FormatInt(at.ID, 10)); err == nil && res != nil && len(res.Data) > 0 {
				id := toInt64(res.Data[0]["id"])
				if err := h.xoluClient.Delete(ctx, issueTemplateAssignmentsEntity, id); err != nil {
					h.logger.Error("failed to clear template assignment", "asset_type_id", at.ID, "error", err)
				}
			}
		}
	}

	http.Redirect(w, r, "/issue-templates/"+key, http.StatusSeeOther)
}

// IssueTemplateDelete handles POST /issue-templates/{key}/delete -- soft
// delete on every version plus removal of the key's assignments.
func (h *UIHandler) IssueTemplateDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.PathValue("key")

	if res, err := h.xoluClient.OQL(ctx,
		"SELECT id FROM "+issueTemplatesEntity+" WHERE key = '"+escapeOQL(key)+"' AND deleted = 0"); err == nil && res != nil {
		for _, row := range res.Data {
			id := toInt64(row["id"])
			if _, err := h.xoluClient.Patch(ctx, issueTemplatesEntity, id, map[string]any{"deleted": 1, "is_current": 0}); err != nil {
				h.logger.Error("failed to delete issue template version", "id", id, "error", err)
			}
		}
	}
	if res, err := h.xoluClient.OQL(ctx,
		"SELECT id FROM "+issueTemplateAssignmentsEntity+" WHERE template_key = '"+escapeOQL(key)+"'"); err == nil && res != nil {
		for _, row := range res.Data {
			id := toInt64(row["id"])
			if err := h.xoluClient.Delete(ctx, issueTemplateAssignmentsEntity, id); err != nil {
				h.logger.Error("failed to delete template assignment", "id", id, "error", err)
			}
		}
	}

	http.Redirect(w, r, "/issue-templates", http.StatusSeeOther)
}

// ── Pages ─────────────────────────────────────────────────────────────────────

// IssueTemplatesListData holds data for the template list page.
type IssueTemplatesListData struct {
	Templates []IssueTemplate
}

// IssueTemplatesListPage renders the template list.
func IssueTemplatesListPage(data IssueTemplatesListData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	items := make([]SimpleListItem, len(data.Templates))
	for i, tpl := range data.Templates {
		subtitle := tpl.Description
		if subtitle == "" {
			subtitle = t("issues.templates.version") + " " + strconv.Itoa(tpl.Version)
		}
		items[i] = SimpleListItem{
			Href:     "/issue-templates/" + tpl.Key,
			Title:    tpl.Name,
			Subtitle: subtitle,
			Badge:    Badge("v"+strconv.Itoa(tpl.Version), "info"),
		}
	}
	return func(b *mi.Builder) mi.Node {
		return b.Div(mi.Class("space-y-6"),
			b.Div(mi.Class("flex items-center justify-between"),
				b.Div(mi.Class("flex items-center gap-3"),
					b.A(mi.Href("/issues"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
						b.Span(mi.Class("material-icons"), "arrow_back"),
					),
					b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), t("issues.templates.list_title")),
				),
				b.A(mi.Href("/issue-templates/new"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
					t("issues.templates.new"),
				),
			),
			SimpleListCard(items, t("issues.templates.empty"))(b),
		)
	}
}

// IssueTemplateFormData holds data for the create/edit template form.
type IssueTemplateFormData struct {
	IsEdit      bool
	Key         string
	Name        string
	Description string
	Fields      []TemplateField
	FieldTypes  []string
	CSRFToken   string
	Error       string
}

// IssueTemplateFormPage renders the template create/edit form with the same
// structured row editor as asset types.
func IssueTemplateFormPage(data IssueTemplateFormData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	title := t("issues.templates.new")
	action := "/issue-templates"
	if data.IsEdit {
		title = t("issues.templates.edit_title")
		action = "/issue-templates/" + data.Key + "/edit"
	}
	fieldTypes := data.FieldTypes
	if len(fieldTypes) == 0 {
		fieldTypes = templateFieldTypes()
	}

	return func(b *mi.Builder) mi.Node {
		var fieldsChildren []interface{}
		if len(data.Fields) == 0 {
			fieldsChildren = append(fieldsChildren, b.P(mi.Class("text-sm text-gray-400 italic"), t("issues.templates.no_fields")))
		} else {
			for i, f := range data.Fields {
				fieldsChildren = append(fieldsChildren, templateFieldRow(b, strconv.Itoa(i), f, fieldTypes, t))
			}
		}

		return b.Div(mi.Class("max-w-3xl mx-auto space-y-6"),
			b.Div(mi.Class("flex items-center gap-3"),
				b.A(mi.Href("/issue-templates"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					b.Span(mi.Class("material-icons"), "arrow_back"),
				),
				b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), title),
			),
			func() mi.Node {
				if data.Error == "" {
					return b.Span()
				}
				return b.Div(mi.Class("px-3 py-2 rounded-lg bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-sm text-red-700 dark:text-red-400"), data.Error)
			}(),
			b.Form(mi.Method("POST"), mi.Action(action), mi.Class("space-y-6"),
				b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
				b.Div(mi.Class("bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-6 space-y-4"),
					b.Div(
						b.Label(mi.For("name"), mi.Class("block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1"), t("issues.templates.name")),
						b.Input(mi.Type("text"), mi.ID("name"), mi.Name("name"), mi.Value(data.Name), mi.Required(),
							mi.Class("w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 focus:ring-2 focus:ring-indigo-500"),
							mi.Placeholder(t("issues.templates.name_placeholder")),
						),
					),
					b.Div(
						b.Label(mi.For("description"), mi.Class("block text-sm font-medium text-gray-700 dark:text-gray-300 mb-1"), t("issues.templates.description")),
						b.Textarea(mi.ID("description"), mi.Name("description"), mi.Rows(3),
							mi.Class("w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 text-gray-900 dark:text-gray-100 focus:ring-2 focus:ring-indigo-500"),
							b.Text(data.Description),
						),
					),
				),
				b.Div(mi.Class("bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700 p-6"),
					b.Div(mi.Class("flex items-center justify-between mb-4"),
						b.Div(
							b.H2(mi.Class("text-lg font-semibold text-gray-900 dark:text-white"), t("issues.templates.fields")),
							b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("issues.templates.fields_hint")),
						),
						b.Button(mi.Type("button"), mi.ID("add-tpl-field"),
							mi.Class("inline-flex items-center gap-1 px-3 py-1.5 text-sm border border-indigo-300 text-indigo-600 rounded-lg hover:bg-indigo-50"),
							b.Span(mi.Class("material-icons text-base"), "add"),
							t("issues.templates.add_field"),
						),
					),
					b.Div(append([]interface{}{mi.ID("tpl-fields-list"), mi.Class("space-y-2")}, fieldsChildren...)...),
				),
				b.Div(mi.Class("flex items-center justify-between pt-2"),
					b.A(mi.Href("/issue-templates"), mi.Class("px-4 py-2 text-sm text-gray-600 dark:text-gray-400 hover:text-gray-800"),
						t("action.cancel"),
					),
					b.Button(mi.Type("submit"), mi.Class("px-6 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium text-sm"),
						conditionalButtonText(data.IsEdit, t("issues.templates.save_changes"), t("issues.templates.create")),
					),
				),
			),
			b.Script(mi.Raw(issueTemplateEditorScript(fieldTypes, t("field.required")))),
		)
	}
}

// templateFieldRow renders one editable field row.
func templateFieldRow(b *mi.Builder, idx string, f TemplateField, fieldTypes []string, t func(key string, args ...any) string) mi.Node {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	typeOpts := make([]interface{}, len(fieldTypes))
	for i, ft := range fieldTypes {
		if ft == f.Type {
			typeOpts[i] = b.Option(mi.Value(ft), mi.Attr("selected", ""), b.Text(ft))
		} else {
			typeOpts[i] = b.Option(mi.Value(ft), b.Text(ft))
		}
	}
	var reqCheck mi.Node
	if f.Required {
		reqCheck = b.Input(mi.Type("checkbox"), mi.Name("tpl_field_required_"+idx), mi.Value("1"), mi.Attr("checked", ""))
	} else {
		reqCheck = b.Input(mi.Type("checkbox"), mi.Name("tpl_field_required_"+idx), mi.Value("1"))
	}
	return b.Div(mi.Class("flex items-center gap-2 p-3 bg-gray-50 dark:bg-gray-700 rounded-lg"),
		b.Input(mi.Type("hidden"), mi.Name("tpl_field_idx[]"), mi.Value(idx)),
		b.Input(mi.Type("text"), mi.Name("tpl_field_key[]"), mi.Value(f.Key),
			mi.Class("flex-1 min-w-0 px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"),
			mi.Required(),
		),
		b.Input(mi.Type("text"), mi.Name("tpl_field_label[]"), mi.Value(f.Label),
			mi.Class("flex-1 min-w-0 px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100"),
			mi.Required(),
		),
		b.Select(append([]interface{}{mi.Name("tpl_field_type[]"),
			mi.Class("px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100")},
			typeOpts...)...),
		b.Label(mi.Class("flex items-center gap-1 text-xs text-gray-600 dark:text-gray-300"),
			reqCheck,
			b.Text(t("field.required")),
		),
		b.Button(mi.Type("button"), mi.Class("text-red-400 hover:text-red-600 ml-1"),
			mi.Attr("onclick", "this.closest('div').remove()"),
			b.Span(mi.Class("material-icons text-lg"), "remove_circle_outline"),
		),
	)
}

func issueTemplateEditorScript(fieldTypes []string, requiredLabel string) string {
	types := fmt.Sprintf("%q", strings.Join(fieldTypes, ","))
	return `
(function() {
  function makeFieldRow(prefix, fieldTypes, requiredLabel) {
    var idx = 'x' + Date.now() + Math.floor(Math.random() * 1000);
    var row = document.createElement('div');
    row.className = 'flex items-center gap-2 p-3 bg-gray-50 dark:bg-gray-700 rounded-lg';
    row.innerHTML =
      '<input type="hidden" name="' + prefix + '_field_idx[]" value="' + idx + '">' +
      '<input type="text" name="' + prefix + '_field_key[]" placeholder="key (e.g. serial_no)"' +
      ' class="flex-1 min-w-0 px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100" required>' +
      '<input type="text" name="' + prefix + '_field_label[]" placeholder="Label"' +
      ' class="flex-1 min-w-0 px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100" required>' +
      '<select name="' + prefix + '_field_type[]"' +
      ' class="px-2 py-1.5 text-sm border border-gray-300 dark:border-gray-600 rounded bg-white dark:bg-gray-800 text-gray-900 dark:text-gray-100">' +
      fieldTypes.map(function(t){ return '<option value="' + t + '">' + t + '</option>'; }).join('') +
      '</select>' +
      '<label class="flex items-center gap-1 text-xs text-gray-600 dark:text-gray-300">' +
      '<input type="checkbox" name="' + prefix + '_field_required_' + idx + '" value="1"> ' + requiredLabel + '</label>' +
      '<button type="button" class="text-red-400 hover:text-red-600 ml-1" onclick="this.closest(\'div\').remove()">' +
      '<span class="material-icons text-lg">remove_circle_outline</span></button>';
    return row;
  }
  var typeArr = ` + types + `.split(',');
  var addBtn = document.getElementById('add-tpl-field');
  if (addBtn) {
    addBtn.addEventListener('click', function() {
      document.getElementById('tpl-fields-list').appendChild(makeFieldRow('tpl', typeArr, ` + fmt.Sprintf("%q", requiredLabel) + `));
    });
  }
})();
`
}

// IssueTemplateDetailData holds data for the template detail page.
type IssueTemplateDetailData struct {
	Key         string
	Name        string
	Description string
	Version     int
	Fields      []TemplateField
	Versions    []IssueTemplate
	AssetTypes  []TemplateAssetType
	Assigned    map[int64]string
	CSRFToken   string
}

// IssueTemplateDetailPage renders a template's current fields, its version
// history, and the asset-type assignment form.
func IssueTemplateDetailPage(data IssueTemplateDetailData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	return func(b *mi.Builder) mi.Node {
		var fieldRows []interface{}
		if len(data.Fields) == 0 {
			fieldRows = append(fieldRows, b.P(mi.Class("text-sm text-gray-400 italic"), t("issues.templates.no_fields")))
		} else {
			for _, f := range data.Fields {
				label := f.Label
				if label == "" {
					label = f.Key
				}
				required := ""
				if f.Required {
					required = " *"
				}
				fieldRows = append(fieldRows, b.Div(mi.Class("flex items-center justify-between px-4 py-2 border-b border-gray-100 dark:border-gray-700 last:border-b-0"),
					b.Div(mi.Class("text-sm text-gray-900 dark:text-white"), label+required),
					b.Div(mi.Class("text-xs text-gray-500 dark:text-gray-400"), f.Key+" · "+f.Type),
				))
			}
		}

		var versionRows []interface{}
		for _, v := range data.Versions {
			badge := ""
			if v.IsCurrent {
				badge = " (" + t("issues.templates.current") + ")"
			}
			versionRows = append(versionRows, b.Div(mi.Class("px-4 py-2 border-b border-gray-100 dark:border-gray-700 last:border-b-0 text-sm text-gray-700 dark:text-gray-300"),
				t("issues.templates.version")+" "+strconv.Itoa(v.Version)+badge,
			))
		}

		var assignRows []interface{}
		if len(data.AssetTypes) == 0 {
			assignRows = append(assignRows, b.P(mi.Class("text-sm text-gray-400 italic"), t("issues.templates.no_asset_types")))
		} else {
			for _, at := range data.AssetTypes {
				assignedKey := data.Assigned[at.ID]
				checked := assignedKey == data.Key
				chk := b.Input(mi.Type("checkbox"), mi.Name("asset_type_"+strconv.FormatInt(at.ID, 10)), mi.Value("1"),
					mi.Class("rounded border-gray-300 text-indigo-600"))
				if checked {
					chk = b.Input(mi.Type("checkbox"), mi.Name("asset_type_"+strconv.FormatInt(at.ID, 10)), mi.Value("1"),
						mi.Attr("checked", ""), mi.Class("rounded border-gray-300 text-indigo-600"))
				}
				row := []interface{}{
					b.Label(mi.Class("flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300"), chk, b.Text(at.Name)),
				}
				if assignedKey != "" && assignedKey != data.Key {
					row = append(row, b.Span(mi.Class("text-xs text-amber-600"), assignedKey))
				}
				assignRows = append(assignRows, b.Div(append([]interface{}{mi.Class("px-4 py-2 border-b border-gray-100 dark:border-gray-700 last:border-b-0")}, row...)...))
			}
		}

		return b.Div(mi.Class("max-w-3xl mx-auto space-y-6"),
			b.Div(mi.Class("flex items-center justify-between"),
				b.Div(mi.Class("flex items-center gap-3"),
					b.A(mi.Href("/issue-templates"), mi.Class("text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
						b.Span(mi.Class("material-icons"), "arrow_back"),
					),
					b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), data.Name),
					Badge("v"+strconv.Itoa(data.Version), "info")(b),
				),
				b.A(mi.Href("/issue-templates/"+data.Key+"/edit"), mi.Class("px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg font-medium hover:bg-gray-50 dark:hover:bg-gray-700"),
					t("issues.templates.edit_title"),
				),
			),
			func() mi.Node {
				if data.Description == "" {
					return b.Span()
				}
				return b.Div(mi.Class("text-sm text-gray-700 dark:text-gray-300"), data.Description)
			}(),
			b.Div(mi.Class("bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700"),
				b.H2(mi.Class("text-lg font-semibold text-gray-900 dark:text-white px-4 py-3 border-b border-gray-100 dark:border-gray-700"), t("issues.templates.fields")),
				b.Div(fieldRows...),
			),
			b.Div(mi.Class("bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700"),
				b.H2(mi.Class("text-lg font-semibold text-gray-900 dark:text-white px-4 py-3 border-b border-gray-100 dark:border-gray-700"), t("issues.templates.versions")),
				b.Div(versionRows...),
			),
			b.Form(mi.Method("POST"), mi.Action("/issue-templates/"+data.Key+"/assign"), mi.Class("bg-white dark:bg-gray-800 rounded-lg shadow-sm border border-gray-200 dark:border-gray-700"),
				b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
				b.Div(mi.Class("px-4 py-3 border-b border-gray-100 dark:border-gray-700"),
					b.H2(mi.Class("text-lg font-semibold text-gray-900 dark:text-white"), t("issues.templates.assigned_types")),
					b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), t("issues.templates.assign_hint")),
				),
				b.Div(assignRows...),
				b.Div(mi.Class("px-4 py-3 flex justify-end"),
					b.Button(mi.Type("submit"), mi.Class("px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium text-sm"),
						t("issues.templates.assign"),
					),
				),
			),
			b.Form(mi.Method("POST"), mi.Action("/issue-templates/"+data.Key+"/delete"),
				b.Input(mi.Type("hidden"), mi.Name("_csrf"), mi.Value(data.CSRFToken)),
				b.Button(mi.Type("submit"), mi.Attr("onclick", "return confirm('"+t("issues.templates.confirm_delete")+"')"),
					mi.Class("px-4 py-2 border border-red-300 dark:border-red-700 text-red-600 dark:text-red-400 rounded-lg font-medium hover:bg-red-50 dark:hover:bg-red-900/20"),
					t("issues.templates.delete"),
				),
			),
		)
	}
}
