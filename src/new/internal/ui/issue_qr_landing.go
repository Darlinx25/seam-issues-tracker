package ui

// Public landing for the links encoded in the per-asset QR codes (/s/{code}).
//
// The landing IS the anonymous report form: the scanner sees which asset the
// code belongs to (name, type, location and the category frozen into the code)
// and fills in their contact details plus the problem description. The form
// posts to /s/{code}/report, which creates the issue with channel "qr" and no
// reported_by, so a person without a Seam account can report a fault.
//
// Priority and severity are not asked on this channel: triage is the operator's
// call, not the reporter's, so the report uses the same defaults as the in-app
// form (medium / minor).
//
// The fields of the template assigned to the asset's type are rendered by the
// server: PublicLayout carries no HTMX, so there is no partial to swap them in
// after the fact. They travel inside the same form as the fixed fields.

import (
	mi "github.com/ha1tch/minty"
)

// issueQRReportInputClass is the input look shared by every field of the form.
const issueQRReportInputClass = "mt-1 w-full px-3 py-2 border border-gray-300 dark:border-gray-600 rounded-lg bg-white dark:bg-gray-700 text-gray-900 dark:text-white text-sm focus:ring-2 focus:ring-indigo-500"

// issueQRReportPageData is everything the anonymous report form renders.
//
// The values survive a rejected submission so the scanner never retypes the
// report, and Errors is keyed by field name ("title", "description",
// "contact_email", a template field key, ...) so each message lands under its
// own field.
type issueQRReportPageData struct {
	Code      string
	AssetName string
	AssetType string

	// AssetLocation is the free-text location stored on the asset, already
	// denormalized the way the asset detail reads it.
	AssetLocation string

	// AssetCategory is the canonical category of the asset ("critico" or
	// "normal"), frozen into the code when the QR was printed.
	AssetCategory string

	ContactName     string
	ContactDocument string
	ContactPhone    string
	ContactEmail    string
	Title           string
	Description     string

	// TemplateFields is the rendered block of the template assigned to the
	// asset's type, or nil when no template applies.
	TemplateFields mi.H

	Errors map[string]string
}

