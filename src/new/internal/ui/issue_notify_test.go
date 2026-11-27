package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	xolu "github.com/ha1tch/xolu/pkg/client"
)

func notifyTestTranslator(key string, args ...any) string {
	templates := map[string]string{
		"issues.notification.subject":                        "Issue \"%s\": %s",
		"issues.notification.body":                           "Issue: **%s**\nAsset: **%s**\nStatus: **%s**",
		"issues.notification.body_no_asset":                  "Issue: **%s**\nStatus: **%s**",
		"issues.notification.body_with_link":                 "Issue: **%s**\nAsset: **%s**\nStatus: **%s**\n\nTrack it here: %s",
		"issues.notification.body_no_asset_with_link":        "Issue: **%s**\nStatus: **%s**\n\nTrack it here: %s",
		"issues.notification.created.subject":                "Issue \"%s\": %s",
		"issues.notification.created.body":                   "Issue: **%s**\nAsset: **%s**\nStatus: **%s**",
		"issues.notification.created.body_no_asset":          "Issue: **%s**\nStatus: **%s**",
		"issues.notification.created.body_with_link":         "Issue: **%s**\nAsset: **%s**\nStatus: **%s**\n\nTrack it here: %s",
		"issues.notification.created.body_no_asset_with_link": "Issue: **%s**\nStatus: **%s**\n\nTrack it here: %s",
		"issues.state.reported":                              "Reported",
		"issues.state.in_progress":                           "In Progress",
	}
	if tpl, ok := templates[key]; ok {
		return fmt.Sprintf(tpl, args...)
	}
	return key
}

func TestNotifyIssueStateChange_OptOutSendsNothing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	h := &UIHandler{
		config: Config{CommsURL: srv.URL, CommsAPIKey: "test-key"},
		logger: slog.Default(),
	}
	issue := Issue{ID: 42, Title: "Pump down", NotifyState: false, NotifyEmail: "ops@example.com"}
	h.notifyIssueStateChange(context.Background(), notifyTestTranslator, issue, "in_progress", 7)
	if calls != 0 {
		t.Fatalf("opt-out must not call seam-comm, got %d calls", calls)
	}
}

func TestNotifyIssueStateChange_DisabledWithoutURL(t *testing.T) {
	h := &UIHandler{logger: slog.Default()}
	issue := Issue{ID: 42, NotifyState: true, NotifyEmail: "ops@example.com"}
	// No server: any HTTP call would fail. The empty URL must short-circuit.
	h.notifyIssueStateChange(context.Background(), notifyTestTranslator, issue, "resolved", 0)
}

func TestNotifyIssueStateChange_SendsEmailRequest(t *testing.T) {
	var gotPath, gotAuth, gotKey string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("Idempotency-Key")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Location", "/v1/notifications/ntf_1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"ntf_1","status":"queued","plan":[]}`))
	}))
	defer srv.Close()

	h := &UIHandler{
		config: Config{CommsURL: srv.URL, CommsAPIKey: "test-key"},
		logger: slog.Default(),
	}
	issue := Issue{ID: 42, Title: "Pump down", Priority: "high", Severity: "major", NotifyState: true, NotifyEmail: "ops@example.com"}
	h.notifyIssueStateChange(context.Background(), notifyTestTranslator, issue, "in_progress", 7)

	if gotPath != "/v1/notifications" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotKey, "issue-42-state_change-") {
		t.Errorf("Idempotency-Key = %q", gotKey)
	}
	if body["category"] != "issue" || body["severity"] != "error" {
		t.Errorf("category/severity = %v/%v", body["category"], body["severity"])
	}
	recipient, ok := body["recipient"].(map[string]any)
	if !ok {
		t.Fatalf("recipient shape: %#v", body["recipient"])
	}
	addresses, ok := recipient["addresses"].(map[string]any)
	if !ok || addresses["email"] != "ops@example.com" {
		t.Errorf("addresses = %#v", recipient["addresses"])
	}
	reference, ok := body["reference"].(map[string]any)
	if !ok || reference["type"] != "issue" || reference["id"] != "42" {
		t.Errorf("reference = %#v", body["reference"])
	}
	content, ok := body["content"].(map[string]any)
	if !ok || content["format"] != "markdown" {
		t.Errorf("content = %#v", body["content"])
	}
	if subject, _ := content["subject"].(string); subject != "Issue \"Pump down\": In Progress" {
		t.Errorf("subject = %q", subject)
	}
	bodyText, _ := content["body"].(string)
	if !strings.Contains(bodyText, "Issue: **Pump down**") || !strings.Contains(bodyText, "Status: **In Progress**") {
		t.Errorf("body = %q", bodyText)
	}
	if strings.Contains(bodyText, "#42") {
		t.Errorf("body should not include issue ID: %q", bodyText)
	}
	metadata, ok := body["metadata"].(map[string]any)
	if !ok || metadata["event"] != "state_change" {
		t.Errorf("metadata = %#v", body["metadata"])
	}
}

func TestNotifyIssueStateChange_IncludesRejectReason(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"ntf_r","status":"queued","plan":[]}`))
	}))
	defer srv.Close()

	h := &UIHandler{config: Config{CommsURL: srv.URL, CommsAPIKey: "test-key"}, logger: slog.Default()}
	issue := Issue{ID: 5, Title: "Broken pump", NotifyState: true, NotifyEmail: "ops@example.com"}
	h.notifyIssueStateChange(context.Background(), notifyTestTranslator, issue, "rejected", 7, int64(0), "Not reproducible")

	content, _ := body["content"].(map[string]any)
	bodyText, _ := content["body"].(string)
	if !strings.Contains(bodyText, "Not reproducible") || !strings.Contains(bodyText, "Reason") {
		t.Errorf("body missing reject reason: %q", bodyText)
	}
}

