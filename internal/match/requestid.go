package match

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
)

// requestIDKey is the context key carrying the caller-supplied calibration
// id (ADR-6): a uuid minted per search call that joins the product_search
// response, every jeff_gate log event and the product_feedback outcome
// record.
type requestIDKey struct{}

// WithRequestID attaches the search-level calibration id to ctx. The match
// stage reads it back for the jeff_gate log events so all Asks of one
// search share the id the caller received in the tool response.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestID returns the id attached by WithRequestID, or a Match-local
// "m<N>" fallback for callers that never set one (direct Match calls in
// tests). The fallback keeps every jeff_gate line carrying a correlation
// id even without a search-level uuid.
func (m *Matcher) requestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok && id != "" {
		return id
	}
	return "m" + strconv.FormatUint(m.reqSeq.Add(1), 10)
}

// NewRequestID mints a random RFC 4122 version-4 uuid — the ADR-6
// calibration id each judged search emits. No dependency: 16 bytes of
// crypto/rand with the version/variant bits set.
func NewRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])  // crypto/rand failure leaves zeros — same convention as go-mcpserver's generateID
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant 10
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}
