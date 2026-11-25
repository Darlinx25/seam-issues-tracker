// Package formengine renders dynamic forms from JSON Schema documents
// extended with Seam's x-seam-* properties (docs/FORM_ENGINE_SPEC.md v0.3).
//
// The schema is the only source of truth for a form's structure, widgets,
// and validation rules — adding a field or reorganising a tab is a
// configuration change, not a code change or redeploy.
//
// Package layout (FORM_ENGINE_SPEC.md §13.1):
//
//	engine.go   — entry point: FormEngine, RenderContext, Render
//	schema.go   — reading and resolving the JSON Schema + x-seam-* extensions
//	renderer.go — per-field rendering orchestration
//	widgets.go  — individual widget implementations
//	sources.go  — x-seam-source resolution: loading options from entities
package formengine

import (
	"context"
	"fmt"

	mi "github.com/ha1tch/minty"
)

// Option is a single selectable value in a select-style widget.
type Option struct {
	Value    string
	Label    string
	// Icon is an optional Material Icons name shown beside the text by the
	// combobox (select_icons.go); one that is not an icon name is dropped.
	Icon     string
	// Disabled marks the option as not selectable. SeamSelect renders it
	// greyed out and the native select refuses to choose it.
	Disabled bool
}

// SourceResolver resolves an x-seam-source value (e.g. "entity:brands")
// to the list of selectable options for a select widget. The concrete,
// xolu-backed implementation is supplied by the caller via
// FormEngine.Sources — this package has no direct xolu dependency.
// See sources.go.
type SourceResolver interface {
	Resolve(ctx context.Context, source string) ([]Option, error)
}

// FormEngine renders a dynamic form from a JSON Schema document.
// FORM_ENGINE_SPEC.md §13.2 names this Seam's minimal public interface to
// the form engine.
type FormEngine struct {
	// Sources resolves x-seam-source values for select widgets whose
	// options come from another entity rather than a static enum.
	// May be nil if the caller has already confirmed no field in the
	// schemas it renders uses x-seam-source — Render returns an error
	// node for such a field rather than panicking if Sources is nil and
	// one is encountered.
	Sources SourceResolver

	// Translator resolves i18n keys for field titles/help and tab/
	// section labels (I18N_SPEC.md §4.1-§4.2). May be nil — every
	// resolution path falls back to the schema's own literal text when
	// Translator is nil or a key doesn't resolve, so rendering without
	// i18n wired up still works exactly as it did before T-13.
	//
	// Caller warning, confirmed by a real crash, not a hypothetical:
	// never assign a nil concrete pointer directly to this field (e.g.
	// Translator: someNilPtr where someNilPtr is a *SomeType(nil)).
	// That produces a "typed nil" -- a non-nil interface wrapping a nil
	// value -- so the nil checks throughout this package's resolution
	// code (which compare the interface itself to nil) silently
	// evaluate false, and the next call into it panics on the nil
	// receiver. Leave the field unset (its zero value is a true nil
	// interface) or explicitly check the concrete pointer for nil
	// before assigning.
	Translator Translator

	// CustomValidators resolves T-10's "custom validation" extension
	// point (x-seam-custom-validator on a field, validated by
	// ValidateCustomFields). Keyed by the name a schema field's
	// x-seam-custom-validator references; the function receives the
	// field's submitted value and returns a non-nil error to reject it.
	// Deliberately the same signature as queryfy's own ValidatorFunc
	// (func(value interface{}) error) so a caller who later wants to
	// register the same function with queryfy directly (once a real
	// per-field injection point exists -- not something this session
	// builds, see ValidateCustomFields' own comment) can do so with no
	// adapter. nil (the zero value) is a legitimate, common case: most
	// schemas define no x-seam-custom-validator fields at all, and
	// ValidateCustomFields treats a nil map exactly like an empty one
	// -- every such field fails closed with a clear "not registered"
	// error rather than silently passing, so a schema author's typo in
	// the validator name surfaces immediately in testing rather than
	// silently skipping validation in production.
	CustomValidators map[string]func(value interface{}) error
}

