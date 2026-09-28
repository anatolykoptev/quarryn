package watch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func ptrInt64(v int64) *int64 { return &v }
func ptrInt(v int) *int       { return &v }

// captureWebhook stands up a sink that records the decoded payload and
// content-type of the last request.
func captureWebhook(t *testing.T) (*WebhookNotifier, *webhookPayload, *string) {
	t.Helper()
	got := &webhookPayload{}
	ct := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*ct = r.Header.Get("content-type")
		_ = json.NewDecoder(r.Body).Decode(got)
	}))
	t.Cleanup(srv.Close)
	return NewWebhookNotifier(srv.URL), got, ct
}

// A consumer parses this body by field name — a wrong key or a missing
// trigger would silently produce nil fields downstream, not an error.
func TestWebhookNotifierPayload(t *testing.T) {
	n, got, ct := captureWebhook(t)

	ts := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	w := Watch{
		ID: 7, Kind: KindOffer, Label: "shoes", URL: "https://ex.test/itm/1",
		Currency: "USD", TargetPct: ptrInt(20), BaselineMinor: ptrInt64(10_000),
		NotifyOn: NotifyAny,
	}
	obs := Observation{TS: ts, PriceMinor: ptrInt64(7_990), Currency: "USD",
		Availability: "in_stock", OfferURL: w.URL, OfferID: "url|abc", Outcome: OutcomeOK}

	if _, err := n.Notify(context.Background(), w, obs, "restock"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if *ct != "application/json" {
		t.Fatalf("content-type = %q", *ct)
	}
	if got.Event != "watch_triggered" || got.Trigger != "restock" {
		t.Fatalf("event/trigger = %q/%q", got.Event, got.Trigger)
	}
	if got.WatchID != 7 || got.Kind != string(KindOffer) || got.Label != "shoes" || got.URL != w.URL {
		t.Fatalf("watch fields: %+v", got)
	}
}

// Observation + trigger-config fields ride the same body.
func TestWebhookNotifierObservationFields(t *testing.T) {
	n, got, _ := captureWebhook(t)

	ts := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	w := Watch{ID: 7, Kind: KindOffer, URL: "https://ex.test/itm/1",
		Currency: "USD", TargetPct: ptrInt(20), BaselineMinor: ptrInt64(10_000)}
	obs := Observation{TS: ts, PriceMinor: ptrInt64(7_990), Currency: "USD",
		Availability: "in_stock", OfferURL: w.URL, Outcome: OutcomeOK}

	if _, err := n.Notify(context.Background(), w, obs, "price"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.PriceMinor == nil || *got.PriceMinor != 7_990 || got.Availability != "in_stock" {
		t.Fatalf("observation fields: %+v", got)
	}
	if got.TargetPct == nil || *got.TargetPct != 20 || got.BaselineMinor == nil || *got.BaselineMinor != 10_000 {
		t.Fatalf("trigger config fields: %+v", got)
	}
	if got.ObservedAt != "2026-09-27T10:00:00Z" || got.Summary == "" {
		t.Fatalf("observed_at=%q summary=%q", got.ObservedAt, got.Summary)
	}
}

// Pending retries arrive with kind "" — the wire must still carry a
// trigger value a sink can group on.
func TestWebhookNotifierEmptyKind(t *testing.T) {
	n, got, _ := captureWebhook(t)

	w := Watch{ID: 1, Kind: KindOffer, URL: "https://ex.test/x", Currency: "USD"}
	obs := Observation{TS: time.Now(), Availability: "in_stock", Outcome: OutcomeOK}
	if _, err := n.Notify(context.Background(), w, obs, ""); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.Trigger != "unknown" {
		t.Fatalf("trigger = %q, want unknown", got.Trigger)
	}
}

// A failing sink must surface as an error (alert stays pending) and
// pass Retry-After through to next_check_after — same contract as the
// alertmanager notifier.
func TestWebhookNotifierHTTPErrorAndRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL)
	ra, err := n.Notify(context.Background(), Watch{ID: 1, Kind: KindOffer}, Observation{Outcome: OutcomeOK}, "price")
	if err == nil {
		t.Fatal("expected error on HTTP 502")
	}
	if ra != 2*time.Minute {
		t.Fatalf("retryAfter = %v, want 2m", ra)
	}

	n.URL = "" // fail-closed like the alertmanager notifier
	if _, err := n.Notify(context.Background(), Watch{}, Observation{}, "price"); err == nil {
		t.Fatal("expected error on empty URL")
	}
}

// tg-owned watches must reach the bot endpoint; fleet watches must keep
// the default sink — a misrouted alert is a silently lost notification.
func TestRoutingNotifier(t *testing.T) {
	var tgHit, defHit int
	tg := &countingNotifier{n: &tgHit}
	def := &countingNotifier{n: &defHit}
	r := RoutingNotifier{TG: tg, Default: def}
	ctx := context.Background()
	obs := Observation{Outcome: OutcomeOK}

	if _, err := r.Notify(ctx, Watch{Owner: "tg:42"}, obs, "price"); err != nil {
		t.Fatal(err)
	}
	if tgHit != 1 || defHit != 0 {
		t.Fatalf("tg watch: tg=%d default=%d", tgHit, defHit)
	}
	if _, err := r.Notify(ctx, Watch{Owner: ""}, obs, "price"); err != nil {
		t.Fatal(err)
	}
	if tgHit != 1 || defHit != 1 {
		t.Fatalf("fleet watch: tg=%d default=%d", tgHit, defHit)
	}
	// No bot notifier configured → tg watch falls back to the fleet sink.
	r.TG = nil
	if _, err := r.Notify(ctx, Watch{Owner: "tg:42"}, obs, "price"); err != nil {
		t.Fatal(err)
	}
	if defHit != 2 {
		t.Fatalf("no-bot fallback: default=%d", defHit)
	}
}

type countingNotifier struct{ n *int }

func (c *countingNotifier) Notify(context.Context, Watch, Observation, string) (time.Duration, error) {
	*c.n++
	return 0, nil
}
