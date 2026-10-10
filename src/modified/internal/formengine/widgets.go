package formengine

import (
	"context"
	"fmt"
	"html"
	"strings"

	mi "github.com/ha1tch/minty"
)

// widgetRenderer renders one field's bare input control — not its label,
// help text, or error message, which renderer.go applies uniformly across
// every widget. value is the field's current value (nil if unset);
// hasError is true when errors[f.Name] is non-empty, so the widget can
// apply error styling without needing the message text itself.
type widgetRenderer func(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H

// widgetRegistry maps an x-seam-input value to its renderer. A registry,
// not a switch — FORMENGINE_DEVELOPMENT_PLAN.md stage 1 calls this out as
// load-bearing: T-10's widgets (signature, image, GPS, multi-select,
// decimal precision) must be addable later by registering a new entry
// here, not by restructuring this file.
//
// This is the stage-1 MVP set (FORM_ENGINE_SPEC.md §14): thirteen scalar
// widgets. document-links, repeatable, and asset-type-select are
// structurally container/dynamic-source widgets and land with stages
// 3–4 (tabs/rows) and sources.go respectively, not here.
// widgetRegistry is populated in init(), not a var literal, because
// renderRepeatable's body refers back to widgetRegistry (to dispatch
// each row's item fields through the normal widget lookup) -- Go's
// initialization-cycle detector conservatively flags that as a cycle
// even though it isn't one at runtime (the map is fully populated
// before anything ever calls a widgetRenderer). init() runs after all
// package-level declarations exist, which sidesteps the static check
// entirely. Confirmed this is the actual Go behaviour, not guessed --
// the plain var-literal form failed to build with exactly this error.
var widgetRegistry map[string]widgetRenderer

func init() {
	widgetRegistry = map[string]widgetRenderer{
		"text":              renderTextLike("text"),
		"email":             renderTextLike("email"),
		"url":               renderTextLike("url"),
		"tel":               renderTextLike("tel"),
		"date":              renderTextLike("date"),
		"datetime-local":    renderTextLike("datetime-local"),
		"time":              renderTextLike("time"),
		"password":          renderPassword,
		"signature":         renderSignature,
		"number":            renderNumber,
		"decimal":           renderDecimal,
		"barcode":           renderBarcode,
		"gps":               renderGPS,
		"image":             renderImage,
		"select":            renderSelect,
		"multi-select":      renderMultiSelect,
		"checkbox":          renderCheckboxLike(false),
		"toggle":            renderCheckboxLike(true),
		"color":             renderColor,
		"textarea":          renderTextarea,
		"asset-picker":      renderAssetPicker,
		"any-picker":        renderAnyPicker, // T-97; asset-picker stays as its own name
		"document-links":    renderDocumentLinks,
		"repeatable":        renderRepeatable,
		"asset-type-select": renderAssetTypeSelect,
	}
}

// selfLabeledWidgets are widgets that render their own label as part of
// the control (checkbox/toggle put the label beside the control, not
// above it) — renderer.go skips its usual top-label wrapper for these.
var selfLabeledWidgets = map[string]bool{
	"checkbox": true,
	"toggle":   true,
}

// inferWidget determines the widget for a field with no explicit
// x-seam-input, per FORM_ENGINE_SPEC.md §4.1.
func inferWidget(f Field) string {
	if f.XSeamInput != "" {
		return f.XSeamInput
	}
	switch f.JSONType {
	case "string":
		if len(f.Enum) > 0 {
			return "select"
		}
		switch f.Format {
		case "decimal":
			return "decimal"
		case "email":
			return "email"
		case "uri":
			return "url"
		case "date":
			return "date"
		case "date-time":
			return "datetime-local"
		case "time":
			return "time"
		case "tel":
			return "tel"
		default:
			return "text"
		}
	case "integer", "number":
		return "number"
	case "boolean":
		return "checkbox"
	case "array":
		return "repeatable" // not in the stage-1 registry; renders as unsupported until stage 3/4
	default:
		return "text"
	}
}

func inputClass(hasError bool) string {
	base := "mt-1 block w-full rounded-lg px-3 py-2 text-sm bg-white dark:bg-gray-700 focus:outline-none focus:ring-2"
	if hasError {
		return base + " border-red-500 focus:ring-red-500 focus:border-red-500"
	}
	return base + " border border-gray-300 dark:border-gray-600 focus:ring-indigo-500 focus:border-indigo-500"
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

// renderTextLike covers text, email, url, tel, date, datetime-local, time —
// every widget that is a plain <input> distinguished only by its type
// attribute (FORM_ENGINE_SPEC.md §4.2).
//
// XSeamInputMask (T-10) is applied here, not as its own widget type --
// it's a formatting modifier on an existing text-like field, not a
// different kind of field (matching how Pablo's own spec lists
// "Máscara de ingreso" as a Field Definition attribute, not a Type).
// When f.XSeamInputMask is empty (every field before this session, and
// the overwhelming majority after it), this function's output is
// byte-for-byte identical to before -- a single <input>, no wrapper,
// no script. Masking is a small, hand-rolled inline script (mirroring
// the signature/asset-picker widgets' own established pattern of a
// self-contained per-field script) rather than a vendored library:
// the algorithm is well-understood and small (~30 lines), and pulling
// in a dependency for it would cost more than it saves. Only f.Name
// (schema-authored) and f.XSeamInputMask (schema-authored) are
// interpolated into the script -- never value, matching the
// T-63/T-64 lesson every other widget's inline script already follows.
func renderTextLike(inputType string) widgetRenderer {
	return func(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
		return func(b *mi.Builder) mi.Node {
			attrs := []mi.Attribute{
				mi.Type(inputType),
				mi.Name(f.Name),
				mi.ID("field-" + f.Name),
				mi.Class(inputClass(hasError)),
				mi.Value(stringValue(value)),
			}
			if f.Required {
				attrs = append(attrs, mi.Required())
			}
			if f.XSeamInputMask == "" {
				return b.Input(attrs...)
			}
			attrs = append(attrs, mi.Attr("placeholder", f.XSeamInputMask), mi.Attr("maxlength", fmt.Sprintf("%d", len(f.XSeamInputMask))))
			return b.Div(
				b.Input(attrs...),
				b.Script(mi.Raw(fmt.Sprintf(`(function(){
  var mask = %q;
  var el = document.getElementById(%q);
  function applyMask(raw) {
    var out = '', ri = 0;
    for (var mi_ = 0; mi_ < mask.length && ri < raw.length; mi_++) {
      var m = mask[mi_], c = raw[ri];
      if (m === '9') {
        if (/[0-9]/.test(c)) { out += c; ri++; } else { ri++; mi_--; }
      } else if (m === 'A') {
        if (/[a-zA-Z]/.test(c)) { out += c.toUpperCase(); ri++; } else { ri++; mi_--; }
      } else if (m === 'a') {
        if (/[a-zA-Z]/.test(c)) { out += c; ri++; } else { ri++; mi_--; }
      } else if (m === '*') {
        if (/[a-zA-Z0-9]/.test(c)) { out += c; ri++; } else { ri++; mi_--; }
      } else {
        out += m;
        if (c === m) ri++;
      }
    }
    return out;
  }
  el.addEventListener('input', function() {
    var pos = el.selectionStart;
    var before = el.value.length;
    el.value = applyMask(el.value.replace(/[^a-zA-Z0-9]/g, ''));
    var after = el.value.length;
    el.selectionStart = el.selectionEnd = Math.max(0, pos + (after - before));
  });
})();`, f.XSeamInputMask, "field-"+f.Name))),
			)
		}
	}
}

// renderPassword is deliberately its own widget, not renderTextLike("password")
// with a new case bolted in: renderTextLike always sets mi.Value(stringValue
// (value)), echoing whatever's in the current values map back into the
// rendered HTML's value= attribute -- correct and expected for every other
// text-like field, but a real security concern for a password field, whose
// value must never appear in rendered markup under any circumstance
// (validation-error re-render, browser history, screen recording, view-
// source). value is deliberately never read here, not merely left unused.
func renderPassword(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		attrs := []mi.Attribute{
			mi.Type("password"),
			mi.Name(f.Name),
			mi.ID("field-" + f.Name),
			mi.Class(inputClass(hasError)),
		}
		if f.Required {
			attrs = append(attrs, mi.Required())
		}
		if f.XSeamAutocomplete != "" {
			attrs = append(attrs, mi.Attr("autocomplete", f.XSeamAutocomplete))
		}
		return b.Input(attrs...)
	}
}

// renderSignature renders a canvas-based signature-capture widget --
// T-10's own named "missing widget" for the Order Engine/CMMS work
// (docs/TRACKING.md), built 2026-08-04 as concrete progress on that
// item rather than leaving it at "not decided yet" indefinitely. A
// general-purpose primitive, not Order-Engine-specific -- doesn't
// require T-11's own scoping to be useful, the same way asset-picker
// (T-19) isn't tied to any one consuming feature either.
//
// The submitted value is a data:image/png;base64,... URL, carried in a
// hidden input (the actual form field) alongside a <canvas> the user
// draws on. Security design, matching the exact lesson T-63/T-64 this
// session established the hard way: the EXISTING value (when editing a
// record with a previously-captured signature) is placed in the hidden
// input's own value="..." attribute -- routed through minty's normal
// automatic html.EscapeString escaping, like every other widget's
// value, NOT interpolated into the <script> block at all. The
// accompanying JS reads that value back via a safe DOM API
// (getElementById(...).value) at runtime, never via string
// interpolation into script source. Only f.Name -- schema-authored,
// server-controlled, never user-submitted -- is interpolated into the
// script itself (for the canvas/field element IDs), the same
// trusted-by-provenance reasoning already established for the (safe)
// mi.Raw sites audited after T-64.
func renderSignature(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		fieldID := "field-" + f.Name
		canvasID := "sigcanvas-" + f.Name

		wrapperBorder := "border border-gray-300 dark:border-gray-600"
		if hasError {
			wrapperBorder = "border border-red-500"
		}
		return b.Div(mi.Class("space-y-2"),
			b.Input(mi.Type("hidden"), mi.Name(f.Name), mi.ID(fieldID), mi.Value(stringValue(value))),
			b.Div(mi.Class(wrapperBorder+" rounded-lg overflow-hidden bg-white"),
				b.Canvas(mi.ID(canvasID), mi.Attr("width", "500"), mi.Attr("height", "150"),
					mi.Class("w-full touch-none cursor-crosshair block")),
			),
			b.Div(mi.Class("flex justify-end"),
				b.Button(mi.Type("button"),
					mi.Attr("onclick", fmt.Sprintf("SeamSignature.clear(%q)", f.Name)),
					mi.Class("text-sm text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"),
					"Clear"),
			),
			b.Script(mi.Raw(fmt.Sprintf(`(function(){
  var fieldName = %q, canvasId = %q;
  var canvas = document.getElementById(canvasId);
  if (!canvas) return;
  var ctx = canvas.getContext('2d');
  ctx.fillStyle = '#fff';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.strokeStyle = '#1f2937';
  ctx.lineWidth = 2;
  ctx.lineJoin = 'round';
  ctx.lineCap = 'round';

  var hidden = document.getElementById('field-' + fieldName);
  var existing = hidden ? hidden.value : '';
  if (existing) {
    var img = new Image();
    img.onload = function() { ctx.drawImage(img, 0, 0, canvas.width, canvas.height); };
    img.src = existing;
  }

  var drawing = false;
  function pos(e) {
    var r = canvas.getBoundingClientRect();
    return { x: (e.clientX - r.left) * (canvas.width / r.width), y: (e.clientY - r.top) * (canvas.height / r.height) };
  }
  function start(e) { drawing = true; var p = pos(e); ctx.beginPath(); ctx.moveTo(p.x, p.y); }
  function move(e) { if (!drawing) return; var p = pos(e); ctx.lineTo(p.x, p.y); ctx.stroke(); }
  function end() {
    if (!drawing) return;
    drawing = false;
    if (hidden) hidden.value = canvas.toDataURL('image/png');
  }
  canvas.addEventListener('pointerdown', start);
  canvas.addEventListener('pointermove', move);
  window.addEventListener('pointerup', end);

  window.SeamSignature = window.SeamSignature || {};
  window.SeamSignature.clear = function(name) {
    if (name !== fieldName) return;
    ctx.fillStyle = '#fff';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    if (hidden) hidden.value = '';
  };
})();`, f.Name, canvasID))),
		)
	}
}

