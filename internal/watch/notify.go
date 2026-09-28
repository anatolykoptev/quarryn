package watch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/quarryn/internal/money"
)

// Notifier is the single outbound boundary of this package — LAW: a check
// ends in a notification and nothing else. kind names the firing trigger
// ("price"|"restock", "" on pending retries); retryAfter comes back from
// the notifier (429/Retry-After) and lands on next_check_after.
type Notifier interface {
	Notify(ctx context.Context, w Watch, obs Observation, kind string) (retryAfter time.Duration, err error)
}

// AlertmanagerNotifier ships alerts through an Alertmanager v4 webhook endpoint.
type AlertmanagerNotifier struct {
	URL    string
	Client *http.Client
	Now    func() time.Time // test seam
}

// NewAlertmanagerNotifier builds the notifier.
func NewAlertmanagerNotifier(url string) *AlertmanagerNotifier {
	return &AlertmanagerNotifier{URL: url, Client: &http.Client{Timeout: 15 * time.Second}, Now: time.Now}
}

type amAlert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
}

type amPayload struct {
	Version string    `json:"version"`
	Status  string    `json:"status"`
	Alerts  []amAlert `json:"alerts"`
}

// alertSummary renders the human-facing line by trigger kind.
func alertSummary(w Watch, obs Observation, kind, label string) string {
	switch {
	case kind == "restock":
		s := "restock: " + label + " is " + obs.Availability
		if obs.PriceMinor != nil {
			s += " at " + money.Format(*obs.PriceMinor, obs.Currency)
		}
		return s
	case obs.PriceMinor != nil:
		s := fmt.Sprintf("price hit: %s — %s", label,
			money.Format(*obs.PriceMinor, obs.Currency))
		if tgt := w.effectiveTarget(); tgt > 0 {
			s += fmt.Sprintf(" (target %s)", money.Format(tgt, w.Currency))
		}
		return s
	default:
		return "watch fired: " + label
	}
}

