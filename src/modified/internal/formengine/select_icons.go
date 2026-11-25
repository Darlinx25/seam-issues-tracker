package formengine

import (
	"fmt"
	"regexp"

	mi "github.com/ha1tch/minty"
)

// Icons in a select's options.
//
// A select field can show an icon beside the text of any of its options, in the
// closed box and in the list:
//
//	"priority": {"type": "string", "x-seam-input": "select",
//	  "enum": ["low", "high"],
//	  "x-seam-option-icons": {"low": "arrow_downward", "high": "priority_high"},
//	  "x-seam-icon-position": "right"}
//
// x-seam-option-icons maps an option value to a Material Icons name (an option
// with no entry shows no icon). x-seam-icon-position is "left" (the default:
// the icon before the text) or "right" (after it). An option that comes from a
// SourceResolver can carry its own Option.Icon. The icon is drawn by SeamSelect,
// the combobox that replaces the native select (internal/formengine/options.go
// SearchableSelect): a native select cannot show one, so with that switched off
// the options keep their text and the icons are not shown.
//
// Icon names are checked twice, because they end up in markup: ParseSchema
// refuses a schema whose icon name is not lower-case letters, digits and
// underscores (so a typo is found when the schema is written), and a name from a
// resolver, which is data, is silently dropped when it does not match. With no
// icons configured the output is exactly what it was.

var iconNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// maxOptionIcons bounds the number of entries of x-seam-option-icons.
const maxOptionIcons = 1000

// parseOptionIcons checks x-seam-option-icons.
func parseOptionIcons(m map[string]string) (map[string]string, error) {
	if len(m) > maxOptionIcons {
		return nil, fmt.Errorf("x-seam-option-icons has %d entries; the limit is %d", len(m), maxOptionIcons)
	}
	for v, icon := range m {
		if !iconNamePattern.MatchString(icon) {
			return nil, fmt.Errorf("x-seam-option-icons: %q for option %q is not an icon name (lower-case letters, digits and underscores)", icon, v)
		}
	}
	return m, nil
}

// parseIconPosition checks x-seam-icon-position; "" is the default, left.
func parseIconPosition(s string) (string, error) {
	switch s {
	case "", "left", "right":
		return s, nil
	}
	return "", fmt.Errorf(`x-seam-icon-position must be "left" or "right", got %q`, s)
}

// safeIcon returns name when it is an icon name and "" otherwise.
func safeIcon(name string) string {
	if iconNamePattern.MatchString(name) {
		return name
	}
	return ""
}

// optionIcon is the icon of one of the field's enum values ("" for none).
func (f Field) optionIcon(value string) string {
	return safeIcon(f.XSeamOptionIcons[value])
}

// optionsHaveIcons reports whether any option has a usable icon.
func optionsHaveIcons(options []Option) bool {
	for _, o := range options {
		if safeIcon(o.Icon) != "" {
			return true
		}
	}
	return false
}

// parseOptionDisabled converts the schema list into a lookup set.
func parseOptionDisabled(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// optionDisabled reports whether the field marks value as disabled.
func (f Field) optionDisabled(value string) bool {
	return f.XSeamOptionDisabled != nil && f.XSeamOptionDisabled[value]
}

// optionNodeIcon is optionNode with an icon/disabled state: data-icon on the
// <option> is read by SeamSelect; disabled options are visible but not selectable.
func optionNodeIcon(b *mi.Builder, value, label string, selected, disabled bool, icon string) mi.Node {
	attrs := []mi.Attribute{mi.Value(value)}
	if selected {
		attrs = append(attrs, mi.Attr("selected", "selected"))
	}
	if disabled {
		attrs = append(attrs, mi.Attr("disabled", "disabled"))
	}
	if ic := safeIcon(icon); ic != "" {
		attrs = append(attrs, mi.Attr("data-icon", ic))
	}
	return b.Option(append(attrArgs(attrs), label)...)
}
