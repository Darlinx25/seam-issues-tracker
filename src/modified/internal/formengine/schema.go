package formengine

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ha1tch/queryfy"
	"github.com/ha1tch/queryfy/builders/jsonschema"

	"github.com/ha1tch/seam-ui/internal/querypicker"
)

// Section is one field-group within a tab row. FORM_ENGINE_SPEC.md §3.4.
type Section struct {
	ID          string
	Label       string
	Layout      string // "1-col" | "2-col" | "2-col-left" | "2-col-right", default "1-col"
	Fields      []Field
	Attachable  bool         // stage 8 (tray) territory — captured now, not yet consumed
	VisibleWhen *VisibleWhen // §3b.3 — nil means always visible
}

// Row is one horizontal band of 1–3 sections within a tab.
// FORM_ENGINE_SPEC.md §3.8.
type Row struct {
	Sections    []Section
	Widths      []string     // parallel to Sections; may be shorter (missing = auto-split). "full"|"1/2"|"1/3"|"2/3"|"1/4"|"3/4"
	VisibleWhen *VisibleWhen // §3b.3 — nil means always visible
}

// DetailPanel shows read-only related-entity data within a tab, loaded
// via htmx once the tab is activated. FORM_ENGINE_SPEC.md §3.5. Never
// generates data on the parent form's own submit — see §3.7.
type DetailPanel struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Source      string   `json:"source"` // URL, may contain "{id}"
	Columns     []string `json:"columns"`
	Empty       string   `json:"empty"`    // i18n key for the empty state
	Position    string   `json:"position"` // "bottom" | "right" | "inline", default "bottom"
	Collapsible bool     `json:"collapsible"`
	Load        string   `json:"load"` // "eager" | "lazy", default "eager"
}

// EmbedForm is a sub-form that acts on a related entity without leaving
// the parent form. FORM_ENGINE_SPEC.md §3.6. Has its own submit cycle,
// independent of the parent form's — see §3.7.
type EmbedForm struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Endpoint      string `json:"endpoint"`       // URL, may contain "{id}"
	Method        string `json:"method"`         // "post" | "put" | "delete"
	Trigger       string `json:"trigger"`        // "button" | "inline" | "auto"
	TriggerLabel  string `json:"trigger_label"`  // i18n key, used when Trigger == "button"
	Mode          string `json:"mode"`           // "modal2" | "inline" | "drawer", default "modal2"
	Schema        string `json:"schema"`         // sub-form schema name; empty = action-only, no fields
	Confirm       string `json:"confirm"`        // i18n key; empty = no confirmation prompt
	RefreshDetail string `json:"refresh_detail"` // ID of a DetailPanel in the same tab to reload after a successful submit
}

// Tab is one tab within a tabbed form. FORM_ENGINE_SPEC.md §3.3.
// Details and Embeds only render when RenderContext.IsEdit is true and,
// for Embeds, the tab isn't in RenderContext.ReadOnlyTabs — §3.7.
type Tab struct {
	ID          string
	Label       string
	Rows        []Row
	Details     []DetailPanel
	Embeds      []EmbedForm
	VisibleWhen *VisibleWhen // §3b.3 — nil means always visible
}