func TestNotifyIssueStateChange_IncludesTrackingURL(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Location", "/v1/notifications/ntf_2")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"ntf_2","status":"queued","plan":[]}`))
	}))
	defer srv.Close()

	h := &UIHandler{
		config: Config{CommsURL: srv.URL, CommsAPIKey: "test-key", QRBaseURL: "http://localhost:8080"},
		logger: slog.Default(),
	}
	issue := Issue{ID: 42, Title: "Pump down", AssetName: "Pump A", NotifyState: true, NotifyEmail: "ops@example.com", TrackToken: "TRACKTOKEN123"}
	h.notifyIssueStateChange(context.Background(), notifyTestTranslator, issue, "in_progress", 7)

	content, ok := body["content"].(map[string]any)
	if !ok {
		t.Fatalf("content shape: %#v", body["content"])
	}
	bodyText, _ := content["body"].(string)
	wantURL := "http://localhost:8080/track/TRACKTOKEN123"
	if !strings.Contains(bodyText, wantURL) || !strings.Contains(bodyText, "Asset: **Pump A**") {
		t.Errorf("body missing tracking url/asset: %q", bodyText)
	}
}

func TestNotifyIssueCreated_SendsEmailRequest(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Location", "/v1/notifications/ntf_3")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"ntf_3","status":"queued","plan":[]}`))
	}))
	defer srv.Close()

	h := &UIHandler{
		config: Config{CommsURL: srv.URL, CommsAPIKey: "test-key", QRBaseURL: "http://localhost:8080"},
		logger: slog.Default(),
	}
	issue := Issue{ID: 99, Title: "New pump", State: "reported", NotifyState: true, NotifyEmail: "ops@example.com", TrackToken: "TRACKTOKEN999", ReportedBy: 5}
	h.notifyIssueCreated(context.Background(), notifyTestTranslator, issue)

	content, ok := body["content"].(map[string]any)
	if !ok {
		t.Fatalf("content shape: %#v", body["content"])
	}
	if subject, _ := content["subject"].(string); subject != "Issue \"New pump\": Reported" {
		t.Errorf("subject = %q", subject)
	}
	bodyText, _ := content["body"].(string)
	if !strings.Contains(bodyText, "http://localhost:8080/track/TRACKTOKEN999") || !strings.Contains(bodyText, "Issue: **New pump**") {
		t.Errorf("body missing expected content: %q", bodyText)
	}
	if strings.Contains(bodyText, "#99") {
		t.Errorf("body should not include issue ID: %q", bodyText)
	}
	metadata, ok := body["metadata"].(map[string]any)
	if !ok || metadata["event"] != "created" {
		t.Errorf("metadata = %#v", body["metadata"])
	}
}

func TestIssueFromEntity_ReadsFrozenNotificationPreference(t *testing.T) {
	e := &xolu.Entity{Data: map[string]any{
		"notify_state": true,
		"notify_email": "ops@example.com",
	}}
	issue := issueFromEntity(e)
	if !issue.NotifyState || issue.NotifyEmail != "ops@example.com" {
		t.Fatalf("notification preference not read back: %+v", issue)
	}

	e.Data["notify_state"] = false
	issue = issueFromEntity(e)
	if issue.NotifyState {
		t.Fatal("a boolean false must stay opt-out")
	}
}

func TestIssueCommsSeverity(t *testing.T) {
	cases := []struct {
		issue Issue
		want  string
	}{
		{Issue{Priority: "critical"}, "alert"},
		{Issue{Severity: "critical"}, "alert"},
		{Issue{Priority: "high"}, "error"},
		{Issue{Severity: "major"}, "error"},
		{Issue{Priority: "medium"}, "warning"},
		{Issue{Priority: "low"}, "important_info"},
	}
	for _, c := range cases {
		if got := issueCommsSeverity(c.issue); got != c.want {
			t.Errorf("issueCommsSeverity(%+v) = %q, want %q", c.issue, got, c.want)
		}
	}
}