// renderBarcode implements the "barcode" widget (T-10): a text field
// for a linear EAN/UPC barcode value, with a live SVG preview rendered
// via JsBarcode (vendored, MIT -- web/static/vendor/VENDOR.md Asset 7)
// -- distinct from the QR support internal/qrcode already has (a
// different symbology entirely; QR is 2D, EAN/UPC are 1D). The
// submitted field value is the plain code string itself (e.g.
// "012345678905"), same as any text field -- the barcode image is a
// live visual aid for the operator to catch a mistyped digit before
// submitting, not a separately-encoded value. Renders correctly, not
// scanning: this widget draws a barcode from typed input; reading one
// via a device camera is a different, harder feature this item never
// asked for (docs/ORDER_ENGINE_INTEGRATION_NOTES.md §6 raises exactly
// this generate-vs-scan distinction for QR, unresolved there too).
//
// f.XSeamBarcodeFormat selects the symbology (EAN13 default, or EAN8/
// UPC/UPCE); JsBarcode's own valid callback drives a visual invalid
// state on the input (red border) without blocking typing or throwing
// -- a value that isn't a valid code yet (still being typed) is a
// normal, expected transient state, not an error to react loudly to.
func renderBarcode(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		format := f.XSeamBarcodeFormat
		if format == "" {
			format = "EAN13"
		}
		fieldID := "field-" + f.Name
		svgID := "barcode-svg-" + f.Name
		attrs := []mi.Attribute{
			mi.Type("text"),
			mi.Name(f.Name),
			mi.ID(fieldID),
			mi.Class(inputClass(hasError)),
			mi.Value(stringValue(value)),
		}
		if f.Required {
			attrs = append(attrs, mi.Required())
		}
		return b.Div(mi.Class("space-y-2"),
			b.Input(attrs...),
			b.Svg(mi.ID(svgID)),
			b.Script(mi.Src("/static/vendor/jsbarcode-ean-upc@3.12.3.min.js")),
			b.Script(mi.Raw(fmt.Sprintf(`(function(){
  var format = %q;
  var input = document.getElementById(%q);
  var svg = document.getElementById(%q);
  function render() {
    if (!input.value) { svg.innerHTML = ''; return; }
    try {
      JsBarcode(svg, input.value, {
        format: format,
        width: 2,
        height: 60,
        displayValue: true,
        valid: function(ok) { input.classList.toggle('border-red-500', !ok); }
      });
    } catch (e) {
      svg.innerHTML = '';
    }
  }
  render();
  input.addEventListener('input', render);
})();`, format, fieldID, svgID))),
		)
	}
}

