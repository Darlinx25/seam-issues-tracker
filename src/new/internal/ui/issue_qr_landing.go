package ui

// Public landing for the links encoded in the per-asset QR codes (/s/{code}).
//
// Iteration 1 keeps this page read-only: the QR reporting channel (anonymous
// report form, check-in, suggested work orders) is iteration 2/3, so the page
// shows only what a person standing in front of the equipment needs to see -
// what the asset is, where it is, who is responsible for it and its category.
//
// The original page shipped two action buttons ("Check In", "Report Issue")
// with no handler behind them, and a "View full details" link to the
// authenticated asset page that sent anonymous scanners to the login screen.
// Neither is acceptable on a public page: the dead buttons are replaced by an
// explicit note, and the detail link is only rendered for viewers who can
// actually open it.

import (
	"fmt"

	mi "github.com/ha1tch/minty"
)

// issueQRScanPage renders the landing a scanned code opens. assetCategory is
// the canonical category of the asset ("critico"/"normal"); canViewDetails is
// true only when the request carries a session allowed to open the asset
// detail page.
func issueQRScanPage(data QRScanData, assetCategory string, canViewDetails bool, t func(key string, args ...any) string) mi.H {
	if t == nil {
		t = func(key string, args ...any) string { return key }
	}
	return func(b *mi.Builder) mi.Node {
		statusColor := "bg-green-500"
		statusText := t("asset.status.active")
		switch data.Status {
		case "alert":
			statusColor = "bg-red-500"
			statusText = t("asset.status.alert")
		case "silent":
			statusColor = "bg-yellow-500"
			statusText = t("asset.status.silent")
		case "lost":
			statusColor = "bg-gray-500"
			statusText = t("asset.status.lost")
		case "maintenance":
			statusColor = "bg-blue-500"
			statusText = t("dashboard.status.maintenance")
		default:
			statusText = data.Status
		}

		categoryLabel := ""
		if assetCategory != "" {
			categoryLabel = t("issues.asset.category." + assetCategory)
		}

		// Footer: the asset detail page is authenticated, so the link only
		// makes sense for a viewer who can open it. Anonymous scanners get a
		// sign-in link that brings them back here instead of a silent 302 to
		// the login page.
		var footer mi.H
		if canViewDetails {
			footer = func(b *mi.Builder) mi.Node {
				return b.Div(mi.Class("text-center"),
					b.A(
						mi.Href(fmt.Sprintf("/assets/%d", data.AssetID)),
						mi.Class("text-sm text-indigo-600 dark:text-indigo-400 hover:underline"),
						t("qr.view_full_details"),
					),
				)
			}
		} else {
			footer = func(b *mi.Builder) mi.Node {
				return b.Div(mi.Class("text-center"),
					b.A(
						mi.Href(fmt.Sprintf("/login?next=/assets/%d", data.AssetID)),
						mi.Class("text-sm text-indigo-600 dark:text-indigo-400 hover:underline"),
						t("qr.sign_in_for_details"),
					),
				)
			}
		}

		children := []interface{}{
			// Asset card
			b.Div(mi.Class("bg-white dark:bg-gray-800 rounded-xl shadow-lg overflow-hidden"),
				// Status bar
				b.Div(mi.Class("h-2 "+statusColor)),

				b.Div(mi.Class("p-6"),
					// Header
					b.Div(mi.Class("flex items-start justify-between mb-4"),
						b.Div(
							b.H1(mi.Class("text-2xl font-bold text-gray-900 dark:text-white"), data.AssetName),
							b.P(mi.Class("text-sm text-gray-500 dark:text-gray-400"), data.AssetType),
						),
						b.Div(mi.Class("px-3 py-1 rounded-full text-sm font-medium text-white "+statusColor), statusText),
					),

					// Details grid
					b.Div(mi.Class("grid grid-cols-2 gap-4 mb-6"),
						qrDetailItem(b, t("qr.field.location"), data.Location, "location_on"),
						qrDetailItem(b, t("sensors.column.last_seen"), data.LastSeen, "schedule"),
						qrDetailItem(b, t("qr.field.assigned_to"), data.AssignedTo, "person"),
						qrDetailItem(b, t("issues.asset.category"), categoryLabel, "category"),
						qrDetailItem(b, t("qr.field.code"), data.Code, "qr_code_2"),
					),

					// Recent activity
					b.Div(mi.Class("border-t border-gray-200 dark:border-gray-700 pt-4"),
						b.H3(mi.Class("text-sm font-medium text-gray-700 dark:text-gray-300 mb-2"), t("qr.recent_activity")),
						qrActivityList(b, data.RecentActivity, t),
					),
				),
			),

			// Read-only notice: the reporting channel is not available yet.
			b.Div(mi.Class("flex items-start gap-3 rounded-lg border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-800"),
				b.Span(mi.Class("material-icons text-indigo-500 dark:text-indigo-400"), "info"),
				b.P(mi.Class("text-sm text-gray-600 dark:text-gray-300"), t("qr.read_only_note")),
			),

			footer(b),
		}

		return b.Div(append([]interface{}{mi.Class("space-y-6")}, children...)...)
	}
}
