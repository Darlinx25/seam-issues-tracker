package formengine

import (
	"context"
	"fmt"
	"strings"

	mi "github.com/ha1tch/minty"
	"github.com/ha1tch/seam-ui/internal/querypicker"
)

func parseQueryPicker(input, jsonType, query string, columns []querypicker.Column, valueField string, labelFields []string, limit int, dropdown string) (*querypicker.Spec, error) {
	if input != "query-picker" {
		return nil, nil
	}
	query = strings.TrimSpace(query)
	valueField = strings.TrimSpace(valueField)
	if query == "" {
		return nil, fmt.Errorf("x-seam-query is required for query-picker")
	}
	if valueField == "" {
		return nil, fmt.Errorf("x-seam-value-field is required for query-picker")
	}
	if len(labelFields) == 0 {
		return nil, fmt.Errorf("x-seam-label-fields must contain at least one field")
	}
	for i, field := range labelFields {
		if strings.TrimSpace(field) == "" {
			return nil, fmt.Errorf("x-seam-label-fields[%d] is empty", i)
		}
	}
	if len(columns) > 12 {
		return nil, fmt.Errorf("x-seam-columns has %d entries; the limit is 12", len(columns))
	}
	for i, column := range columns {
		if strings.TrimSpace(column.Name) == "" {
			return nil, fmt.Errorf("x-seam-columns[%d].name is empty", i)
		}
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("x-seam-limit must be between 1 and 200, got %d", limit)
	}
	switch dropdown {
	case "", "list", "table":
	default:
		return nil, fmt.Errorf("x-seam-dropdown must be \"list\" or \"table\", got %q", dropdown)
	}
	return &querypicker.Spec{
		Query:       query,
		ValueField:  valueField,
		LabelFields: labelFields,
		Columns:     columns,
		Limit:       limit,
		Dropdown:    dropdown,
	}, nil
}

func renderQueryPicker(ctx context.Context, e *FormEngine, f Field, value interface{}, hasError bool, rc RenderContext) mi.H {
	return func(b *mi.Builder) mi.Node {
		message := resolveWidgetChrome(e, rc, "field.query_picker.unavailable", "Query picker is not available in this build")
		return b.Div(
			mi.Class("space-y-1"),
			mi.Attr("data-seam-widget", "query-picker"),
			b.Input(mi.Type("hidden"), mi.Name(f.Name), mi.ID("field-"+f.Name), mi.Value(stringValue(value))),
			b.P(mi.Class("text-xs text-red-600"), message),
		)
	}
}
