package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go-product-search/internal/money"
)

// Notifier is the single outbound boundary of this package — LAW: a check
// ends in a notification and nothing else. retryAfter comes back from the
// notifier (429/Retry-After) and lands on next_check_after.
type Notifier interface {
	Notify(ctx context.Context, w Watch, obs Observation) (retryAfter time.Duration, err error)
}

// DozorNotifier ships alerts through the governed dozor alertmanager v4
// webhook — the same path fleet scripts use for Telegram delivery.
type DozorNotifier struct {
	URL    string
	Client *http.Client
	Now    func() time.Time // test seam
}

// NewDozorNotifier builds the fleet-default notifier.
func NewDozorNotifier(url string) *DozorNotifier {
	return &DozorNotifier{URL: url, Client: &http.Client{Timeout: 15 * time.Second}, Now: time.Now}
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

// Notify posts an alertmanager v4 payload. severity=warning is the level
// dozor ships to Telegram; the alertname/watch labels give alertmanager's
// grouping a stable identity.
func (n *DozorNotifier) Notify(ctx context.Context, w Watch, obs Observation) (time.Duration, error) {
	label := w.Label
	if label == "" {
		label = w.Query
	}
	if label == "" {
		label = w.URL
	}
	summary := fmt.Sprintf("price hit: %s — %s (target %s)",
		label, money.Format(*obs.PriceMinor, obs.Currency),
		money.Format(w.TargetPriceMinor, w.Currency))
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
				"service":   "go-product-search",
				"watch":     strconv.FormatInt(w.ID, 10),
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
		return retryAfter(resp), fmt.Errorf("dozor webhook: HTTP %d", resp.StatusCode)
	}
	return 0, nil
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