// issueQRReportPage renders the anonymous report form a scanned code opens.
func issueQRReportPage(data issueQRReportPageData, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	return func(b *mi.Builder) mi.Node {
		// A legacy code carries no category and the landing falls back to the
		// asset; a payload without either still has to render a known badge.
		category := data.AssetCategory
		if category != "critico" && category != "normal" {
			category = "normal"
		}
		categoryVariant := "info"
		if category == "critico" {
			categoryVariant = "danger"
		}

		assetSubtitle := data.AssetType
		if data.AssetLocation != "" {
			if assetSubtitle != "" {
				assetSubtitle += " · "
			}
			assetSubtitle += data.AssetLocation
		}

		problemFields := []interface{}{
			issueQRReportTextInput(b, "title", t("qr.report.field_title"), "text", data.Title, data.Errors["title"]),
			issueQRReportTextarea(b, "description", t("qr.report.field_description"), data.Description, data.Errors["description"]),
		}
		if data.TemplateFields != nil {
			problemFields = append(problemFields, data.TemplateFields(b))
		}

		return b.Div(mi.Class("space-y-4"),
			// Heading: the scanner is told up front that the report is
			// anonymous, so nobody abandons the form looking for a login.
			b.Div(mi.Class("text-center"),
				b.H1(mi.Class("text-xl font-bold text-gray-900 dark:text-white"), t("qr.report.title")),
				b.P(mi.Class("mt-1 text-sm text-gray-500 dark:text-gray-400"), t("qr.report.anonymous_note")),
			),

			// Asset card: the scanner confirms the code belongs to the
			// equipment in front of them before writing anything.
			Card("", func(b *mi.Builder) mi.Node {
				return b.Div(mi.Class("flex items-start gap-3"),
					b.Div(mi.Class("w-10 h-10 rounded-lg bg-indigo-50 dark:bg-indigo-900/50 flex items-center justify-center flex-shrink-0"),
						b.Span(mi.Class("material-icons text-indigo-600 dark:text-indigo-400"), "engineering"),
					),
					b.Div(mi.Class("min-w-0"),
						b.Div(mi.Class("font-medium text-gray-900 dark:text-white"), data.AssetName),
						b.Div(mi.Class("text-sm text-gray-500 dark:text-gray-400"), assetSubtitle),
						b.Div(mi.Class("mt-2 flex flex-wrap gap-2"),
							Badge(t("issues.asset.category."+category), categoryVariant)(b),
							// The channel label and token the filter bar already
							// use, not a second word for the same concept.
							Badge(t("issues.filter.channel")+": "+t("issues.channel.qr"), "info")(b),
						),
					),
				)
			})(b),

			// One form: the two cards and the submit travel together, so the
			// scanner answers everything and sends once.
			b.Form(mi.Method("POST"), mi.Action("/s/"+data.Code+"/report"), mi.Class("space-y-4"),
				Card(t("qr.report.contact_title"), func(b *mi.Builder) mi.Node {
					return b.Div(mi.Class("space-y-4"),
						issueQRReportTextInput(b, "contact_name", t("qr.report.contact_name"), "text", data.ContactName, data.Errors["contact_name"]),
						issueQRReportTextInput(b, "contact_document", t("qr.report.contact_document"), "text", data.ContactDocument, data.Errors["contact_document"]),
						b.Div(mi.Class("grid grid-cols-1 sm:grid-cols-2 gap-4"),
							issueQRReportTextInput(b, "contact_phone", t("qr.report.contact_phone"), "tel", data.ContactPhone, data.Errors["contact_phone"]),
							issueQRReportTextInput(b, "contact_email", t("qr.report.contact_email"), "email", data.ContactEmail, data.Errors["contact_email"]),
						),
					)
				})(b),
				Card(t("qr.report.problem_title"), func(b *mi.Builder) mi.Node {
					return b.Div(append([]interface{}{mi.Class("space-y-4")}, problemFields...)...)
				})(b),
				b.Button(mi.Type("submit"), mi.Class("w-full px-4 py-2 bg-indigo-600 text-white rounded-lg hover:bg-indigo-700 font-medium"),
					t("qr.report.submit"),
				),
			),
		)
	}
}

// issueQRReportLabel renders one field label with the required marker every
// field of this form carries.
func issueQRReportLabel(b *mi.Builder, forID, text string) mi.Node {
	return b.Label(mi.For(forID), mi.Class("block text-sm font-medium text-gray-700 dark:text-gray-300"),
		b.Text(text),
		b.Span(mi.Class("text-red-500"), " *"),
	)
}

// issueQRReportFieldError renders the inline error of one field, or nothing.
func issueQRReportFieldError(b *mi.Builder, message string) mi.Node {
	if message == "" {
		// A text node, not an empty span: the page must not carry markup that
		// has no content.
		return b.Text("")
	}
	return b.P(mi.Class("mt-1 text-xs text-red-600 dark:text-red-400"), message)
}

// issueQRReportTextInput renders one labelled single-line input. inputType is
// the HTML input type, so the browser enforces "required" and the email shape
// before the form leaves the device; the handler validates them again.
func issueQRReportTextInput(b *mi.Builder, name, label, inputType, value, errMsg string) mi.Node {
	return b.Div(
		issueQRReportLabel(b, name, label),
		b.Input(
			mi.Type(inputType),
			mi.ID(name),
			mi.Name(name),
			mi.Value(value),
			mi.Required(),
			mi.Class(issueQRReportInputClass),
		),
		issueQRReportFieldError(b, errMsg),
	)
}

// issueQRReportTextarea renders the labelled description field.
func issueQRReportTextarea(b *mi.Builder, name, label, value, errMsg string) mi.Node {
	return b.Div(
		issueQRReportLabel(b, name, label),
		b.Textarea(
			mi.ID(name),
			mi.Name(name),
			mi.Rows(4),
			mi.Required(),
			mi.Class(issueQRReportInputClass),
			b.Text(value),
		),
		issueQRReportFieldError(b, errMsg),
	)
}
