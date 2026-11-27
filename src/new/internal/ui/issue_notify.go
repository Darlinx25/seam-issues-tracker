package ui

// Issue state-change notifications through seam-comm.
//
// The reporter may opt in when creating an issue. From then on every
// successful lifecycle transition sends one short email to the address frozen
// on the issue. Notification failures are logged but never block or roll back
// the FSM transition: seam-comm is a best-effort side effect in this slice.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	seamcomm "github.com/ha1tch/seam-ui/internal/seamcomm"
)

const (
	issueCommsCategory = "issue"
	issueCommsChannel  = "email"
	issueCommsTimeout  = 5 * time.Second
)

// issueCommsSeverity maps the issue's own priority/severity onto the ordinal
// severity understood by seam-comm. The issue category defaults to
// important_info, but critical work must be able to reach the higher tiers.
func issueCommsSeverity(issue Issue) string {
	if issue.Priority == "critical" || issue.Severity == "critical" {
		return "alert"
	}
	if issue.Priority == "high" || issue.Severity == "major" {
		return "error"
	}
	if issue.Priority == "medium" {
		return "warning"
	}
	return "important_info"
}

func (h *UIHandler) issueNotificationTrackingURL(issue Issue) string {
	if strings.TrimSpace(issue.TrackToken) == "" {
		return ""
	}
	return h.issueTrackingLink(issue.TrackToken)
}

func (h *UIHandler) issueNotificationAssetName(issue Issue) string {
	if name := strings.TrimSpace(issue.AssetName); name != "" {
		return name
	}
	return ""
}

func (h *UIHandler) issueNotificationContent(t func(string, ...any) string, issue Issue, event, state, reason string) (subject, body string) {
	stateLabel := t("issues.state." + state)
	trackingURL := h.issueNotificationTrackingURL(issue)
	assetName := h.issueNotificationAssetName(issue)
	reason = strings.TrimSpace(reason)

	subjectKey := "issues.notification.subject"
	bodyKey := "issues.notification.body"
	if event == "created" {
		subjectKey = "issues.notification.created.subject"
		bodyKey = "issues.notification.created.body"
	}
	issueTitle := strings.TrimSpace(issue.Title)
	if issueTitle == "" {
		issueTitle = fmt.Sprintf("Issue #%d", issue.ID)
	}
	if strings.TrimSpace(stateLabel) == "" {
		stateLabel = state
	}
	subject = t(subjectKey, issueTitle, stateLabel)
	if strings.TrimSpace(subject) == "" || subject == subjectKey {
		subject = fmt.Sprintf("Issue \"%s\": %s", issueTitle, stateLabel)
	}

	if assetName != "" {
		if trackingURL != "" {
			body = t(bodyKey+"_with_link", issueTitle, assetName, stateLabel, trackingURL)
		} else {
			body = t(bodyKey, issueTitle, assetName, stateLabel)
		}
	} else if trackingURL != "" {
		body = t(bodyKey+"_no_asset_with_link", issueTitle, stateLabel, trackingURL)
	} else {
		body = t(bodyKey+"_no_asset", issueTitle, stateLabel)
	}
	if strings.TrimSpace(body) == "" || body == bodyKey || body == bodyKey+"_with_link" || body == bodyKey+"_no_asset" || body == bodyKey+"_no_asset_with_link" {
		body = fmt.Sprintf("Issue: %s\nStatus: %s", issueTitle, stateLabel)
		if assetName != "" {
			body = fmt.Sprintf("Issue: %s\nAsset: %s\nStatus: %s", issueTitle, assetName, stateLabel)
		}
		if trackingURL != "" {
			body += "\n\nTrack it here: " + trackingURL
		}
	}
	if reason != "" {
		reasonLabel := t("issues.notification.reason")
		if strings.TrimSpace(reasonLabel) == "" || reasonLabel == "issues.notification.reason" {
			reasonLabel = "Reason"
		}
		body += fmt.Sprintf("\n%s: %s", reasonLabel, reason)
	}
	return subject, body
}

