package sources

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anatolykoptev/go-kit/wowa"
)

// gateStep is one scripted upstream outcome: a page status, or a call
// error (transport-level / wowa envelope).
type gateStep struct {
	status int
	err    error
}

// queueFetcher replays a scripted sequence of responses and counts calls —
// the fetch gate's retry/backoff behaviour is asserted on the call count.
type queueFetcher struct {
	calls atomic.Int64
	queue []gateStep
}

func (q *queueFetcher) Fetch(context.Context, wowa.FetchRequest) (*wowa.FetchResponse, error) {
	i := int(q.calls.Add(1)) - 1
	if i >= len(q.queue) {
		i = len(q.queue) - 1
	}
	step := q.queue[i]
	if step.err != nil {
		return nil, step.err
	}
	return &wowa.FetchResponse{Status: step.status, Body: "x"}, nil
}

// testPacer is zero-paced with a 1ms backoff base so waits cost nothing.
func testPacer() *DomainPacer {
	return &DomainPacer{
		domains:  make(map[string]*domainState),
		minWait:  0,
		backoff:  time.Millisecond,
		maxRetry: maxDomainRetries,
	}
}

func gateFor(f *queueFetcher) *FetchGate {
	return NewFetchGate(f, nil, "detail", testPacer())
}

func gateReq(url string) wowa.FetchRequest {
	return wowa.FetchRequest{URL: url, TimeoutSecs: 5}
}

func TestGateFetchPassThrough(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{{status: 200}}}
	resp, err := gateFor(f).Fetch(t.Context(), gateReq("https://shop.example.com/p/1"))
	if err != nil || resp.Status != 200 {
		t.Fatalf("fetch = %v, %+v", err, resp)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", f.calls.Load())
	}
}

func TestGateBackoffSucceedsAfterThrottles(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{{status: 429}, {status: 429}, {status: 200}}}
	resp, err := gateFor(f).Fetch(t.Context(), gateReq("https://shop.example.com/p/1"))
	if err != nil {
		t.Fatalf("fetch err = %v", err)
	}
	if resp == nil || resp.Status != 200 {
		t.Fatalf("resp = %+v", resp)
	}
	if f.calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (two throttles then success)", f.calls.Load())
	}
}

func TestGateRetriesExhaustedSkipsDomain(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{{status: 429}}}
	g := gateFor(f)
	ctx := WithPageBudget(t.Context(), NewPageBudget(100))

	resp, err := g.Fetch(ctx, gateReq("https://walled.example.com/p/1"))
	// The final throttle response is still returned as data — the caller
	// sees upstream 429, not a gate error.
	if err != nil || resp == nil || resp.Status != 429 {
		t.Fatalf("final throttle = resp %+v err %v", resp, err)
	}
	// initial + maxDomainRetries retries
	if got, want := f.calls.Load(), int64(maxDomainRetries+1); got != want {
		t.Fatalf("calls = %d, want %d", got, want)
	}

	// The domain is skipped for the rest of THIS request — no wire call.
	resp, err = g.Fetch(ctx, gateReq("https://walled.example.com/p/2"))
	if !errors.Is(err, ErrDomainThrottled) {
		t.Fatalf("skipped fetch err = %v, want ErrDomainThrottled", err)
	}
	if resp != nil || f.calls.Load() != int64(maxDomainRetries+1) {
		t.Fatalf("skipped fetch hit the wire: resp %+v calls %d", resp, f.calls.Load())
	}

	// A different domain in the same request is unaffected.
	f.queue = []gateStep{{status: 200}}
	if resp, err = g.Fetch(ctx, gateReq("https://other.example.com/p/1")); err != nil || resp.Status != 200 {
		t.Fatalf("other domain = %v, %+v", err, resp)
	}

	// A fresh request gets a fresh skip set (the shared pacer still paces).
	resp, err = g.Fetch(t.Context(), gateReq("https://walled.example.com/p/3"))
	if err != nil || resp == nil || resp.Status != 200 {
		t.Fatalf("new request = %v, %+v", err, resp)
	}
}

func TestGateTransportThrottleAlsoRetries(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{
		{err: &wowa.StatusError{Endpoint: "fetch", StatusCode: 503}},
		{status: 200},
	}}
	resp, err := gateFor(f).Fetch(t.Context(), gateReq("https://shop.example.com/p/1"))
	if err != nil || resp.Status != 200 {
		t.Fatalf("fetch = %v, %+v", err, resp)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls.Load())
	}
}

func TestGatePlainErrorNotRetried(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{{err: errors.New("connection reset")}}}
	_, err := gateFor(f).Fetch(t.Context(), gateReq("https://shop.example.com/p/1"))
	if err == nil {
		t.Fatal("transport error must propagate")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("non-throttle error retried: calls = %d", f.calls.Load())
	}
}

func TestGatePageBudgetStopsCalls(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{{status: 200}}}
	g := gateFor(f)
	ctx := WithPageBudget(t.Context(), NewPageBudget(1))

	if _, err := g.Fetch(ctx, gateReq("https://a.example.com/1")); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := g.Fetch(ctx, gateReq("https://b.example.com/2")); !errors.Is(err, ErrPageBudgetExhausted) {
		t.Fatalf("second fetch err = %v, want ErrPageBudgetExhausted", err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("budget breached: calls = %d", f.calls.Load())
	}
}

func TestGateMinIntervalPacing(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{{status: 200}}}
	pacer := testPacer()
	pacer.minWait = 60 * time.Millisecond
	g := NewFetchGate(f, nil, "serp", pacer)

	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := g.Fetch(t.Context(), gateReq("https://shop.example.com/p/1")); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	// 3 calls to one domain at a 60ms min interval → at least 2 full
	// waits between call starts.
	if elapsed := time.Since(start); elapsed < 110*time.Millisecond {
		t.Fatalf("calls not paced: %v for 3 calls at 60ms interval", elapsed)
	}
	// Different domains are not paced against each other.
	start = time.Now()
	for _, host := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		if _, err := g.Fetch(t.Context(), gateReq("https://"+host+"/p")); err != nil {
			t.Fatalf("fetch %s: %v", host, err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("distinct domains wrongly paced: %v", elapsed)
	}
}

func TestGateRenderSharesBudget(t *testing.T) {
	f := &queueFetcher{queue: []gateStep{{status: 200}}}
	r := &queueRenderer{}
	g := NewFetchGate(f, r, "detail", testPacer())
	ctx := WithPageBudget(t.Context(), NewPageBudget(2))

	if _, err := g.Fetch(ctx, gateReq("https://a.example.com/1")); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Render(ctx, wowa.RenderRequest{URL: "https://a.example.com/1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Fetch(ctx, gateReq("https://a.example.com/2")); !errors.Is(err, ErrPageBudgetExhausted) {
		t.Fatalf("third page err = %v, want ErrPageBudgetExhausted", err)
	}
	if f.calls.Load()+r.calls.Load() != 2 {
		t.Fatalf("budget breached: fetch=%d render=%d", f.calls.Load(), r.calls.Load())
	}
}

type queueRenderer struct {
	calls atomic.Int64
}

func (q *queueRenderer) Render(context.Context, wowa.RenderRequest) (*wowa.RenderResponse, error) {
	q.calls.Add(1)
	return &wowa.RenderResponse{Status: 200, HTML: "<html/>"}, nil
}