// ValidateCustomFields runs T-10's custom-validation extension point:
// for every field in form.Fields with XSeamCustomValidator set, looks
// up that name in e.CustomValidators and calls it with the field's
// current value from values. Returns a flat field-name -> message map,
// the same shape validateAssetValues (internal/ui) already builds by
// hand from queryfy's own errors -- callers merge the two the same way
// that function merges multiple error sources today (first error per
// path wins is this package's own established convention; merging
// order is the caller's choice, not enforced here).
//
// A field naming a validator that isn't registered in
// e.CustomValidators (typo, or CustomValidators left nil/empty) is
// itself a validation failure, not a silent skip -- see
// CustomValidators' own doc comment above for why: a schema-authoring
// mistake should surface immediately in testing, not pass validation
// silently in production because nothing was actually checked.
//
// Message resolution is deliberately simple: err.Error() verbatim, not
// run through ResolveQueryfyError (i18n_errors.go) -- that function
// decodes queryfy's own specific structured error-message format,
// which a hand-written custom validator has no reason to produce. A
// validator that wants a translated message is responsible for
// resolving it itself (it already has no less capability to do so than
// any other Go code in this codebase) before returning the error.
//
// Nested/array-field values (e.g. a custom validator on a repeatable
// row's own item field) are out of scope for this pass -- forms.Fields
// only walks top-level fields, matching every other extension point
// added this session. A future need for row-level custom validation
// would need FieldStatus-style path handling, not attempted here
// without a real, named use case driving the design.
func ValidateCustomFields(e *FormEngine, form *Form, values map[string]interface{}) map[string]string {
	if form == nil {
		return nil
	}
	var errs map[string]string
	for _, f := range form.Fields {
		if f.XSeamCustomValidator == "" {
			continue
		}
		validator, ok := e.CustomValidators[f.XSeamCustomValidator]
		if !ok {
			if errs == nil {
				errs = make(map[string]string)
			}
			errs[f.Name] = fmt.Sprintf("formengine: field %q references custom validator %q, which is not registered", f.Name, f.XSeamCustomValidator)
			continue
		}
		if err := validator(values[f.Name]); err != nil {
			if errs == nil {
				errs = make(map[string]string)
			}
			errs[f.Name] = err.Error()
		}
	}
	return errs
}

// Translator resolves one i18n key for one locale. Deliberately the same
// shape as internal/i18n.Translator's own T method (locale, key,
// interpolation args) so that type already satisfies this interface
// structurally — formengine doesn't import internal/i18n directly, same
// decoupling as SourceResolver not importing xolu's client package.
type Translator interface {
	T(locale, key string, args ...any) string
}