// renderGPS implements the "gps" widget (T-10): device-location capture
// plus a Leaflet map picker, replacing the "ad-hoc lat/long number
// fields" pattern the gap analysis named
// (docs/ORDER_ENGINE_INTEGRATION_NOTES.md §4) with one unified field.
// Value format is a plain "lat,lng" string (e.g.
// "-34.9011,-56.1645"), editable directly in the text input (typing
// or pasting coordinates works, matching "device capture" broadly
// construed) or set by clicking/dragging the marker on the map below
// it. Deliberately a single new field, not a migration of Asset's own
// existing separate latitude/longitude number fields -- that's schema
// content, a different, deliberate decision left untouched here (same
// boundary XSeamUnit's own comment draws for temp_min/temp_max).
//
// Reuses the SAME vendored Leaflet already in production for
// AssetGeoPage (pages.go) -- no new vendoring needed for this widget.
// One real gap found and fixed while building this: AssetGeoPage only
// ever uses L.circleMarker (a drawn shape, no icon image), so the
// vendored marker-icon.png/marker-icon-2x.png/marker-shadow.png
// (VENDOR.md Asset 2) have never actually been exercised by any code
// path before this widget -- Leaflet's own default-icon path
// auto-detection is regex-based against the loading script's own src
// and not reliable against a versioned filename like
// "leaflet@1.9.4.js", so this widget sets L.Icon.Default.mergeOptions
// explicitly rather than trusting auto-detection to find files that
// have never actually been loaded through it before.
//
// Each field's map/button logic lives entirely inside its own IIFE
// closure -- the button is wired via a scoped addEventListener, not a
// shared window-level namespace function keyed by field name. A
// shared-global approach (SeamGPS.useDeviceLocation, closure-captured
// per field) would have the exact bug T-57 already documents for
// visibilityScript: a second GPS field on the same page would silently
// overwrite the first field's handler, so the first button would call
// the second field's closure. Deliberately avoided from the start
// rather than built and found later.
func renderGPS(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		fieldID := "field-" + f.Name
		mapID := "gps-map-" + f.Name
		buttonID := "gps-locate-" + f.Name
		current := stringValue(value)

		attrs := []mi.Attribute{
			mi.Type("text"),
			mi.Name(f.Name),
			mi.ID(fieldID),
			mi.Class(inputClass(hasError)),
			mi.Value(current),
			mi.Attr("placeholder", "-34.9011,-56.1645"),
		}
		if f.Required {
			attrs = append(attrs, mi.Required())
		}

		return b.Div(mi.Class("space-y-2"),
			b.Div(mi.Class("flex gap-2"),
				b.Input(attrs...),
				b.Button(mi.Type("button"), mi.ID(buttonID),
					mi.Class("shrink-0 text-sm px-3 py-2 rounded-lg border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 hover:bg-gray-50 dark:hover:bg-gray-700"),
					resolveWidgetChrome(e, rc, "field.gps.use_location", "Use my location"),
				),
			),
			b.Div(mi.ID(mapID), mi.Class("h-[250px] w-full rounded-lg border border-gray-300 dark:border-gray-600 overflow-hidden")),
			b.Link(mi.Rel("stylesheet"), mi.Href("/static/vendor/leaflet@1.9.4.css")),
			b.Script(mi.Src("/static/vendor/leaflet@1.9.4.js")),
			b.Script(mi.Raw(fmt.Sprintf(`(function(){
  var input = document.getElementById(%q);
  var mapEl = document.getElementById(%q);
  var button = document.getElementById(%q);
  var initial = %q;

  L.Icon.Default.mergeOptions({
    iconUrl: '/static/vendor/images/marker-icon.png',
    iconRetinaUrl: '/static/vendor/images/marker-icon-2x.png',
    shadowUrl: '/static/vendor/images/marker-shadow.png'
  });

  function parseLatLng(s) {
    var parts = (s || '').split(',');
    if (parts.length !== 2) return null;
    var lat = parseFloat(parts[0]), lng = parseFloat(parts[1]);
    if (isNaN(lat) || isNaN(lng)) return null;
    return [lat, lng];
  }

  var start = parseLatLng(initial);
  var map = L.map(mapEl).setView(start || [0, 0], start ? 13 : 2);
  L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
    attribution: '&copy; OpenStreetMap contributors'
  }).addTo(map);

  var marker = null;

  function wireDrag() {
    marker.on('dragend', function() {
      var p = marker.getLatLng();
      setPoint(p.lat, p.lng, false);
    });
  }

  function setPoint(lat, lng, recenter) {
    input.value = lat.toFixed(6) + ',' + lng.toFixed(6);
    if (marker) {
      marker.setLatLng([lat, lng]);
    } else {
      marker = L.marker([lat, lng], {draggable: true}).addTo(map);
      wireDrag();
    }
    if (recenter) map.setView([lat, lng], 15);
  }

  if (start) {
    marker = L.marker(start, {draggable: true}).addTo(map);
    wireDrag();
  }

  map.on('click', function(ev) {
    setPoint(ev.latlng.lat, ev.latlng.lng, false);
  });

  button.addEventListener('click', function() {
    if (!navigator.geolocation) return;
    navigator.geolocation.getCurrentPosition(function(pos) {
      setPoint(pos.coords.latitude, pos.coords.longitude, true);
    });
  });

  input.addEventListener('change', function() {
    var p = parseLatLng(input.value);
    if (p) setPoint(p[0], p[1], true);
  });
})();`, fieldID, mapID, buttonID, current))),
		)
	}
}

