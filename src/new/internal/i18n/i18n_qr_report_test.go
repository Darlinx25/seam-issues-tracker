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
		// The public tracking page, which replaced the old /done confirmation:
		// the reporter follows the issue through an opaque token, and every
		// label of that page must resolve in every locale.
		"qr.track.title",
		"qr.track.card_title",
		"qr.track.new_title",
		"qr.track.new_hint",
		"qr.track.issue_number",
		"qr.track.reported_at",
		"qr.track.step.reported",
		"qr.track.step.triaged",
		"qr.track.step.in_progress",
		"qr.track.step.resolved",
		"qr.track.step.closed",
		"qr.track.activity.title",
		"qr.track.activity.empty",
		"qr.track.activity.created",
		"qr.track.activity.triage",
		"qr.track.activity.start",
		"qr.track.activity.hold",
		"qr.track.activity.resume",
		"qr.track.activity.resolve",
		"qr.track.activity.close",
		"qr.track.activity.reject",
		"qr.track.activity.merge",
		"qr.track.link.title",
		"qr.track.link.hint",
		"qr.track.link.warning",
		"qr.track.link.copy",
		"qr.track.link.copied",
		"qr.track.error.not_found_title",
		"qr.track.error.not_found_message",
		"qr.track.error.expired_title",
		"qr.track.error.expired_message",
		"qr.track.duplicate_note",
		"qr.track.duplicate_open",
		// The operator-side card in the issue detail.
		"issues.tracking.title",
		"issues.tracking.hint",
		"issues.tracking.expires_at",
		"issues.tracking.expired",
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
	if en, es := tr.ForLocale("en")("qr.track.new_title"), tr.ForLocale("es")("qr.track.new_title"); en == es {
		t.Errorf("en and es render the same tracking banner %q", en)
	}
}