// RenderContext describes the full rendering context for one form: which
// form, what it submits to, and every permission/visibility decision the
// caller has already resolved. The engine trusts this structure
// completely — it does not consult roles, sessions, or entity state on
// its own; that resolution is the caller's job (a permission service, not
// yet built — see the T-01 tracker, stage 6).
//
// Authoritative definition: FORM_ENGINE_SPEC.md §3c.2.
type RenderContext struct {
	// Form identification
	Module string // e.g. "assets"
	Form   string // e.g. "asset"

	// Submission
	Action     string // URL the form submits to
	Method     string // "post" | "put" | "patch"
	IsPartial  bool   // true when rendering inside SeamModal / SeamModal2
	IsEdit     bool   // true = editing an existing entity, false = creating
	EntityID   int64  // entity ID in edit mode (needed by detail/embed/tray endpoints)
	EntityType string // "asset" | "sensor" | "custodio" | ... (needed by tray)
	ActiveTab  string // id of the tab to open first; anything that is not a tab of the form is ignored
	CSRFToken  string

	// Lifecycle read-only state
	ReadOnly bool // true = the entire form is read-only

	// Resolved permissions — computed by the caller's permission service
	// before invoking the engine. Everything listed here is omitted from
	// the rendered HTML entirely; the engine never emits a hidden element
	// for client-side toggling.
	HiddenFields   []string
	HiddenSections []string
	// HiddenRows is keyed by tab ID, not flat — FORM_ENGINE_SPEC.md's own
	// §3c.2 declares this as []int, but §3c.4 (ResolvedPerms, which
	// exists specifically to populate this field) declares the same
	// data as map[string][]int. A flat list can't distinguish "hide row
	// 2 in tab A" from "hide row 2 in tab B" for a multi-tab form — the
	// very first example schema in the spec is multi-tab — so the two
	// sections of the spec disagree with each other, not just with this
	// code. Resolved 2026-07-28, stage 6: took the map shape as
	// authoritative, since it's the only one that's well-defined for
	// more than one tab, and fixed here before any real caller
	// (Administration module doesn't exist yet) could ship against the
	// broken flat version.
	HiddenRows map[string][]int
	HiddenTabs []string

	ReadOnlyFields   []string
	ReadOnlySections []string
	ReadOnlyTabs     []string

	// Attachment tray permissions, keyed by section ID.
	// Values: "hidden" | "read" | "preview" | "full".
	// A section with attachable:true and no entry here renders without a
	// tray. Not consumed by any code path before stage 8 (T-01 tracker
	// FE-25, deferred — see docs/FORMENGINE_DEVELOPMENT_PLAN.md).
	TrayPermissions map[string]string

	// Locale active for i18n key resolution.
	Locale string

	// Statuses is an additive, optional alternative to the errors map
	// Render's own errors parameter carries -- introduced for element
	// identifiers that don't fit the "exact field name as a map key"
	// shape (nested array-item paths like "document_links[0].name",
	// which is what queryfy's own validator actually produces for
	// object-in-array constraints, confirmed empirically, not assumed).
	// Deliberately additive, not a replacement: the errors map keeps
	// working exactly as it always has for every existing caller, and
	// Statuses is consulted alongside it, not instead of it. Also
	// carries a genuine severity beyond error/no-error -- "WRN"/"INF"
	// entries are recorded and matched the same way "ERR" is, though
	// no widget gives them distinct visual treatment yet (that's
	// separate, later work, not implied by this field's existence).
	Statuses []FieldStatus
}

// FieldStatus is one status entry for an element -- a field name, or a
// queryfy-style nested path (see RenderContext.Statuses' own comment
// for why paths need this rather than the errors map). Element is
// matched against a field's name by resolveFieldStatus, not required
// to be a valid Go map key or HTML attribute name itself.
type FieldStatus struct {
	Element string
	Status  string // "ERR" | "WRN" | "INF"
	Message string
}

// Render produces the minty node tree for the form described by schema,
// populated with values and current validation errors, under rc.
//
// schema is the raw JSON Schema document (with x-seam-* extensions)
// exactly as stored in xolu — see schema.go for parsing and compilation.
// values holds the current field values (from the entity being edited, or
// empty on create). errors maps field name to a validation error message,
// set when the form is being re-rendered after a failed submit.
//
// Deliberate deviation from FORM_ENGINE_SPEC.md §13.2's exact prose
// signature: this takes a context.Context as its first parameter.
// Resolving an x-seam-source during rendering is a network call to xolu
// (§5.1); a form-rendering call path that can make network calls without
// a way to cancel or time it out is not something to build deliberately.
// The spec's §13.1/§13.2 use "SchemaToForm" and "Render" prose-interchangeably
// for this entry point; this package uses the method name.
//
// If schema fails to parse or compile, Render does not panic or return an
// error (matching the spec's mi.H-only return) — it returns a node that
// renders a visible, non-crashing error banner instead. Schemas are
// expected to be validated once at registration time (§9.1), so a parse
// failure here indicates a caller bug, not routine user input to reject
// gracefully; making it loudly visible in the rendered page is preferable
// to a panic that takes down the whole request.
func (e *FormEngine) Render(ctx context.Context, schema []byte, values map[string]interface{}, errors map[string]string, rc RenderContext) mi.H {
	form, err := ParseSchema(schema)
	if err != nil {
		return errorBanner(err)
	}
	// Resolve i18n module/form identity once, per §4.1's precedence
	// (RenderContext overrides the schema's own default). Every
	// downstream render function reads rc.Module/rc.Form directly from
	// here on, rather than each needing *Form threaded through
	// separately just for this.
	rc.Module, rc.Form = resolveModuleForm(form, rc)
	if o := renderObserver.Load(); o != nil {
		(*o)(ctx, schema, rc.Module, rc.Form)
	}
	return renderForm(ctx, e, form, values, errors, rc)
}