// Field is one property of a schema, with its standard JSON Schema
// attributes and the x-seam-* extensions relevant to rendering it.
// FORM_ENGINE_SPEC.md §2.1 (extension table), §4 (x-seam-input), §5
// (x-seam-source), §6 (x-seam-help).
type Field struct {
	Name     string
	Title    string
	JSONType string // "string" | "integer" | "number" | "boolean" | "array"
	Format   string // JSON Schema "format": "email" | "uri" | "date" | "date-time" | "time" | "tel" | ...
	Enum     []string
	Required bool

	// x-seam-* extensions relevant to the stage-1 MVP widget set.
	// Deferred to later stages: x-seam-fk-popover (§7), x-seam-relation
	// (§8).
	XSeamInput     string // forces a widget; empty = infer per §4.1
	XSeamSource    string // "entity:<name>", for dynamic select options (§5)
	XSeamHelp      string
	XSeamHelpStyle string // "inline" | "tooltip" (§6)
	XSeamRows      int    // textarea row count, default 3 (§4.2)
	// XSeamAutocomplete sets the rendered <input>'s autocomplete
	// attribute verbatim (e.g. "current-password", "new-password") --
	// added alongside the "password" widget (MOD-8, 2026-08-03): the
	// distinction meaningfully changes browser password-manager
	// behaviour (current-password enables autofill of a saved
	// credential; new-password suggests a generated one and actively
	// avoids autofilling the old one), so a single hardcoded value for
	// every password-type field would be wrong for at least one of
	// them. Empty = no autocomplete attribute at all, not "off" --
	// browsers don't reliably honour autocomplete="off" for password
	// fields anyway, so omitting the attribute and doing nothing is the
	// honest default rather than a non-functional pretence of one.
	XSeamAutocomplete string

	XSeamVisibleWhen  *VisibleWhen // §3b.2 — nil means always visible
	XSeamHiddenReject bool         // §15.2 — true = reject (422) a submitted value when hidden, rather than silently discard it

	XSeamFKPopover string // §7 — URL base for the FK-preview popover button; empty = no popover
	XSeamRelation  string // §8 — "contains" | "uses", only relevant when XSeamInput == "asset-picker"

	// XSeamSearchFields/XSeamDisplayFields extend XSeamSource's
	// entity:<name> convention for the "asset-picker" widget (T-19,
	// 2026-08-04): which fields the backend search endpoint matches
	// the query against, and which fields render per result row.
	// Deliberately generic, not asset-specific, despite the widget's
	// own name -- a future picker field for a different entity
	// (Sensors, say) looks identical in shape, just pointing at a
	// different entity:<name> with its own field lists. One widget
	// implementation, one generalised backend endpoint, not a new
	// widget type per entity.
	XSeamSearchFields  []string
	XSeamDisplayFields []string

	// XSeamSelector (T-97), only meaningful for XSeamInput == "any-picker":
	// the key of a server-side Selector source (internal/ui/selector.go,
	// e.g. "users"). When set, the widget adds a Browse button that opens
	// the Selector dialog (GET /selector/<key>) and fills the picker from
	// the 'seam:selector-chosen' event. Empty = no Browse button, search
	// box only. The server's registry decides whether the key exists; an
	// unknown key just shows the dialog's 400 text, never a different entity.
	XSeamSelector string

	// XSeamNaming is "indexed" to force indexed field names on a repeatable
	// (blocks[0][name]); see repeatable_indexed.go. Empty = automatic: flat
	// {field}_{item}[] names unless the repeatable holds a nested repeatable
	// or an any-picker.
	XSeamNaming string

	// XSeamPlaceholder is the placeholder text of a text-like, number,
	// textarea or any-picker field, and the empty-option text of a select.
	// Inside a repeatable row it also names the cell. When it is empty, a
	// repeatable row falls back to the item's own title (see rowLabel).
	XSeamPlaceholder string

	// XSeamLayout (on a repeatable): "row" (default) keeps every cell of a
	// row on one line; "form" lays each row out as a card with its cells in a
	// responsive grid, each under its label.
	XSeamLayout string

	// XSeamBrowse (on an any-picker with x-seam-selector): how the Browse
	// control looks. "button" (default) is an outlined button with an icon and
	// the label; "icon" a small icon-only button; "link" the old text link.
	XSeamBrowse string

	// XSeamHighlight (on any-picker, icon-picker, asset-picker): how the
	// matched part of a result is shown. "color" (default) is bold in the
	// theme's primary colour; "background" a translucent marker; "none" plain.
	XSeamHighlight string

	// XSeamAddLabel, XSeamAddIcon and XSeamAddStyle (on a repeatable): the add
	// button. Label replaces the default text (literal text, not an i18n key);
	// Icon is a Material Icons name; Style is "outline" (default), "filled",
	// "link" or "icon" (icon only; the label becomes its tooltip).
	XSeamAddLabel string
	XSeamAddIcon  string
	XSeamAddStyle string

	// XSeamRemoveIcon, XSeamRemoveLabel and XSeamRemoveColor (on a repeatable):
	// the remove button of each row. A Label turns the icon-only button into
	// icon plus text; Color is a name from repColors or a #rrggbb value.
	XSeamRemoveIcon  string
	XSeamRemoveLabel string
	XSeamRemoveColor string

	// XSeamAccent (on a repeatable): the colour of its rows, add button and
	// nested rule, a name from repColors or a #rrggbb value. Empty = the theme.
	XSeamAccent string

	// XSeamReorder (on a repeatable): every row gets a drag handle and the rows
	// can be reordered by pointer or keyboard (see repeatable_reorder.go). Off
	// unless set. Indexed repeatables also post a name[i][_pos] per row, so the
	// server keeps the order the user left them in.
	XSeamReorder bool

	// MinItems and MaxItems (on a repeatable): the bounds on its rows, from the
	// standard JSON Schema keywords minItems and maxItems (see
	// repeatable_bounds.go). MinItems 0 means none; MaxItems 0 means unlimited.
	MinItems int
	MaxItems int

	// XSeamOptionIcons (on a select): option value -> Material icon name, shown
	// beside the option's text by SeamSelect. XSeamIconPosition is "left" (the
	// default, also written as "") or "right". See select_icons.go.
	XSeamOptionIcons    map[string]string
	XSeamIconPosition   string
	// XSeamOptionDisabled (on a select): option values that render disabled.
	// The option is visible but cannot be selected. Used to advertise
	// channels/features not yet implemented without hiding them.
	XSeamOptionDisabled map[string]bool

	// QueryPicker is the validated specification of a "query-picker" field
	// (docs/QUERY_PICKER.md): its OQL query, its value and label columns and
	// the columns of the dropdown. Nil for every other widget.
	QueryPicker *querypicker.Spec

	// XSeamDropdown and XSeamDetail (on a query-picker): the dropdown layout,
	// "table", "rows" or "" (chosen by the field's width), and whether the
	// other columns of the chosen row are shown under the box.
	XSeamDropdown string
	XSeamDetail   bool

	// render-time state of a repeatable, set by withBoundLocks, never by a schema
	addLocked    bool
	removeLocked bool

	// rowLabel is set by the repeatable renderers on the copy of each item
	// field they render: the text that tells the user what the cell is
	// (x-seam-placeholder, else an i18n key, else the item title). Widgets
	// show it as the placeholder and the accessible name of the cell.
	rowLabel string

	// indexedNaming / indexDepth are set by the renderer on the copy of a
	// nested repeatable it renders inside an indexed row.
	indexedNaming bool
	indexDepth    int

	// XSeamOptionGroups, when true on a "select" field, groups f.Enum's
	// options into <optgroup> sections inferred from each value's own
	// "category_unit" naming convention (e.g. "distance_kilometers" groups
	// under "distance") -- opt-in per field, not automatic for every
	// underscore-containing enum, since several existing fields
	// (connectivity_tier's "qr_only"/"lora_mesh"/etc.) have underscores
	// with no such two-level meaning and must render as a flat list
	// exactly as before. T-36, 2026-08-02: service_configs.measure was
	// the field that surfaced the need -- distance/time/usage/condition
	// are genuinely different kinds of "measure", not one flat list.
	XSeamOptionGroups bool

	// DecimalPrecision/DecimalScale surface xolu's own "format":"decimal"
	// schema-layer fixed-point support (T-10, docs/JSON_SCHEMA.md in
	// xolu: type "string" + format "decimal", value travels as a JSON
	// string like "10.99" to avoid the float64 round-trip). Populated
	// whenever Format == "decimal"; zero otherwise. DecimalScale governs
	// the "decimal" widget's pattern/step; DecimalPrecision (total
	// significant digits) is exposed for a future max-length hint but
	// not enforced client-side today -- xolu's own write path is the
	// actual validation authority (docs/JSON_SCHEMA.md: "Values that
	// would require truncation are rejected rather than silently
	// rounded"), the widget only needs to not visually invite a bad
	// value.
	DecimalPrecision int
	DecimalScale     int

	// XSeamUnit is a static unit-of-measure suffix shown beside a
	// field's control (T-10's "Unidad de medida" extension). MVP scope,
	// deliberately narrow: a plain display string ("°C", "kg", "psi"),
	// no i18n-labeled unit table, no conversion logic -- those are the
	// fuller vision T-35 names and explicitly defers ("filed rather
	// than decided unilaterally"). This solves T-35's actual, narrower
	// problem (temp_min/temp_max bake their unit into the translatable
	// title text instead of a structured, independently-addressable
	// concept) without building the larger system nothing has asked
	// for yet. Applied uniformly in renderFieldRow (renderer.go), not
	// duplicated per-widget -- works for any widget's control, not just
	// number/decimal. Migrating asset.json's temp_min/temp_max to
	// actually use this is a separate, deliberate decision left
	// untouched here (schema content, not formengine capability).
	XSeamUnit string

	// XSeamInputMask is a formatting mask applied client-side to a
	// text-like field as the operator types (T-10's "Máscara de
	// ingreso" extension, docs/ORDER_ENGINE_INTEGRATION_NOTES.md §4).
	// Syntax, deliberately small and hand-rolled rather than a vendored
	// masking library (renderTextLike's own inline script implements
	// it -- see that function's comment for why): '9' = digit, 'A' =
	// letter (auto-uppercased), 'a' = letter (any case), '*' =
	// alphanumeric, any other character is a literal the script
	// auto-inserts (e.g. "999-999-9999" for a phone-shaped field, or
	// "AA-9999" for a plate-shaped one). Empty = no masking, the
	// existing, default, unaffected behaviour. Purely a client-side
	// input aid -- xolu's own write-path validation (pattern/format)
	// remains the actual authority, exactly like the "decimal" widget's
	// pattern attribute above.
	XSeamInputMask string

	// XSeamERPCode is a pass-through identifier for an external ERP
	// system's own code for this field (T-10's fourth x-seam-*
	// extension). Deliberately inert at the formengine layer -- no
	// widget, no validation, no rendering -- since no ERP integration
	// exists yet to consume it (docs/ORDER_ENGINE_INTEGRATION_NOTES.md
	// §4: "probablemente una propiedad JSON Schema simple sin necesidad
	// de lógica especial"). Parsed and carried on Field so a schema
	// author can record the mapping now and a future integration reads
	// it without a second schema-format change.
	XSeamERPCode string

	// XSeamCustomValidator names a validator registered in
	// FormEngine.CustomValidators (T-10's "custom validation"
	// extension point, engine.go). Empty = no custom validation, the
	// default and overwhelming-majority case. Checked by
	// ValidateCustomFields (renderer.go), a separate pass a caller runs
	// alongside queryfy's own compiled-schema validation -- not
	// injected into the compiled queryfy.Schema itself. See
	// CustomValidators' own doc comment (engine.go) for why: queryfy's
	// Custom() hook is a builder-API method that doesn't compose with
	// formengine's declarative jsonschema.FromJSON conversion path, and
	// reaching into a compiled Schema to inject a validator by field
	// path would mean depending on internals this package doesn't
	// otherwise touch. A real per-field injection point in queryfy
	// itself (if one exists or is added upstream) could replace this
	// with a cleaner mechanism later -- not attempted here.
	XSeamCustomValidator string

	// XSeamBarcodeFormat selects the linear-barcode symbology for the
	// "barcode" widget (T-10's EAN/UPC gap, distinct from the QR
	// support internal/qrcode already has). Values: "EAN13" (default
	// when empty -- the common retail symbology worldwide), "EAN8",
	// "UPC", "UPCE" -- exactly the formats JsBarcode's own vendored
	// ean-upc build supports (web/static/vendor/VENDOR.md, Asset 7).
	// A companion attribute rather than four separate widget names
	// (barcode-ean13, barcode-upc, ...) because it's the same widget
	// behaviour (text input + live SVG preview + validity check) with
	// one parameter varying, matching how XSeamInputMask/XSeamUnit are
	// companion attributes on existing widgets rather than new widget
	// identities.
	XSeamBarcodeFormat string

	// XSeamPanel (on a gps field): how the map panel opens. "dropdown" (the default) is a
	// panel under the pin button, aligned to its right edge; "wide" opens it at the full width
	// of the field, in the page flow, like the icon-picker's grid. The user can switch either
	// way with the panel's expand button.
	XSeamPanel string

	// XSeamHideLabel keeps a field's label for screen readers only (visually hidden): for a field
	// that a section title already names, such as the Assets form's photo gallery.
	XSeamHideLabel bool

	// Items is the row schema for array-typed widgets (document-links,
	// repeatable, §14) — the fields each row contains, parsed from the
	// JSON Schema "items.properties" object the same way the form's own
	// top-level properties are. nil for every non-array widget.
	Items []Field

	// I18nName carries a repeatable row item's original, un-rewritten
	// field name (e.g. "measure") for i18n key resolution, separate from
	// Name, which repeatableRow rewrites to the array-bracket submission
	// form (e.g. "service_configs_measure[]") before rendering. Without
	// this, resolveEnumOptionLabel builds an unresolvable key straight
	// from the mangled Name (found during T-18 Stage 9's asset.json
	// audit, 2026-08-01: service_configs.measure's enum labels were
	// structurally unable to resolve through any sensibly-named key).
	// Empty for every field outside a repeatable row, where Name is
	// already the field's true name and this is simply unused.
	I18nName string
}