// renderImage implements the "image" widget (T-10): a photo gallery
// backed by internal/ui/media_gallery.go's xolu-/blob-plus-metadata-
// entity storage (designed together this session -- see that file's
// own doc comment for the full storage rationale), fronted by a
// Lit-wrapped Dropzone.js upload UI (web/static/js/file-upload.js).
//
// Edit-mode only, deliberately, matching the precedent renderer.go's
// own Details/Embeds gate already established (§3.7: "Details and
// embeds only render in edit mode... both require an existing entity
// (EntityID) to build their htmx URLs against") -- a new, unsaved
// record has no EntityID yet, so there is no owner to attach uploaded
// images to. Unlike Details/Embeds, which are tab-level and simply
// absent in create mode, Image is a field-level widget: silently
// rendering nothing would leave a labelled field with an empty,
// unexplained control under it, so create mode renders an explicit
// message instead of omitting the widget outright.
//
// The custom element tag itself is emitted via mi.Raw with explicit
// html.EscapeString on every interpolated value -- mi.Raw bypasses
// minty's own automatic escaping entirely, and this is the exact same
// pattern pages.go's own <seam-fsm-editor> mount uses, for the same
// T-63-derived reason: never trust an interpolated value going into a
// raw HTML string, even one that looks safe (EntityID is a database
// int64, EntityType and the field name are schema-authored, not
// operator-editable) -- escape all of them anyway rather than
// reasoning per-value about which is "safe enough" to skip.
func renderImage(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		if !rc.IsEdit {
			return b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400 italic"),
				resolveWidgetChrome(e, rc, "field.image.save_first", "Save the record first to add photos"))
		}
		return b.Div(
			b.Link(mi.Rel("stylesheet"), mi.Href("/static/vendor/dropzone@5.9.3.min.css")),
			b.Script(mi.Src("/static/vendor/dropzone@5.9.3.min.js")),
			b.Script(mi.Type("module"), mi.Src("/static/js/file-upload.js")),
			mi.Raw(fmt.Sprintf(
				`<seam-file-upload owner-type="%s" owner-id="%s" owner-field="%s" csrf-token="%s"></seam-file-upload>`,
				html.EscapeString(rc.EntityType),
				html.EscapeString(fmt.Sprintf("%d", rc.EntityID)),
				html.EscapeString(f.Name),
				html.EscapeString(rc.CSRFToken),
			)),
		)
	}
}

func renderNumber(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		attrs := []mi.Attribute{
			mi.Type("number"),
			mi.Name(f.Name),
			mi.ID("field-" + f.Name),
			mi.Class(inputClass(hasError)),
			mi.Value(stringValue(value)),
		}
		if f.Required {
			attrs = append(attrs, mi.Required())
		}
		return b.Input(attrs...)
	}
}

// renderDecimal implements the "decimal" widget (T-10): exposes xolu's
// own "format":"decimal" schema-layer fixed-point support, which
// FORM_ENGINE_SPEC.md had no widget for until now (docs/JSON_SCHEMA.md
// in xolu §"Decimal fields"). Deliberately type="text", not
// type="number" -- an HTML number input is IEEE 754 float-backed and
// can silently coerce a value like "10.999" or reformat on blur in
// browser-dependent ways, exactly the precision loss xolu's own
// "type":"string" wire format (decimal values travel as JSON strings,
// e.g. "10.99") exists to avoid. A text input submits the operator's
// keystrokes byte-for-byte; xolu's write path is the actual validation
// authority (rejects values that would need truncation, per
// docs/JSON_SCHEMA.md), not this widget.
//
// inputmode="decimal" gets the numeric keyboard on mobile without
// changing the underlying input type. The pattern attribute is a
// visual/native-validation hint derived from f.DecimalScale (e.g.
// scale 2 -> optional minus, digits, optional "." + 1-2 digits) --
// generated, not hand-authored per field, so it can't drift from the
// schema's own declared scale.
func renderDecimal(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		scale := f.DecimalScale
		if scale == 0 {
			scale = 4 // matches xolu's own default (docs/JSON_SCHEMA.md); parseField already applies
			// this when a schema omits decimalScale, so this branch only
			// guards a Field constructed some other way (e.g. tests).
		}
		var pattern string
		if scale > 0 {
			pattern = fmt.Sprintf(`-?\d+(\.\d{1,%d})?`, scale)
		} else {
			pattern = `-?\d+`
		}
		attrs := []mi.Attribute{
			mi.Type("text"),
			mi.Name(f.Name),
			mi.ID("field-" + f.Name),
			mi.Class(inputClass(hasError)),
			mi.Value(stringValue(value)),
			mi.Attr("inputmode", "decimal"),
			mi.Attr("pattern", pattern),
		}
		if f.Required {
			attrs = append(attrs, mi.Required())
		}
		return b.Input(attrs...)
	}
}

