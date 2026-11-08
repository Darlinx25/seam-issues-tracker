package i18n

// Verifies the anonymous QR report keys actually resolve in all four locales.
// This is the runtime half of the invariant: a key present in the JSON is not
// the same thing as a key the Translator can find, and the fallback (the key
// verbatim) would silently ship English keys in the Spanish UI.

import (
	"strings"
	"testing"
)

func TestQRReportKeysResolve(t *testing.T) {
	tr, err := New("en")
	if err != nil {
		t.Fatalf("could not load the locales: %v", err)
	}

	keys := []string{
		"qr.report.title",
		"qr.report.anonymous_note",
		// The two badges of the asset card: the category of the issue and the
		// channel label the filter bar already uses.
		"issues.asset.category",
		"issues.asset.category.critico",
		"issues.asset.category.normal",
		"issues.filter.channel",
		"issues.channel.qr",
		"qr.report.contact_title",
		"qr.report.contact_name",
		"qr.report.contact_document",
		"qr.report.contact_phone",
		"qr.report.contact_email",
		"qr.report.problem_title",
		"qr.report.field_title",
		"qr.report.field_description",
		"qr.report.submit",
		"qr.report.error.contact_required",
		"qr.report.error.description_required",
		"qr.report.error.create_failed",
		"qr.report.done_title",
		"qr.report.done_message",
		"qr.report.done_hint",
	}

	for _, locale := range []string{"en", "es", "pt", "ja"} {
		translate := tr.ForLocale(locale)
		for _, key := range keys {
			if got := translate(key); got == key || strings.TrimSpace(got) == "" {
				t.Errorf("%s: %q did not resolve (got %q)", locale, key, got)
			}
		}
	}

	// The same key must not read the same in two different locales: a copy and
	// paste that forgot to translate is invisible otherwise.
	if en, es := tr.ForLocale("en")("qr.report.title"), tr.ForLocale("es")("qr.report.title"); en == es {
		t.Errorf("en and es render the same title %q", en)
	}
	if ja := tr.ForLocale("ja")("qr.report.title"); ja == tr.ForLocale("en")("qr.report.title") {
		t.Errorf("ja still renders the English title %q", ja)
	}
}