// Form is a parsed schema, ready to render. Tabs is nil for the flat,
// no-tabs case (§3.10) — Fields is then rendered directly, in
// properties order. When Tabs is populated, Fields still holds every
// field (used to resolve section.fields lookups) but the renderer walks
// Tabs instead.
type Form struct {
	// Fields in the order they appear in the schema's "properties"
	// object. encoding/json does not preserve object key order through a
	// map[string]interface{} decode, so this is captured separately by
	// walking the raw token stream — see propertyOrder.
	Fields []Field

	// Tabs is nil unless x-seam-form.tabs is present and non-empty
	// (§3.1–§3.4, §3.8). nil means render Fields flat per §3.10.
	Tabs []Tab

	// Module and Name are x-seam-form.module / x-seam-form.name (§3.2),
	// the schema's own default identity for i18n key resolution
	// (I18N_SPEC.md §4.1). Both empty unless the schema declares them.
	// RenderContext.Module / RenderContext.Form (already present in the
	// engine since stage 1, unwired until T-13) override these per call
	// when non-empty — see resolveTitle/resolveHelp in i18n.go.
	Module string
	Name   string

	// Compiled is the queryfy-compiled schema, used for validating
	// submitted values. Not yet wired to anything — validation call sites
	// land with the handlers that use this package (stage 2 onward).
	Compiled queryfy.Schema
}