// renderAssetPicker implements the real "asset-picker" widget (T-19,
// 2026-08-04), replacing §8.3's MVP number-input stopgap. A live,
// server-backed search box (autoComplete.js, already vendored and
// re-themed -- web/static/vendor/VENDOR.md's Asset 6), generic over
// entity via f.XSeamSource's existing "entity:<name>" convention, not
// asset-specific despite the widget's own name: a future picker field
// for a different entity looks identical in shape.
//
// Two inputs, not one: a hidden field (f.Name, the actual submitted
// value, the raw ID) and a visible text field the user actually
// searches in. Kept deliberately separate rather than trying to make
// one input serve both roles -- the visible field's content is a
// human-readable label, the submitted value is an integer ID, and
// conflating them would mean parsing free text back into an ID on
// submit, an unnecessary and fragile step this design avoids entirely.
//
// Security design follows the exact lesson T-63/T-64 (this session)
// established: the current value's label (a real record's own name --
// genuinely user-editable, same shape of risk as the Map page's asset
// names or the FSM editor's machine names) is NEVER interpolated into
// this widget's own inline JS. It's resolved entirely client-side, via
// a fetch to /picker/search's own id= lookup mode, and written into the
// visible input via a safe DOM property assignment (search.value = ...
// -- not innerHTML, not a template string built from it), never
// touching this Go code's own string-building at all. Only f.Name and
// f.XSeamSource -- schema-authored, server-controlled, never user-
// submitted -- are interpolated into the script itself, the same
// trusted-by-provenance reasoning already established for the other
// (confirmed safe) mi.Raw sites audited after T-64.
//
// Self-reference/cycle prevention: the search endpoint's exclude=
// parameter exists (asset_picker_search.go) but is deliberately not
// wired into this widget's own JS yet -- an asset editing its own
// parent_id could, today, select itself. Considered wiring it via a
// "current record ID" passed through RenderContext, but a naive
// always-exclude would be actively wrong for a cross-entity picker
// (e.g. a Sensor's own ID excluded from an Asset search would hide an
// unrelated asset that happens to share that numeric ID -- different
// entities, different id spaces, no real collision risk, but the
// exclude wouldn't know that without also comparing the picker's own
// entity source against the record being edited's entity, which
// RenderContext doesn't carry today). A silently-hidden legitimate
// result is a worse, subtler bug than an operator being able to select
// an obviously-wrong self-referencing parent and immediately noticing.
// Left unwired rather than rushed -- matches T-19's own tracked note
// that cycle/self-reference handling remains undecided, not silently
// resolved here.
func renderAssetPicker(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		fieldID := "field-" + f.Name
		searchID := "picker-search-" + f.Name

		return b.Div(mi.Class("space-y-1"),
			b.Link(mi.Rel("stylesheet"), mi.Href("/static/vendor/autocomplete@10.2.10.css")),
			b.Link(mi.Rel("stylesheet"), mi.Href("/static/css/autocomplete-seam.css")),
			b.Script(mi.Src("/static/vendor/autocomplete@10.2.10.min.js")),
			b.Input(mi.Type("hidden"), mi.Name(f.Name), mi.ID(fieldID), mi.Value(stringValue(value))),
			b.Input(mi.Type("text"), mi.ID(searchID), mi.Class(inputClass(hasError)),
				mi.Attr("autocomplete", "off"),
				mi.Attr("placeholder", resolveWidgetChrome(e, rc, "field.asset_picker.placeholder", "Search…"))),
			b.Script(mi.Raw(fmt.Sprintf(`(function(){
  var fieldName = %q, entitySource = %q;
  var hidden = document.getElementById(%q);
  var search = document.getElementById(%q);
  var suppressClear = false;

  function resolveExisting() {
    if (!hidden.value) return;
    fetch('/picker/search?entity=' + encodeURIComponent(entitySource) + '&id=' + encodeURIComponent(hidden.value))
      .then(function(r){ return r.json(); })
      .then(function(results){
        if (results && results.length > 0) {
          suppressClear = true;
          search.value = results[0].label;
          suppressClear = false;
        }
      })
      .catch(function(){});
  }
  resolveExisting();

  var ac = new autoComplete({
    selector: '#' + %q,
    placeHolder: search.placeholder,
    debounce: 250,
    threshold: 1,
    data: {
      src: async function(query) {
        var url = '/picker/search?entity=' + encodeURIComponent(entitySource) + '&q=' + encodeURIComponent(query);
        var resp = await fetch(url);
        if (!resp.ok) return [];
        return await resp.json();
      },
      keys: ['label']
    },
    resultsList: { maxResults: 20 },
    resultItem: {
      element: function(item, data) { item.textContent = data.value.label; },
      highlight: true
    },
    events: {
      input: {
        selection: function(event) {
          var selection = event.detail.selection.value;
          hidden.value = selection.id;
          suppressClear = true;
          search.value = selection.label;
          suppressClear = false;
          // Notify any listeners (e.g. the issue template loader) that the
          // underlying selection changed. The input event this widget
          // otherwise relies on is not fired by programmatic value writes.
          hidden.dispatchEvent(new Event('change', { bubbles: true }));
        }
      }
    }
  });

  // A stale hidden value must not silently survive the user typing
  // something new without selecting a fresh result -- otherwise the
  // form would submit whatever was previously selected while the
  // visible box shows unrelated, unselected text. Only clears on
  // genuine user typing (suppressClear guards the two programmatic
  // value assignments above, which don't fire native "input" events
  // in standard browsers anyway, but guarding explicitly costs nothing
  // and doesn't depend on that assumption holding forever).
  search.addEventListener('input', function() {
    if (suppressClear) return;
    if (hidden.value !== '') {
      hidden.value = '';
      hidden.dispatchEvent(new Event('change', { bubbles: true }));
    }
  });
})();`, f.Name, f.XSeamSource, fieldID, searchID, searchID))),
		)
	}
}

