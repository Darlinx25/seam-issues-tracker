package ui

import (
	"testing"

	"github.com/ha1tch/seam-ui/internal/modules"
)

// TestActiveModuleSidebarItems_NestedPageLightsOnlyItsOwnItem is the
// regression guard for /issues/new lighting up both Incidencias
// ("/issues") and Nueva Incidencia ("/issues/new") at once. Exactly one
// entry may be active, and it is the longest matching href.
func TestActiveModuleSidebarItems_NestedPageLightsOnlyItsOwnItem(t *testing.T) {
	cfg := Config{
		VisibleModules: []modules.Module{
			{
				ID:   "assets",
				Path: "/",
				SidebarItems: []modules.SidebarItem{
					{Label: "nav.section.operational", Section: true},
					{Label: "nav.dashboard", URL: "/", Icon: "dashboard"},
					{Label: "nav.issues", URL: "/issues", Icon: "report_problem"},
					{Label: "nav.issue_new", URL: "/issues/new", Icon: "add_circle"},
				},
			},
		},
	}

	cases := []struct {
		path string
		want string
	}{
		{"/issues/new", "/issues/new"},
		{"/issues/42", "/issues"},
		{"/issues", "/issues"},
		{"/", "/"},
	}
	for _, c := range cases {
		var active []string
		for _, item := range activeModuleSidebarItems(c.path, cfg, nil) {
			if item.Active {
				active = append(active, item.URL)
			}
		}
		if len(active) != 1 || active[0] != c.want {
			t.Errorf("activeModuleSidebarItems(%q) active = %v, want exactly [%q]", c.path, active, c.want)
		}
	}
}

// TestNavItemMatchesPath_UsesSegmentBoundary confirms the shared matcher
// keeps a sibling route such as "/issues-archive" from matching "/issues",
// while still accepting descendants of a real item.
func TestNavItemMatchesPath_UsesSegmentBoundary(t *testing.T) {
	cases := []struct {
		path, href string
		want       bool
	}{
		{"/", "/", true},
		{"/issues", "/issues", true},
		{"/issues/42", "/issues", true},
		{"/issues/new", "/issues/new", true},
		{"/issues-archive", "/issues", false},
		{"/issuesx", "/issues", false},
	}
	for _, c := range cases {
		if got := navItemMatchesPath(c.path, c.href); got != c.want {
			t.Errorf("navItemMatchesPath(%q, %q) = %v, want %v", c.path, c.href, got, c.want)
		}
	}
}