// ParseSchema parses a raw JSON Schema document (with x-seam-* extensions)
// into a Form. Exported 2026-07-28 (was parseSchema, package-private) so
// callers outside formengine -- specifically handlers needing Form.Compiled
// for validation before Render -- can reach it without a parallel,
// formengine-owned validation API duplicating queryfy's own interface.
// Form.Fields/Tabs are also now reachable this way, not just Compiled.
func ParseSchema(raw []byte) (*Form, error) {
	var doc struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("formengine: parsing schema: %w", err)
	}

	order, err := propertyOrder(raw)
	if err != nil {
		return nil, fmt.Errorf("formengine: reading property order: %w", err)
	}

	required := make(map[string]bool, len(doc.Required))
	for _, r := range doc.Required {
		required[r] = true
	}

	compiled, convErrs := jsonschema.FromJSON(raw, &jsonschema.Options{StoreUnknown: true})
	// convErrs mixes fatal problems with non-fatal warnings (queryfy's own
	// ConversionError.IsWarning, set when StrictMode=false and a feature
	// was skipped rather than rejected) -- StoreUnknown:true is exactly
	// the "skip and store as metadata" case, e.g. xolu's own
	// "format":"decimal" (T-10), which queryfy doesn't recognise as a
	// standard JSON Schema format and never will, by design (it's a
	// xolu-layer extension, not a JSON Schema one). Only real errors
	// should fail parsing; every prior format string used in this
	// codebase (email/uri/date/date-time/time/tel) happened to be one
	// queryfy already knows, so this distinction was previously
	// unexercised -- confirmed by TestParseSchema_DecimalDefaults
	// failing here before this fix, for exactly this reason, not a
	// hypothetical.
	for _, ce := range convErrs {
		if !ce.IsWarning {
			return nil, fmt.Errorf("formengine: compiling schema: %s", ce.Message)
		}
	}

	fields := make([]Field, 0, len(order))
	for _, name := range order {
		propRaw, ok := doc.Properties[name]
		if !ok {
			// propertyOrder and doc.Properties disagree — shouldn't
			// happen against valid JSON, but don't silently drop a
			// field if it somehow does.
			return nil, fmt.Errorf("formengine: property %q in document order but not in decoded properties", name)
		}
		f, err := parseField(name, propRaw)
		if err != nil {
			return nil, fmt.Errorf("formengine: field %q: %w", name, err)
		}
		f.Required = required[name]
		if f.Required && (inferWidget(f) == "repeatable" || f.JSONType == "array") && f.MinItems == 0 {
			f.MinItems = 1
		}
		fields = append(fields, f)
	}

	fieldsByName := make(map[string]Field, len(fields))
	for _, f := range fields {
		fieldsByName[f.Name] = f
	}
	tabs, err := parseTabs(raw, fieldsByName)
	if err != nil {
		return nil, err
	}

	module, name, err := parseFormIdentity(raw)
	if err != nil {
		return nil, err
	}

	return &Form{Fields: fields, Tabs: tabs, Module: module, Name: name, Compiled: compiled}, nil
}