// renderAnyPicker is the "any-picker" widget (T-97): a hidden id plus a
// live-search box, generic over x-seam-source "entity:<name>" -- the same
// idea as renderAssetPicker, which stays registered and untouched (asset
// forms keep using it). Unlike asset-picker it is safe inside repeatable
// rows: its markup is inert and carries data attributes, and ONE shared
// script (SeamPicker in web/static/js/app.js) initialises every picker on
// the page, and the ones in rows added later (SeamRepeatable.add calls
// SeamPicker.init on the new row). Nothing is looked up by a per-widget id,
// so the ids a repeatable row repeats cannot make two pickers collide.
//
// With f.XSeamSelector set (a server-side Selector source key, e.g.
// "users") a Browse button opens the Selector dialog; the chosen record
// comes back through the 'seam:selector-chosen' event, which SeamPicker
// matches to this widget by a generated data-pkid and applies by property
// assignment (never as HTML). Only schema-authored strings (entity source,
// selector key, chrome label) appear in attributes, escaped by minty; a
// record's label only travels through the event. A current value appears
// only in the hidden input's value attribute.
func renderAnyPicker(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		attrs := []interface{}{
			mi.Class("space-y-1"),
			mi.Attr("data-seam-picker", "1"),
			mi.Attr("data-entity", f.XSeamSource),
		}
		if f.XSeamSelector != "" {
			attrs = append(attrs, mi.Attr("data-selector", f.XSeamSelector))
		}
		attrs = append(attrs,
			b.Link(mi.Rel("stylesheet"), mi.Href("/static/vendor/autocomplete@10.2.10.css")),
			b.Link(mi.Rel("stylesheet"), mi.Href("/static/css/autocomplete-seam.css")),
			b.Script(mi.Src("/static/vendor/autocomplete@10.2.10.min.js")),
			b.Input(mi.Type("hidden"), mi.Name(f.Name), mi.ID("field-"+f.Name), mi.Value(stringValue(value))),
			b.Input(mi.Type("text"), mi.ID("picker-search-"+f.Name), mi.Class(inputClass(hasError)),
				mi.Attr("autocomplete", "off"),
				mi.Attr("placeholder", resolveWidgetChrome(e, rc, "field.asset_picker.placeholder", "Search\u2026"))),
		)
		if f.XSeamSelector != "" {
			label := resolveWidgetChrome(e, rc, "field.any_picker.browse", "Browse\u2026")
			attrs = append(attrs, b.Button(mi.Type("button"),
				mi.Class("text-sm text-indigo-600 dark:text-indigo-400 hover:underline"),
				mi.Attr("data-title", label),
				mi.Attr("onclick", "SeamPicker.browse(this)"),
				label,
			))
		}
		// Initialise now for server-rendered pickers; rows added later are
		// initialised by SeamRepeatable.add. Idempotent.
		attrs = append(attrs, b.Script(mi.Raw(`(function(){function go(){if(window.SeamPicker){SeamPicker.init(document);}}if(document.readyState==='loading'){document.addEventListener('DOMContentLoaded',go);}else{go();}})();`)))
		return b.Div(attrs...)
	}
}

// renderDocumentLinks implements the "document-links" widget (§4.2's
// document-links entry): a dynamic list of {nombre, url, tipo} rows the
// operator can add and remove. Field naming deliberately matches the
// existing hand-coded DocumentLinksField's parallel-array convention
// (name_nombre[], name_url[], name_tipo[]) rather than a nested-object
// naming scheme — §13.3's migration is meant to convert forms without
// requiring their POST handlers to change in the same pass, and the
// existing handlers already parse this exact field-name shape.
// Generalised over f.Items rather than hardcoding nombre/url/tipo, so
// it isn't silently wrong if a schema ever orders or renames them.
func renderDocumentLinks(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		rows := arrayOfMaps(value)
		if len(rows) == 0 {
			rows = []map[string]interface{}{{}} // always at least one empty row, matching DocumentLinksField
		}
		containerID := "field-" + f.Name + "-container"
		var rowNodes []interface{}
		for _, row := range rows {
			rowNodes = append(rowNodes, documentLinkRow(b, f, row, e, rc))
		}
		addOnclick := fmt.Sprintf(`(function(){
  var c=document.getElementById(%s);
  var src=c._rowTemplate||c.querySelector('.doc-link-row');
  if(!src){return;}
  var t=src.cloneNode(true);
  t.querySelectorAll('input').forEach(function(i){i.value='';});
  c.appendChild(t);
})()`, jsStringLiteral(containerID))
		return b.Div(
			b.Div(append([]interface{}{mi.ID(containerID), mi.Class("space-y-2")}, rowNodes...)...),
			b.Button(mi.Type("button"),
				mi.Class("mt-1 text-sm text-indigo-600 dark:text-indigo-400 hover:underline"),
				mi.Attr("onclick", addOnclick),
				resolveWidgetChrome(e, rc, "action.add_link", "+ Agregar enlace"),
			),
		)
	}
}

func documentLinkRow(b *mi.Builder, f Field, row map[string]interface{}, e *FormEngine, rc RenderContext) mi.Node {
	const inputClass = "px-2 py-1 border border-gray-300 dark:border-gray-600 rounded text-sm bg-white dark:bg-gray-700 text-gray-900 dark:text-white flex-1"
	var inputs []interface{}
	for _, item := range f.Items {
		inputType := "text"
		if item.Format == "uri" {
			inputType = "url"
		}
		inputs = append(inputs, b.Input(
			mi.Type(inputType),
			mi.Name(f.Name+"_"+item.Name+"[]"),
			mi.Attr("placeholder", resolveDocumentLinkItemLabel(e, rc, item.Name, item.Title)),
			mi.Value(stringValue(row[item.Name])),
			mi.Class(inputClass),
		))
	}
	inputs = append(inputs, b.Button(mi.Type("button"),
		mi.Class("text-red-500 hover:text-red-700 text-sm px-2"),
		mi.Attr("onclick", rowRemoveOnclick("doc-link-row")),
		"\u2715",
	))
	return b.Div(append([]interface{}{mi.Class("flex gap-2 items-start doc-link-row")}, inputs...)...)
}