// Notify posts an alertmanager v4 payload. severity=warning is a portable alert level; the alertname/watch labels give alertmanager's
// grouping a stable identity. The trigger label splits price hits from
// restocks; the summary text follows the firing kind.
func (n *AlertmanagerNotifier) Notify(ctx context.Context, w Watch, obs Observation, kind string) (time.Duration, error) {
	label := watchLabel(w)
	summary := alertSummary(w, obs, kind, label)
	desc := obs.OfferURL
	if w.NotifiedPriceMinor != nil {
		desc = fmt.Sprintf("%s\nprevious notify at %s", desc,
			money.Format(*w.NotifiedPriceMinor, w.Currency))
	}
	body, err := json.Marshal(amPayload{
		Version: "4",
		Status:  "firing",
		Alerts: []amAlert{{
			Status: "firing",
			Labels: map[string]string{
				"alertname": "PriceWatchHit",
				"severity":  "warning",
				"service":   "quarryn",
				"watch":     strconv.FormatInt(w.ID, 10),
				"trigger":   triggerKind(kind),
			},
			Annotations: map[string]string{"summary": summary, "description": desc},
			StartsAt:    n.Now().UTC().Format(time.RFC3339),
		}},
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := n.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return retryAfter(resp), fmt.Errorf("alertmanager webhook: HTTP %d", resp.StatusCode)
	}
	return 0, nil
}

// watchLabel is the human-facing name — label, else query, else URL.
func watchLabel(w Watch) string {
	if w.Label != "" {
		return w.Label
	}
	if w.Query != "" {
		return w.Query
	}
	return w.URL
}

// triggerKind normalizes the empty kind a pending retry can carry.
func triggerKind(kind string) string {
	if kind == "" {
		return "unknown"
	}
	return kind
}

// WebhookNotifier posts a plain JSON body to the configured URL — the
// standalone-user sink (ntfy/Gotify/custom) that doesn't assume a fleet
// Alertmanager (issue #99). Same at-least-once semantics: a transport or
// HTTP>=400 failure leaves the alert pending and the checker retries.
// hmacSecret signs the body when set — X-Webhook-Timestamp +
// X-Webhook-Signature-V2 over "<ts>.<body>", the scheme the Hermes
// gateway webhook platform validates (bot delivery lane).
type WebhookNotifier struct {
	URL    string
	Secret string
	Client *http.Client
	Now    func() time.Time // test seam
}

// NewWebhookNotifier builds the notifier; hmacSecret "" = unsigned.
func NewWebhookNotifier(url, hmacSecret string) *WebhookNotifier {
	return &WebhookNotifier{URL: url, Secret: hmacSecret, Client: &http.Client{Timeout: 15 * time.Second}, Now: time.Now}
}

type webhookPayload struct {
	Event            string `json:"event"` // watch_triggered
	Trigger          string `json:"trigger"`
	WatchID          int64  `json:"watch_id"`
	Kind             string `json:"kind"`
	Owner            string `json:"owner,omitempty"`   // tg:<chat_id> — the sink resolves the recipient
	ChatID           string `json:"chat_id,omitempty"` // owner sans tg: — hermes webhook deliver_extra template
	Label            string `json:"label,omitempty"`
	URL              string `json:"url,omitempty"`
	Query            string `json:"query,omitempty"`
	OfferID          string `json:"offer_id,omitempty"`
	PriceMinor       *int64 `json:"price_minor,omitempty"`
	Currency         string `json:"currency,omitempty"`
	Availability     string `json:"availability,omitempty"`
	TargetPriceMinor *int64 `json:"target_price_minor,omitempty"`
	TargetPct        *int   `json:"target_pct,omitempty"`
	BaselineMinor    *int64 `json:"baseline_price_minor,omitempty"`
	ObservedAt       string `json:"observed_at"`
	Summary          string `json:"summary"`
}

// Notify posts the flat JSON payload. trigger/observed_at/summary mirror
// the alertmanager labels+annotations so sinks can route or render
// without knowing the v4 envelope.
func (n *WebhookNotifier) Notify(ctx context.Context, w Watch, obs Observation, kind string) (time.Duration, error) {
	label := watchLabel(w)
	body, err := json.Marshal(webhookPayload{
		Event:            "watch_triggered",
		Trigger:          triggerKind(kind),
		WatchID:          w.ID,
		Kind:             string(w.Kind),
		Owner:            w.Owner,
		ChatID:           strings.TrimPrefix(w.Owner, OwnerPrefixTelegram),
		Label:            label,
		URL:              w.URL,
		Query:            w.Query,
		OfferID:          obs.OfferID,
		PriceMinor:       obs.PriceMinor,
		Currency:         obs.Currency,
		Availability:     obs.Availability,
		TargetPriceMinor: w.TargetPriceMinor,
		TargetPct:        w.TargetPct,
		BaselineMinor:    w.BaselineMinor,
		ObservedAt:       obs.TS.UTC().Format(time.RFC3339),
		Summary:          alertSummary(w, obs, kind, label),
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("content-type", "application/json")
	if n.Secret != "" {
		// Hermes-style HMAC-V2: hex(HMAC-SHA256(secret, "<unix>.<body>")).
		ts := strconv.FormatInt(n.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(n.Secret))
		mac.Write([]byte(ts))
		mac.Write([]byte("."))
		mac.Write(body)
		req.Header.Set("X-Webhook-Timestamp", ts)
		req.Header.Set("X-Webhook-Signature-V2", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := n.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return retryAfter(resp), fmt.Errorf("webhook notify: HTTP %d", resp.StatusCode)
	}
	return 0, nil
}

// OwnerPrefixTelegram marks bot-owned watches ("tg:<chat_id>") — the
// routing notifier sends those to the bot's delivery endpoint instead of
// the fleet alertmanager.
const OwnerPrefixTelegram = "tg:"

// RoutingNotifier fans a fired trigger out by owner: tg-owned watches go
// to the bot webhook (each alert lands in the right chat), everything
// else to the fleet sink. A nil/absent bot notifier falls back to the
// fleet sink — misconfiguration degrades to admin-visible alerts, never
// a silently dropped user notification.
type RoutingNotifier struct {
	TG      Notifier // bot delivery endpoint (BOT_NOTIFY_URL), may be nil
	Default Notifier // fleet sink (alertmanager)
}

// Notify routes by owner — tg-owned watches to the bot, rest to Default.
func (r RoutingNotifier) Notify(ctx context.Context, w Watch, obs Observation, kind string) (time.Duration, error) {
	if r.TG != nil && strings.HasPrefix(w.Owner, OwnerPrefixTelegram) {
		return r.TG.Notify(ctx, w, obs, kind)
	}
	return r.Default.Notify(ctx, w, obs, kind)
}

// retryAfter parses the Retry-After header — seconds or an HTTP date —
// into a duration the checker lands on next_check_after.
func retryAfter(resp *http.Response) time.Duration {
	h := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if h == "" {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return 5 * time.Minute // bounded default backoff on transient failures
		}
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