// parseFormIdentity reads x-seam-form.module and x-seam-form.name (§3.2),
// the schema's own default {módulo}/{nombre_formulario} for i18n key
// resolution (I18N_SPEC.md §4.1). Both are optional and independent —
// a schema can declare one, both, or neither; RenderContext's own
// Module/Form fields can supply whichever the schema didn't, or override
// what it did.
func parseFormIdentity(raw []byte) (module, name string, err error) {
	var doc struct {
		XSeamForm *struct {
			Module string `json:"module"`
			Name   string `json:"name"`
		} `json:"x-seam-form"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", "", fmt.Errorf("formengine: reading x-seam-form identity: %w", err)
	}
	if doc.XSeamForm == nil {
		return "", "", nil
	}
	return doc.XSeamForm.Module, doc.XSeamForm.Name, nil
}

// rawSection is the wire shape of a section object, whether it appears
// directly under tab.sections or resolved via tab.section_defs.
type rawSection struct {
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	Layout      string          `json:"layout"`
	Fields      []string        `json:"fields"`
	Attachable  bool            `json:"attachable"`
	VisibleWhen json.RawMessage `json:"visible_when"`
}

// parseTabs reads x-seam-form.tabs (§3.1–§3.4, §3.8). Returns (nil, nil)
// when x-seam-form is absent or has no tabs — §3.10's flat rendering
// path, not an error.
func parseTabs(raw []byte, fieldsByName map[string]Field) ([]Tab, error) {
	var doc struct {
		XSeamForm *struct {
			Tabs []json.RawMessage `json:"tabs"`
		} `json:"x-seam-form"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("formengine: reading x-seam-form: %w", err)
	}
	if doc.XSeamForm == nil || len(doc.XSeamForm.Tabs) == 0 {
		return nil, nil
	}

	tabs := make([]Tab, 0, len(doc.XSeamForm.Tabs))
	for _, tabRaw := range doc.XSeamForm.Tabs {
		var t struct {
			ID       string            `json:"id"`
			Label    string            `json:"label"`
			Sections []json.RawMessage `json:"sections"`
			Rows     []struct {
				Sections    []string        `json:"sections"`
				Widths      []string        `json:"widths"`
				VisibleWhen json.RawMessage `json:"visible_when"`
			} `json:"rows"`
			SectionDefs []rawSection    `json:"section_defs"`
			Details     []DetailPanel   `json:"details"`
			Embeds      []EmbedForm     `json:"embeds"`
			VisibleWhen json.RawMessage `json:"visible_when"`
		}
		if err := json.Unmarshal(tabRaw, &t); err != nil {
			return nil, fmt.Errorf("formengine: parsing tab: %w", err)
		}

		var rows []Row
		if len(t.Rows) > 0 {
			// rows + section_defs form (§3.8): row.sections are string
			// IDs resolved against section_defs.
			defs := make(map[string]rawSection, len(t.SectionDefs))
			referenced := make(map[string]bool, len(t.SectionDefs))
			for _, d := range t.SectionDefs {
				defs[d.ID] = d
			}
			for _, r := range t.Rows {
				rowVW, err := parseVisibleWhen(r.VisibleWhen)
				if err != nil {
					return nil, fmt.Errorf("formengine: tab %q row: visible_when: %w", t.ID, err)
				}
				row := Row{Widths: r.Widths, VisibleWhen: rowVW}
				for _, sid := range r.Sections {
					def, ok := defs[sid]
					if !ok {
						return nil, fmt.Errorf("formengine: tab %q row references section_def %q, not defined in section_defs", t.ID, sid)
					}
					referenced[sid] = true
					sec, err := resolveSection(def, fieldsByName)
					if err != nil {
						return nil, fmt.Errorf("formengine: tab %q section %q: %w", t.ID, sid, err)
					}
					row.Sections = append(row.Sections, sec)
				}
				rows = append(rows, row)
			}
			// §15.1 (resolved: Option B) — reject the schema at load if
			// section_defs has an ID no row references. Catches dead
			// section definitions before they silently never render.
			for _, d := range t.SectionDefs {
				if !referenced[d.ID] {
					return nil, fmt.Errorf("formengine: tab %q: section_defs %q is not referenced by any row (§15.1)", t.ID, d.ID)
				}
			}
		} else if len(t.Sections) > 0 {
			// Plain sections form (§3.8 "Compatibilidad con sections
			// directo"): each section object is its own full-width row.
			for _, secRaw := range t.Sections {
				var def rawSection
				if err := json.Unmarshal(secRaw, &def); err != nil {
					return nil, fmt.Errorf("formengine: tab %q: parsing section: %w", t.ID, err)
				}
				sec, err := resolveSection(def, fieldsByName)
				if err != nil {
					return nil, fmt.Errorf("formengine: tab %q section %q: %w", t.ID, def.ID, err)
				}
				rows = append(rows, Row{
					Sections: []Section{sec},
					Widths:   []string{"full"},
				})
			}
		}

		for i := range t.Details {
			if t.Details[i].Position == "" {
				t.Details[i].Position = "bottom"
			}
			if t.Details[i].Load == "" {
				t.Details[i].Load = "eager"
			}
		}
		for i := range t.Embeds {
			if t.Embeds[i].Mode == "" {
				t.Embeds[i].Mode = "modal2"
			}
		}

		tabVW, err := parseVisibleWhen(t.VisibleWhen)
		if err != nil {
			return nil, fmt.Errorf("formengine: tab %q: visible_when: %w", t.ID, err)
		}

		tabs = append(tabs, Tab{ID: t.ID, Label: t.Label, Rows: rows, Details: t.Details, Embeds: t.Embeds, VisibleWhen: tabVW})
	}
	return tabs, nil
}

