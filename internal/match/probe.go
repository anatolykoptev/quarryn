package match

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/anatolykoptev/go-kit/jeff"
)

// errJeffUnconfigured is the Ping outcome on a degrade-mode matcher (no
// JEFF_URL): the probe reports it as reachability failure detail rather
// than a transport error.
var errJeffUnconfigured = errors.New("jeff unconfigured (JEFF_URL unset)")

// Ping is the jeff_reachable acceptance probe: ONE canned noul Ask through
// the configured jeff client under the per-call deadline. Wire types stay
// inside package match (ADR-11) — callers get a bare error.
func (m *Matcher) Ping(ctx context.Context) error {
	if m.jeff == nil {
		return errJeffUnconfigured
	}
	cctx, cancel := m.callDeadline(ctx)
	defer cancel()
	_, err := m.jeff.Ask(cctx, jeff.Request{
		State:     CandidateState{Name: "product probe"},
		Questions: map[string]jeff.Question{"p0": jeff.NoulQuestion("Is this a product listing?")},
	})
	return err
}

// NewWithAsker builds a Matcher on an injected Ask implementation — the
// injection probe's state-capturing boundary. cfg gets the same defaults
// New applies; a nil asker leaves a degrade-mode matcher.
func NewWithAsker(a Asker, cfg Config) *Matcher {
	m := newMatcher(cfg)
	m.jeff = a
	return m
}

// CaptureAsker is a boundary tap for the injection acceptance probe: it
// records the serialized CandidateState of every Ask and answers each
// question with a fixed noul. It lives in package match so jeff wire types
// never leave the stage package (ADR-11); production matchers never use
// it.
type CaptureAsker struct {
	// Noul is the probability every answer reports.
	Noul float64
	// States holds json.Marshal(req.State) per Ask — the exact bytes that
	// would have crossed the jeff boundary, in call order.
	States [][]byte
}

// Ask records the state and answers every question with c.Noul.
func (c *CaptureAsker) Ask(_ context.Context, req jeff.Request) (*jeff.Response, error) {
	raw, err := json.Marshal(req.State)
	if err != nil {
		return nil, err
	}
	c.States = append(c.States, raw)
	answers := make(map[string]jeff.Answer, len(req.Questions))
	for id := range req.Questions {
		answers[id] = jeff.Answer{Type: "noul", Noul: c.Noul}
	}
	return &jeff.Response{Answers: answers}, nil
}
