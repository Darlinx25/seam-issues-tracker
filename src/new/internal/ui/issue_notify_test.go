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
	// The real translator resolves the key first and only then applies the
	// arguments; mirror that here instead of formatting the key itself.
	templates := map[string]string{
		"issues.notification.subject": "Issue #%d changed to %s",
		"issues.state.in_progress":    "In Progress",
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
	if !strings.HasPrefix(gotKey, "issue-42-in_progress-") {
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
	if subject, _ := content["subject"].(string); !strings.Contains(subject, "42") || !strings.Contains(subject, "In Progress") {
		t.Errorf("subject = %q", subject)
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
