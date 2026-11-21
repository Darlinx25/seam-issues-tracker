// Copyright (c) 2026 haitch <h@ual.li>
// Proprietary and confidential. All rights reserved.

package client

import "time"

// Recipient names who gets the notification: a directory contact or raw
// addresses keyed by channel (email, sms, whatsapp).
type Recipient struct {
	ContactID string            `json:"contact_id,omitempty"`
	Addresses map[string]string `json:"addresses,omitempty"`
}

// ChannelSelection overrides the default channel policy.
type ChannelSelection struct {
	Mode   string   `json:"mode,omitempty"`
	SendTo []string `json:"send_to,omitempty"`
}

// TemplateRef selects an approved template.
type TemplateRef struct {
	ID       string            `json:"id"`
	Language string            `json:"language"`
	Params   map[string]string `json:"params,omitempty"`
}

// Content is the message. Format is required for inline content and is one of
// text, markdown or html; leaving it out is a validation error.
type Content struct {
	Format   string       `json:"format,omitempty"`
	Subject  string       `json:"subject,omitempty"`
	Body     string       `json:"body,omitempty"`
	Template *TemplateRef `json:"template,omitempty"`
}

// AttachmentRef points at an uploaded attachment.
type AttachmentRef struct {
	AttachmentID string `json:"attachment_id"`
}

// Dedupe suppresses repeats of the same key inside a window.
type Dedupe struct {
	Key           string `json:"key"`
	WindowSeconds int    `json:"window_seconds"`
}

// Reference ties a notification to something in the caller's world, for
// example an asset. It is searchable when listing.
type Reference struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Request is a notification request.
type Request struct {
	Recipient        Recipient          `json:"recipient"`
	Severity         string             `json:"severity,omitempty"`
	Category         string             `json:"category"`
	Channels         *ChannelSelection  `json:"channels,omitempty"`
	Content          Content            `json:"content"`
	Overrides        map[string]Content `json:"overrides,omitempty"`
	Attachments      []AttachmentRef    `json:"attachments,omitempty"`
	AttachmentPolicy string             `json:"attachment_policy,omitempty"`
	Dedupe           *Dedupe            `json:"dedupe,omitempty"`
	ScheduleAt       *time.Time         `json:"schedule_at,omitempty"`
	ExpiresAt        *time.Time         `json:"expires_at,omitempty"`
	Reference        *Reference         `json:"reference,omitempty"`
	Metadata         map[string]string  `json:"metadata,omitempty"`
}

// PlanItem is one channel the notification will use.
type PlanItem struct {
	Channel              string `json:"channel"`
	Role                 string `json:"role"`
	Provider             string `json:"provider,omitempty"`
	EscalateAfterSeconds int    `json:"escalate_after_seconds,omitempty"`
	Mode                 string `json:"mode,omitempty"`
	Format               string `json:"format,omitempty"`
	TemplateID           string `json:"template_id,omitempty"`
	Encoding             string `json:"encoding,omitempty"`
	Segments             int    `json:"segments,omitempty"`
	EstimatedCostUnits   int    `json:"estimated_cost_units,omitempty"`
	AttachmentsAsLinks   int    `json:"attachments_as_links,omitempty"`
}

// Skip is a channel left out of the plan, and why.
type Skip struct {
	Channel string `json:"channel"`
	Reason  string `json:"reason"`
}

// Warning is a non-fatal observation about the request.
type Warning struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Channel string `json:"channel,omitempty"`
}

// FieldError is one problem with one part of a request.
type FieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Channel string `json:"channel,omitempty"`
	Limit   *int64 `json:"limit,omitempty"`
	Actual  *int64 `json:"actual,omitempty"`
}

// Accepted is the answer to Send.
type Accepted struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	Plan        []PlanItem `json:"plan"`
	Skipped     []Skip     `json:"skipped,omitempty"`
	Warnings    []Warning  `json:"warnings"`
	Reference   *Reference `json:"reference,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	DuplicateOf string     `json:"duplicate_of,omitempty"`

	// Replayed is true when the Idempotency-Key had already been used with this
	// request and the original answer was returned.
	Replayed bool `json:"-"`
	// Location is the URL path of the notification.
	Location string `json:"-"`
}

// ValidationResult is the answer to Validate.
type ValidationResult struct {
	Valid    bool         `json:"valid"`
	Severity string       `json:"severity,omitempty"`
	Category string       `json:"category,omitempty"`
	Plan     []PlanItem   `json:"plan"`
	Skipped  []Skip       `json:"skipped,omitempty"`
	Warnings []Warning    `json:"warnings"`
	Errors   []FieldError `json:"errors"`
}

// Failure explains why a delivery failed.
type Failure struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
	Detail    string `json:"detail,omitempty"`
}

// Delivery is the state of one channel.
type Delivery struct {
	ID        string    `json:"id"`
	Channel   string    `json:"channel"`
	Provider  string    `json:"provider,omitempty"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
	Failure   *Failure  `json:"failure,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

// Notification is the state of a notification and its deliveries.
type Notification struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	Category   string     `json:"category"`
	Severity   string     `json:"severity"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Reference  *Reference `json:"reference,omitempty"`
	Deliveries []Delivery `json:"deliveries"`
}

// Terminal reports whether the notification will not change any more.
func (n *Notification) Terminal() bool {
	switch n.Status {
	case "completed", "failed", "suppressed", "cancelled", "expired":
		return true
	}
	return false
}

// ListOptions filters List. Zero values mean no filter.
type ListOptions struct {
	Status         []string
	Category       string
	Severity       string
	Channel        string
	DeliveryStatus string
	ReferenceType  string
	ReferenceID    string
	CreatedAfter   time.Time
	CreatedBefore  time.Time
	Cursor         string
	Limit          int
}

// Page is one page of notifications, newest first.
type Page struct {
	Items      []Notification `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// AttachmentSupport describes attachment limits for a channel.
type AttachmentSupport struct {
	Supported     bool `json:"supported"`
	MaxFiles      *int `json:"max_files,omitempty"`
	MaxTotalBytes *int `json:"max_total_bytes,omitempty"`
}

// ChannelInfo describes one channel's capabilities and limits.
type ChannelInfo struct {
	ID                             string            `json:"id"`
	Provider                       string            `json:"provider"`
	Formats                        []string          `json:"formats"`
	MaxSubjectChars                *int              `json:"max_subject_chars,omitempty"`
	MaxBodyChars                   *int              `json:"max_body_chars,omitempty"`
	MaxHTMLBytes                   *int              `json:"max_html_bytes,omitempty"`
	MaxTextBytes                   *int              `json:"max_text_bytes,omitempty"`
	MaxSegments                    *int              `json:"max_segments,omitempty"`
	Attachments                    AttachmentSupport `json:"attachments"`
	RequiresTemplateOutsideSession bool              `json:"requires_template_outside_session"`
}

// Slice names what the deployment does not provide yet.
type Slice struct {
	ID          string   `json:"id"`
	Unavailable []string `json:"unavailable"`
}

// Channels is the answer to Channels.
type Channels struct {
	Channels []ChannelInfo `json:"channels"`
	// Slice is nil when nothing is missing.
	Slice *Slice `json:"slice,omitempty"`
}

// Category is an entry of the category registry.
type Category struct {
	ID              string   `json:"id"`
	Class           string   `json:"class"`
	AllowedChannels []string `json:"allowed_channels"`
	DefaultSeverity string   `json:"default_severity"`
}
