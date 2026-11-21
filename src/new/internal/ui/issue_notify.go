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

// notifyIssueStateChange sends the state-change email. It is a no-op when the
// reporter did not opt in, when there is no address, or when seam-comm is not
// configured -- the latter keeps a merged build usable without the companion
// service running.
//
// The caller passes t() explicitly so every user-visible string still goes
// through the translator in the four supported locales.
func (h *UIHandler) notifyIssueStateChange(ctx context.Context, t func(string, ...any) string, issue Issue, newState string, actorID int64) {
	if !issue.NotifyState || strings.TrimSpace(issue.NotifyEmail) == "" {
		return
	}
	if h.config.CommsURL == "" {
		return
	}

	stateLabel := t("issues.state." + newState)
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
			Subject: t("issues.notification.subject", issue.ID, stateLabel),
			Body:    t("issues.notification.body", issue.ID, issue.Title, stateLabel),
		},
		Reference: &seamcomm.Reference{Type: "issue", ID: strconv.FormatInt(issue.ID, 10)},
		Metadata:  map[string]string{"state": newState},
	}
	if actorID > 0 {
		request.Metadata["actor_id"] = strconv.FormatInt(actorID, 10)
	}

	// Same issue + same state + same second dedupes a retry of one transition;
	// a later transition to a different state gets its own key.
	key := fmt.Sprintf("issue-%d-%s-%s", issue.ID, newState, time.Now().UTC().Format(time.RFC3339))
	client := seamcomm.New(h.config.CommsURL, h.config.CommsAPIKey, seamcomm.WithRetry(0, 0))

	sendCtx, cancel := context.WithTimeout(ctx, issueCommsTimeout)
	defer cancel()

	accepted, err := client.Send(sendCtx, key, request)
	if err != nil {
		h.logger.Warn("issue state notification failed", "id", issue.ID, "state", newState, "error", err)
		return
	}
	h.logger.Info("issue state notification queued", "id", issue.ID, "state", newState, "notification", accepted.ID)
}
