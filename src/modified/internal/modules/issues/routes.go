// Package issues provides the Issue Tracker module's
// registry.Module.MountRoutes closure (2026-08-05). A simple,
// always-on module -- not gated like Work Orders was, since nothing
// here needed that playground framing.
package issues

import (
	"net/http"

	"github.com/ha1tch/seam-ui/internal/ui"
)

// MountRoutes returns a modules.Module.MountRoutes-compatible closure
// that registers the Issue Tracker module's routes via router's
// existing handlers and middleware, unchanged.
func MountRoutes(router *ui.Router) func(mux *http.ServeMux) {
	return func(mux *http.ServeMux) {
		router.MountIssuesRoutes(mux)
	}
}
