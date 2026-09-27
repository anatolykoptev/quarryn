package api

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"testing"

	"github.com/anatolykoptev/go-kit/wowa"
	"github.com/anatolykoptev/quarryn/internal/config"
	"github.com/anatolykoptev/quarryn/internal/match"
	"github.com/anatolykoptev/quarryn/internal/probe"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// captureSlog redirects the default logger — the sink jeff_gate events
// write to — into a buffer for the duration of the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestSearchRequestIDJoinsResponseAndLogs — the ADR-6 calibration pair:
// the response payload's request_id is a v4 uuid, and the SAME id lands on
// the jeff_gate calibration events so the offline join to feedback works.
func TestSearchRequestIDJoinsResponseAndLogs(t *testing.T) {
	clearSourceEnv(t)
	buf := captureSlog(t)
	wowaSrv := wowaStub(t)
	jeffSrv := jeffStub(t, map[string]float64{
		"Sony XM5 Headphones — hot deal": 0.9,
		"Budget Earbuds deal":            0.9,
	})
	d := testDeps(t, config.Config{WowaURL: wowaSrv.URL, JeffURL: jeffSrv.URL, JeffToken: "t"})

	res, err := handleProductSearch(t.Context(), d, productSearchInput{
		Query:    "headphones",
		Criteria: []string{"good sound"},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out searchOutput
	decodeResult(t, res, &out)

	if !uuidV4.MatchString(out.RequestID) {
		t.Fatalf("request_id not a v4 uuid: %q", out.RequestID)
	}
	// Every judged candidate emits one jeff_gate line; all carry this id.
	want := []byte("request_id=" + out.RequestID)
	if !bytes.Contains(buf.Bytes(), []byte("jeff_gate")) || !bytes.Contains(buf.Bytes(), want) {
		t.Fatalf("jeff_gate events lack the response request_id %q:\n%s", out.RequestID, buf.String())
	}
}

// TestMatchURLRequestID — product_match emits the same calibration id.
func TestMatchURLRequestID(t *testing.T) {
	clearSourceEnv(t)
	wowaSrv := wowaStub(t)
	jeffSrv := jeffStub(t, map[string]float64{"Sony XM5 Headphones": 0.9})
	d := testDeps(t, config.Config{WowaURL: wowaSrv.URL, JeffURL: jeffSrv.URL, JeffToken: "t"})

	res, err := handleProductMatch(t.Context(), d, productMatchInput{
		ProductURL: "http://203.0.113.60/item-a",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out matchOutput
	decodeResult(t, res, &out)
	if !uuidV4.MatchString(out.RequestID) {
		t.Fatalf("request_id not a v4 uuid: %q", out.RequestID)
	}
}

// probeFetch stubs the wowa_reachable probe's /fetch.
type probeFetch struct{}

func (probeFetch) Fetch(_ context.Context, _ wowa.FetchRequest) (*wowa.FetchResponse, error) {
	return &wowa.FetchResponse{Status: 200, Body: "<html>ok</html>"}, nil
}

// TestProductProbeTool — the tool returns the structured probe report.
func TestProductProbeTool(t *testing.T) {
	jeffSrv := jeffStub(t, nil)
	m, err := match.New(match.Config{URL: jeffSrv.URL, Token: "t"})
	if err != nil {
		t.Fatalf("match.New: %v", err)
	}
	d := deps{prober: probe.New(probeFetch{}, m, 0.55)}

	res, err := handleProductProbe(t.Context(), d)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var rep probe.Report
	decodeResult(t, res, &rep)
	if !rep.Pass || len(rep.Probes) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	byName := map[string]bool{}
	for _, r := range rep.Probes {
		byName[r.Probe] = r.Pass
	}
	for _, name := range []string{"jeff_reachable", "wowa_reachable", "injection_probe"} {
		if !byName[name] {
			t.Fatalf("probe %q missing/failed: %+v", name, rep.Probes)
		}
	}
}

// TestProductProbeNoRunner — an unwired prober is a tool error, not a panic.
func TestProductProbeNoRunner(t *testing.T) {
	res, err := handleProductProbe(t.Context(), deps{})
	if err != nil || !res.IsError {
		t.Fatalf("nil prober must be a tool error: %v %+v", err, res)
	}
}