func (h *UIHandler) sendIssueNotification(ctx context.Context, issue Issue, event, state string, actorID int64, subject, body string, canonicalID ...int64) {
	request := seamcomm.Request{
		Recipient: seamcomm.Recipient{
			Addresses: map[string]string{issueCommsChannel: issue.NotifyEmail},
		},
		Category: issueCommsCategory,
		Severity: issueCommsSeverity(issue),
		Channels: &seamcomm.ChannelSelection{
			Mode:   "explicit",
			SendTo: []string{issueCommsChannel},
		},
		Content: seamcomm.Content{
			Format:  "markdown",
			Subject: subject,
			Body:    body,
		},
		Reference: &seamcomm.Reference{Type: "issue", ID: strconv.FormatInt(issue.ID, 10)},
		Metadata:  map[string]string{"state": state, "event": event},
	}
	if actorID > 0 {
		request.Metadata["actor_id"] = strconv.FormatInt(actorID, 10)
	}
	if len(canonicalID) > 0 && canonicalID[0] > 0 {
		request.Metadata["canonical_id"] = strconv.FormatInt(canonicalID[0], 10)
	}
	trackingURL := h.issueNotificationTrackingURL(issue)
	if trackingURL != "" {
		request.Metadata["tracking_url"] = trackingURL
	}

	// Same issue + same event/state + same second dedupes a retry; a later
	// transition gets its own key.
	key := fmt.Sprintf("issue-%d-%s-%s", issue.ID, event, time.Now().UTC().Format(time.RFC3339))
	client := seamcomm.New(h.config.CommsURL, h.config.CommsAPIKey, seamcomm.WithRetry(0, 0))

	sendCtx, cancel := context.WithTimeout(ctx, issueCommsTimeout)
	defer cancel()

	accepted, err := client.Send(sendCtx, key, request)
	if err != nil {
		h.logger.Warn("issue notification failed", "id", issue.ID, "event", event, "state", state, "error", err)
		return
	}
	h.logger.Info("issue notification queued", "id", issue.ID, "event", event, "state", state, "notification", accepted.ID)
}

// notifyIssueStateChange sends the state-change email. It is a no-op when the
// reporter did not opt in, when there is no address, or when seam-comm is not
// configured -- the latter keeps a merged build usable without the companion
// service running.
//
// The caller passes t() explicitly so every user-visible string still goes
// through the translator in the four supported locales.
func (h *UIHandler) notifyIssueStateChange(ctx context.Context, t func(string, ...any) string, issue Issue, newState string, actorID int64, extra ...any) {
	if !issue.NotifyState || strings.TrimSpace(issue.NotifyEmail) == "" {
		return
	}
	if h.config.CommsURL == "" {
		return
	}
	var canonicalID int64
	reason := ""
	if len(extra) > 0 {
		canonicalID, _ = extra[0].(int64)
	}
	if len(extra) > 1 {
		reason, _ = extra[1].(string)
	}
	if reason == "" {
		reason = issue.RejectReason
	}
	subject, body := h.issueNotificationContent(t, issue, "state_change", newState, reason)
	h.sendIssueNotification(ctx, issue, "state_change", newState, actorID, subject, body, canonicalID)
}

// notifyIssueCreated sends the issue creation email. It is best-effort like
// state-change notifications: failures are logged and never block creation.
func (h *UIHandler) notifyIssueCreated(ctx context.Context, t func(string, ...any) string, issue Issue) {
	if !issue.NotifyState || strings.TrimSpace(issue.NotifyEmail) == "" {
		return
	}
	if h.config.CommsURL == "" {
		return
	}
	state := issue.State
	if state == "" {
		state = "reported"
	}
	actorID := issue.ReportedBy
	if actorID == 0 {
		actorID = issue.Assignee
	}
	subject, body := h.issueNotificationContent(t, issue, "created", state, "")
	h.sendIssueNotification(ctx, issue, "created", state, actorID, subject, body)
}
