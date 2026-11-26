package ui

import (
	"context"
	"net/http"
	"strings"
)

type issueAnonymousContactSettings struct {
	Ask      bool
	Required bool
}

func defaultIssueAnonymousContactSettings() issueAnonymousContactSettings {
	return issueAnonymousContactSettings{Ask: true, Required: true}
}

func issueAnonymousContactBool(v any, fallback bool) bool {
	switch value := v.(type) {
	case bool:
		return value
	case int:
		return value != 0
	case int64:
		return value != 0
	case float64:
		return value != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(value))
		switch s {
		case "true", "1", "on", "yes":
			return true
		case "false", "0", "off", "no", "":
			return false
		}
	}
	return fallback
}

func issueAnonymousContactSettingsFromRow(row map[string]any) issueAnonymousContactSettings {
	result := defaultIssueAnonymousContactSettings()
	if v, ok := row["ask_contact"]; ok {
		result.Ask = issueAnonymousContactBool(v, true)
	}
	if v, ok := row["require_contact"]; ok {
		result.Required = issueAnonymousContactBool(v, true)
	}
	result.Required = result.Ask && result.Required
	return result
}

func (h *UIHandler) issueAnonymousContactSettingsForCode(ctx context.Context, code string) issueAnonymousContactSettings {
	if h.xoluClient == nil || !validIssueQRCode(code) {
		return defaultIssueAnonymousContactSettings()
	}
	res, err := h.xoluClient.OQL(ctx,
		"SELECT TOP 1 ask_contact, require_contact FROM "+issueQRCodesEntity+
			" WHERE code = '"+escapeOQL(code)+"'")
	if err != nil || res == nil || len(res.Data) == 0 {
		return defaultIssueAnonymousContactSettings()
	}
	return issueAnonymousContactSettingsFromRow(res.Data[0])
}

func parseIssueAnonymousContactSettings(r *http.Request) issueAnonymousContactSettings {
	result := defaultIssueAnonymousContactSettings()
	if err := r.ParseForm(); err != nil {
		return result
	}
	if r.FormValue("qr_contact_form") != "1" {
		return result
	}
	result.Ask = r.FormValue("qr_contact_ask") == "on"
	result.Required = result.Ask && r.FormValue("qr_contact_required") == "on"
	return result
}