// resolveSection turns a rawSection (whose Fields are property-key
// strings) into a Section (whose Fields are the actual resolved Field
// values), applying the §3.4 default layout.
func resolveSection(def rawSection, fieldsByName map[string]Field) (Section, error) {
	layout := def.Layout
	if layout == "" {
		layout = "1-col"
	}
	vw, err := parseVisibleWhen(def.VisibleWhen)
	if err != nil {
		return Section{}, fmt.Errorf("visible_when: %w", err)
	}
	sec := Section{ID: def.ID, Label: def.Label, Layout: layout, Attachable: def.Attachable, VisibleWhen: vw}
	for _, fname := range def.Fields {
		if f, ok := fieldsByName[fname]; ok {
			sec.Fields = append(sec.Fields, f)
		}
		// A field name in section.fields with no matching schema
		// property is silently skipped rather than erroring — the
		// section-vs-row validation (§15.1) covers dead section_defs;
		// a typo'd field name inside an otherwise-valid section is a
		// narrower authoring mistake left for a future schema-linting
		// pass rather than a hard load-time failure.
	}
	return sec, nil
}

// parseField decodes one property definition into a Field.
func parseField(name string, raw json.RawMessage) (Field, error) {
	var p struct {
		Title  string   `json:"title"`
		Type   string   `json:"type"`
		Format string   `json:"format"`
		Enum   []string `json:"enum"`

		XSeamInput           string            `json:"x-seam-input"`
		XSeamSource          string            `json:"x-seam-source"`
		XSeamHelp            string            `json:"x-seam-help"`
		XSeamHelpStyle       string            `json:"x-seam-help-style"`
		XSeamRows            int               `json:"x-seam-rows"`
		XSeamAutocomplete    string            `json:"x-seam-autocomplete"`
		XSeamVisibleWhen     json.RawMessage   `json:"x-seam-visible-when"`
		XSeamHiddenReject    bool              `json:"x-seam-hidden-reject"`
		XSeamFKPopover       string            `json:"x-seam-fk-popover"`
		XSeamRelation        string            `json:"x-seam-relation"`
		XSeamSearchFields    []string          `json:"x-seam-search-fields"`
		XSeamSelector        string            `json:"x-seam-selector"`
		XSeamNaming          string            `json:"x-seam-naming"`
		XSeamPlaceholder     string            `json:"x-seam-placeholder"`
		XSeamLayout          string            `json:"x-seam-layout"`
		XSeamBrowse          string            `json:"x-seam-browse"`
		XSeamHighlight       string            `json:"x-seam-highlight"`
		XSeamAddLabel        string            `json:"x-seam-add-label"`
		XSeamAddIcon         string            `json:"x-seam-add-icon"`
		XSeamAddStyle        string            `json:"x-seam-add-style"`
		XSeamRemoveIcon      string            `json:"x-seam-remove-icon"`
		XSeamRemoveLabel     string            `json:"x-seam-remove-label"`
		XSeamRemoveColor     string            `json:"x-seam-remove-color"`
		XSeamAccent          string            `json:"x-seam-accent"`
		XSeamReorder         bool              `json:"x-seam-reorder"`
		MinItems             *int              `json:"minItems"`
		MaxItems             *int              `json:"maxItems"`
		XSeamOptionIcons     map[string]string `json:"x-seam-option-icons"`
		XSeamIconPosition    string            `json:"x-seam-icon-position"`
		XSeamOptionDisabled  []string          `json:"x-seam-option-disabled"`
		XSeamDisplayFields   []string          `json:"x-seam-display-fields"`
		XSeamOptionGroups    bool              `json:"x-seam-option-groups"`
		DecimalPrecision     int               `json:"decimalPrecision"`
		DecimalScale         int               `json:"decimalScale"`
		XSeamERPCode         string            `json:"x-seam-erp-code"`
		XSeamCustomValidator string            `json:"x-seam-custom-validator"`
		XSeamBarcodeFormat   string            `json:"x-seam-barcode-format"`
		XSeamPanel           string            `json:"x-seam-panel"`
		XSeamHideLabel       bool              `json:"x-seam-hide-label"`
		XSeamInputMask       string            `json:"x-seam-input-mask"`
		XSeamUnit            string            `json:"x-seam-unit"`
		Items                json.RawMessage   `json:"items"`

		XSeamQuery       string               `json:"x-seam-query"`
		XSeamColumns     []querypicker.Column `json:"x-seam-columns"`
		XSeamValueField  string               `json:"x-seam-value-field"`
		XSeamLabelFields []string             `json:"x-seam-label-fields"`
		XSeamDropdown    string               `json:"x-seam-dropdown"`
		XSeamDetail      bool                 `json:"x-seam-detail"`
		XSeamLimit       int                  `json:"x-seam-limit"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Field{}, err
	}
	title := p.Title
	if title == "" {
		title = name
	}
	rows := p.XSeamRows
	if rows == 0 {
		rows = 3 // §4.2 default for textarea
	}
	decimalPrecision, decimalScale := p.DecimalPrecision, p.DecimalScale
	if p.Format == "decimal" {
		// xolu's own defaults (docs/JSON_SCHEMA.md), applied here so a
		// schema author can omit either and still get correct widget
		// behaviour rather than a silent 0/0.
		if decimalPrecision == 0 {
			decimalPrecision = 18
		}
		if decimalScale == 0 {
			decimalScale = 4
		}
	}
	visibleWhen, err := parseVisibleWhen(p.XSeamVisibleWhen)
	if err != nil {
		return Field{}, fmt.Errorf("x-seam-visible-when: %w", err)
	}

	var items []Field
	if p.Type == "array" && len(p.Items) > 0 {
		items, err = parseItemFields(p.Items)
		if err != nil {
			return Field{}, fmt.Errorf("items: %w", err)
		}
	}

	// Bounds on a repeatable's rows, and icons on a select's options: both are
	// checked here so a mistake is found when the schema is written.
	minItems, maxItems, err := parseRepeatableBounds(p.MinItems, p.MaxItems)
	if err != nil {
		return Field{}, err
	}
	optionIcons, err := parseOptionIcons(p.XSeamOptionIcons)
	if err != nil {
		return Field{}, err
	}
	iconPosition, err := parseIconPosition(p.XSeamIconPosition)
	if err != nil {
		return Field{}, err
	}
	optionDisabled := parseOptionDisabled(p.XSeamOptionDisabled)

	queryPicker, err := parseQueryPicker(p.XSeamInput, p.Type, p.XSeamQuery, p.XSeamColumns, p.XSeamValueField, p.XSeamLabelFields, p.XSeamLimit, p.XSeamDropdown)
	if err != nil {
		return Field{}, err
	}

	return Field{
		Name:                 name,
		Title:                title,
		JSONType:             p.Type,
		Format:               p.Format,
		Enum:                 p.Enum,
		XSeamInput:           p.XSeamInput,
		XSeamSource:          p.XSeamSource,
		XSeamHelp:            p.XSeamHelp,
		XSeamHelpStyle:       p.XSeamHelpStyle,
		XSeamRows:            rows,
		XSeamAutocomplete:    p.XSeamAutocomplete,
		XSeamVisibleWhen:     visibleWhen,
		XSeamHiddenReject:    p.XSeamHiddenReject,
		XSeamFKPopover:       p.XSeamFKPopover,
		XSeamRelation:        p.XSeamRelation,
		XSeamSearchFields:    p.XSeamSearchFields,
		XSeamSelector:        p.XSeamSelector,
		XSeamNaming:          p.XSeamNaming,
		XSeamPlaceholder:     p.XSeamPlaceholder,
		XSeamLayout:          p.XSeamLayout,
		XSeamBrowse:          p.XSeamBrowse,
		XSeamHighlight:       p.XSeamHighlight,
		XSeamAddLabel:        p.XSeamAddLabel,
		XSeamAddIcon:         p.XSeamAddIcon,
		XSeamAddStyle:        p.XSeamAddStyle,
		XSeamRemoveIcon:      p.XSeamRemoveIcon,
		XSeamRemoveLabel:     p.XSeamRemoveLabel,
		XSeamRemoveColor:     p.XSeamRemoveColor,
		XSeamAccent:          p.XSeamAccent,
		XSeamReorder:         p.XSeamReorder,
		MinItems:             minItems,
		MaxItems:             maxItems,
		XSeamOptionIcons:     optionIcons,
		XSeamIconPosition:    iconPosition,
		XSeamOptionDisabled:  optionDisabled,
		XSeamDisplayFields:   p.XSeamDisplayFields,
		XSeamOptionGroups:    p.XSeamOptionGroups,
		DecimalPrecision:     decimalPrecision,
		DecimalScale:         decimalScale,
		XSeamERPCode:         p.XSeamERPCode,
		XSeamCustomValidator: p.XSeamCustomValidator,
		XSeamBarcodeFormat:   p.XSeamBarcodeFormat,
		XSeamPanel:           p.XSeamPanel,
		XSeamHideLabel:       p.XSeamHideLabel,
		XSeamInputMask:       p.XSeamInputMask,
		XSeamUnit:            p.XSeamUnit,
		Items:                items,

		QueryPicker:   queryPicker,
		XSeamDropdown: p.XSeamDropdown,
		XSeamDetail:   p.XSeamDetail,
	}, nil
}

// parseItemFields parses an array field's "items" sub-schema (JSON
// Schema "items": {"type":"object","properties":{...}}) into row field
// definitions, in document order — same propertyOrder + parseField
// pipeline the form's own top-level properties use, applied to the
// items sub-document instead.
func parseItemFields(itemsRaw json.RawMessage) ([]Field, error) {
	var items struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return nil, err
	}
	if items.Type != "object" || len(items.Properties) == 0 {
		return nil, nil
	}
	order, err := propertyOrder(itemsRaw)
	if err != nil {
		return nil, err
	}
	required := make(map[string]bool, len(items.Required))
	for _, r := range items.Required {
		required[r] = true
	}
	fields := make([]Field, 0, len(order))
	for _, fname := range order {
		propRaw, ok := items.Properties[fname]
		if !ok {
			return nil, fmt.Errorf("property %q in document order but not in decoded properties", fname)
		}
		f, err := parseField(fname, propRaw)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", fname, err)
		}
		f.Required = required[fname]
		if f.Required && (inferWidget(f) == "repeatable" || f.JSONType == "array") && f.MinItems == 0 {
			f.MinItems = 1
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// propertyOrder returns the field names under the schema's top-level
// "properties" object, in the order they appear in the document.
//
// json.Unmarshal into a map does not preserve key order — Go maps have
// none — so §3.10's "render fields in the order they appear in
// properties" cannot be satisfied by decoding into
// map[string]interface{} alone. This walks the raw token stream instead,
// which does preserve document order, to recover just the ordering.
func propertyOrder(raw []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))

	// Find the "properties" key at the top level.
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("no \"properties\" object found: %w", err)
		}
		switch t := tok.(type) {
		case json.Delim:
			if t == '{' || t == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if depth == 1 && t == "properties" {
				goto found
			}
		}
	}
found:
	// Next token must be the opening '{' of the properties object.
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("\"properties\" is not an object")
	}

	var names []string
	propDepth := 1
	for propDepth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				propDepth++
			case '}', ']':
				propDepth--
			}
		case string:
			// A field name is a string token at propDepth 1 (the level
			// of the properties object's own direct keys), immediately
			// followed by that field's definition object. Nested string
			// tokens inside a field's own definition occur at propDepth
			// >= 2 and must not be collected.
			if propDepth == 1 {
				names = append(names, t)
			}
		}
	}
	return names, nil
}