// arrayOfMaps normalises an array-field's current value (typically
// []interface{} of map[string]interface{}, the natural shape for a
// JSON-persisted array of objects) into []map[string]interface{}. A
// nil, wrongly-typed, or empty value returns an empty (not nil) slice
// so callers can rely on len() without a separate nil check.
func arrayOfMaps(value interface{}) []map[string]interface{} {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

// arrayOfStrings normalises an array-field's current value (typically
// []interface{} of plain strings, the natural JSON-unmarshalled shape
// for a scalar array) into []string. Mirrors arrayOfMaps' defensive
// shape -- nil/wrongly-typed/empty all return an empty, non-nil slice.
// Non-string elements are skipped rather than stringified, matching
// multi-select's own use (option values are always schema-declared
// strings; a non-string element in a submitted array is malformed
// data, not a value worth guessing at).
func arrayOfStrings(value interface{}) []string {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// resolveOptionsForField resolves a field's option list from either
// f.Enum (static) or f.XSeamSource (dynamic, via e.Sources.Resolve) --
// the same two branches renderSelectWithExtraAttrs implements for the
// "select" widget. Deliberately a separate, self-contained function
// rather than an extraction from renderSelectWithExtraAttrs: that
// function is load-bearing for every existing select field across
// Assets/Sensors/Masters, and factoring it now would mean re-touching
// and re-verifying already-correct, already-tested code for no gain
// this session actually needs. Duplication of ~15 lines is the safer
// trade here; a future pass can unify both once multi-select has its
// own real-world mileage. errNode is non-nil (and options nil) exactly
// when the schema is misconfigured (neither enum nor x-seam-source, or
// a source resolution failure) -- callers render errNode in place of
// their control, matching renderSelectWithExtraAttrs's own inline-
// error convention rather than silently rendering nothing.
func resolveOptionsForField(ctx context.Context, e *FormEngine, f Field, rc RenderContext, b *mi.Builder) (options []Option, errNode mi.Node) {
	switch {
	case f.XSeamSource != "":
		if e.Sources == nil {
			return nil, b.P(mi.Class("text-xs text-red-600"),
				fmt.Sprintf("formengine: field %q needs x-seam-source %q but no SourceResolver is configured", f.Name, f.XSeamSource))
		}
		resolved, err := e.Sources.Resolve(ctx, f.XSeamSource)
		if err != nil {
			return nil, b.P(mi.Class("text-xs text-red-600"),
				fmt.Sprintf("formengine: resolving %q: %v", f.XSeamSource, err))
		}
		return resolved, nil
	case len(f.Enum) > 0:
		options = make([]Option, 0, len(f.Enum))
		for _, v := range f.Enum {
			options = append(options, Option{Value: v, Label: resolveEnumOptionLabel(e, rc, i18nFieldName(f), v)})
		}
		return options, nil
	default:
		return nil, b.P(mi.Class("text-xs text-red-600"),
			fmt.Sprintf("formengine: field %q has widget \"multi-select\" but no enum or x-seam-source", f.Name))
	}
}

// renderMultiSelect implements the "multi-select" widget (T-10's
// "DomainMultiSelect" gap -- docs/ORDER_ENGINE_INTEGRATION_NOTES.md §4:
// "no hay ningún widget de selección múltiple"). Renders as a checkbox
// group rather than a native <select multiple> -- the native control's
// ctrl/cmd-click multi-selection UX is not discoverable and is widely
// considered an accessibility/usability dead end; a checkbox list needs
// no new vendored dependency and matches this codebase's existing
// preference for plain, explicit controls over cleverness.
//
// Submission shape matches document-links/repeatable's own established
// array convention: repeated form keys, f.Name+"[]", one per checked
// option -- not a single delimited string. Current value arrives as
// []interface{} of strings (arrayOfStrings), the same JSON-array shape
// document-links/repeatable use for their own array values.
func renderMultiSelect(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		options, errNode := resolveOptionsForField(ctx, e, f, rc, b)
		if errNode != nil {
			return errNode
		}
		selected := make(map[string]bool, len(options))
		for _, v := range arrayOfStrings(value) {
			selected[v] = true
		}
		wrapClass := "space-y-1"
		if hasError {
			wrapClass += " rounded-lg border border-red-500 p-2"
		}
		var rows []interface{}
		for i, opt := range options {
			inputID := fmt.Sprintf("field-%s-%d", f.Name, i)
			inputAttrs := []mi.Attribute{
				mi.Type("checkbox"),
				mi.Name(f.Name + "[]"),
				mi.ID(inputID),
				mi.Value(opt.Value),
				mi.Class("h-4 w-4 rounded border-gray-300 text-indigo-600 focus:ring-indigo-500"),
			}
			if selected[opt.Value] {
				inputAttrs = append(inputAttrs, mi.Checked())
			}
			rows = append(rows, b.Label(mi.Class("flex items-center gap-2"),
				b.Input(inputAttrs...),
				b.Span(mi.Class("text-sm text-gray-700 dark:text-gray-300"), opt.Label),
			))
		}
		return b.Div(append([]interface{}{mi.Class(wrapClass)}, rows...)...)
	}
}

func renderTextarea(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		rows := f.XSeamRows
		if rows == 0 {
			rows = 3
		}
		attrs := []mi.Attribute{
			mi.Name(f.Name),
			mi.ID("field-" + f.Name),
			mi.Class(inputClass(hasError)),
			mi.Attr("rows", fmt.Sprintf("%d", rows)),
		}
		if f.Required {
			attrs = append(attrs, mi.Required())
		}
		// Builder.Textarea takes ...interface{}, not ...Attribute — unlike
		// Input, it does not flatten a []Attribute passed as one argument
		// (createElement type-switches each arg individually). Spread via
		// attrArgs, then append the text content as the final arg.
		return b.Textarea(append(attrArgs(attrs), stringValue(value))...)
	}
}

// attrArgs converts a []mi.Attribute to []interface{} so it can be spread
// into a Builder method whose signature is ...interface{} (Textarea,
// Select, Option, Label, Div, ...) rather than ...Attribute (Input only).
// createElement type-switches each argument individually and does not
// flatten a slice passed as a single argument — every such call needs
// this conversion, not just the ones that also mix in non-attribute args.
func attrArgs(attrs []mi.Attribute) []interface{} {
	args := make([]interface{}, len(attrs))
	for i, a := range attrs {
		args[i] = a
	}
	return args
}

func renderColor(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		v := stringValue(value)
		if v == "" {
			v = "#6366f1"
		}
		return b.Input(
			mi.Type("color"),
			mi.Name(f.Name),
			mi.ID("field-"+f.Name),
			mi.Class("mt-1 h-10 w-16 rounded border border-gray-300 dark:border-gray-600"),
			mi.Value(v),
		)
	}
}

// renderCheckboxLike covers both checkbox and toggle: semantically
// identical (§4.2 — toggle is "semánticamente equivalente a checkbox"),
// differing only in visual class. toggle=true selects the peer-checked
// pill styling; toggle=false is a plain HTML checkbox.
func renderCheckboxLike(toggle bool) widgetRenderer {
	return func(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
		return func(b *mi.Builder) mi.Node {
			checked, _ := value.(bool)
			inputAttrs := []mi.Attribute{
				mi.Type("checkbox"),
				mi.Name(f.Name),
				mi.ID("field-" + f.Name),
			}
			if checked {
				inputAttrs = append(inputAttrs, mi.Checked())
			}

			if !toggle {
				inputAttrs = append(inputAttrs, mi.Class("h-4 w-4 rounded border-gray-300 text-indigo-600 focus:ring-indigo-500"))
				return b.Label(mi.Class("inline-flex items-center gap-2"),
					b.Input(inputAttrs...),
					b.Span(mi.Class("text-sm text-gray-700 dark:text-gray-300"), f.Title),
				)
			}

			// Toggle pill: Tailwind peer-checked pattern, per §4.2.
			inputAttrs = append(inputAttrs, mi.Class("peer sr-only"))
			return b.Label(mi.Class("inline-flex items-center gap-2 cursor-pointer"),
				b.Input(inputAttrs...),
				b.Div(mi.Class("relative w-11 h-6 bg-gray-200 dark:bg-gray-600 rounded-full peer peer-checked:bg-indigo-600 "+
					"after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-white after:rounded-full "+
					"after:h-5 after:w-5 after:transition-all peer-checked:after:translate-x-5"),
				),
				b.Span(mi.Class("text-sm text-gray-700 dark:text-gray-300"), f.Title),
			)
		}
	}
}

// renderSelect covers static (enum) and dynamic (x-seam-source) selects
// (§4.2, §5). A field with neither is a schema-authoring error — this
// renders an inline error node rather than an empty, silently-broken
// select, since a select the operator can't diagnose from the rendered
// page is worse than an obviously-wrong one.
func renderSelect(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return renderSelectWithExtraAttrs(ctx, e, f, value, hasError, rc, nil)
}

// renderSelectWithExtraAttrs is renderSelect's shared core, with room
// for a caller to inject additional attributes onto the <select> itself
// — asset-type-select (§12's example: select + x-seam-source +
// x-seam-fk-popover + an htmx trigger for the dynamic identity/state
// fields) needs exactly the same option-resolution and base-attribute
// logic as a plain select, plus four more attributes. Extracted here
// rather than duplicated, so the two widgets can never silently drift
// apart on how options or the empty-option/required interaction work.
func renderSelectWithExtraAttrs(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext, extra []mi.Attribute) mi.H {
	return func(b *mi.Builder) mi.Node {
		var options []Option
		emptyLabel := resolveSelectPlaceholder(e, rc, "field.select.empty", "— Seleccionar —") // §5.3, enum default

		switch {
		case f.XSeamSource != "":
			emptyLabel = resolveSelectPlaceholder(e, rc, "field.select.empty_fk", "— Sin asignar —") // §5.3, FK default
			if e.Sources == nil {
				return b.P(mi.Class("text-xs text-red-600"),
					fmt.Sprintf("formengine: field %q needs x-seam-source %q but no SourceResolver is configured", f.Name, f.XSeamSource))
			}
			resolved, err := e.Sources.Resolve(ctx, f.XSeamSource)
			if err != nil {
				return b.P(mi.Class("text-xs text-red-600"),
					fmt.Sprintf("formengine: resolving %q: %v", f.XSeamSource, err))
			}
			options = resolved
		case len(f.Enum) > 0:
			for _, v := range f.Enum {
				options = append(options, Option{Value: v, Label: resolveEnumOptionLabel(e, rc, i18nFieldName(f), v)})
			}
		default:
			return b.P(mi.Class("text-xs text-red-600"),
				fmt.Sprintf("formengine: field %q has widget \"select\" but no enum or x-seam-source", f.Name))
		}

		current := stringValue(value)
		attrs := []mi.Attribute{
			mi.Name(f.Name),
			mi.ID("field-" + f.Name),
			mi.Class(inputClass(hasError)),
		}
		if f.Required {
			attrs = append(attrs, mi.Required())
		}
		if f.XSeamFKPopover != "" {
			// §7.2: lets initFKJumps() in app.js update the popover's
			// href in real time as the selection changes.
			attrs = append(attrs, mi.Attr("data-jump-base", f.XSeamFKPopover))
		}
		attrs = append(attrs, extra...)

		// Select's signature is ...interface{} (attrs and children mixed
		// positionally, like Div/Label) — build one arg list rather than
		// passing the attribute slice as a single argument. Options are
		// built as Nodes directly (via b, already in scope) rather than
		// as mi.H closures, since mi.H itself doesn't satisfy any of
		// createElement's recognised argument types either.
		args := attrArgs(attrs)
		// §5.3/§5.4: no empty option when the field is required.
		if !f.Required {
			args = append(args, optionNode(b, "", emptyLabel, current == ""))
		}
		if f.XSeamOptionGroups {
			args = append(args, groupedOptionNodes(b, e, rc, i18nFieldName(f), options, current)...)
		} else {
			for _, opt := range options {
				args = append(args, optionNode(b, opt.Value, opt.Label, opt.Value == current))
			}
		}

		return b.Select(args...)
	}
}

// renderAssetTypeSelect implements "asset-type-select" (§12's Asset
// schema example): a select — same option/FK-popover/required handling
// as any other — that also triggers an htmx reload of the
// asset-type-driven identity/state fields when changed. Attribute
// values matched against internal/ui/asset_detail.go's
// assetTypeSelectWithHTMX directly before writing this, not
// reconstructed from memory of what such a trigger "should" look like.
func renderAssetTypeSelect(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	extra := []mi.Attribute{
		mi.Attr("hx-get", "/assets/type-fields"),
		mi.Attr("hx-trigger", "change"),
		mi.Attr("hx-target", "#asset-type-fields"),
		mi.Attr("hx-include", "[name="+f.Name+"]"),
	}
	return renderSelectWithExtraAttrs(ctx, e, f, value, hasError, rc, extra)
}

func optionNode(b *mi.Builder, value, label string, selected bool) mi.Node {
	attrs := []mi.Attribute{mi.Value(value)}
	if selected {
		attrs = append(attrs, mi.Attr("selected", "selected"))
	}
	return b.Option(append(attrArgs(attrs), label)...)
}

// groupedOptionNodes renders options as <optgroup>-wrapped children for
// an XSeamOptionGroups field. Category is inferred from each option's
// own value, splitting on the first underscore (e.g.
// "distance_kilometers" groups under "distance") -- a naming
// convention, not a separate schema field, so no schema-format change
// was needed for this (T-36, 2026-08-02). Groups render in the order
// their first member first appears in the schema's own enum array,
// not alphabetically, so schema authors keep control of group order.
// A value with no underscore becomes its own single-option group,
// using the full value as the category -- a defined fallback rather
// than a panic, though XSeamOptionGroups is only meant for fields
// whose every value follows the convention.
func groupedOptionNodes(b *mi.Builder, e *FormEngine, rc RenderContext, fieldName string, options []Option, current string) []interface{} {
	type group struct {
		category string
		opts     []Option
	}
	var groups []group
	index := map[string]int{}
	for _, opt := range options {
		category := opt.Value
		if i := strings.IndexByte(opt.Value, '_'); i >= 0 {
			category = opt.Value[:i]
		}
		if gi, ok := index[category]; ok {
			groups[gi].opts = append(groups[gi].opts, opt)
		} else {
			index[category] = len(groups)
			groups = append(groups, group{category: category, opts: []Option{opt}})
		}
	}

	nodes := make([]interface{}, 0, len(groups))
	for _, g := range groups {
		groupLabel := resolveOptionGroupLabel(e, rc, fieldName, g.category)
		children := make([]interface{}, 0, len(g.opts)+1)
		children = append(children, mi.Attr("label", groupLabel))
		for _, opt := range g.opts {
			children = append(children, optionNode(b, opt.Value, opt.Label, opt.Value == current))
		}
		nodes = append(nodes, b.Optgroup(children...))
	}
	return nodes
}
