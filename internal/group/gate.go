package group

import (
	"sort"
	"strings"
)

// gate.go is the precision half of embedding grouping (issue #98). Cosine
// similarity supplies recall — "WH-1000XM5" and "Sony WH1000XM5 wireless
// headphones" land close in vector space — but it cannot tell same-product
// from same-family: "WH-1000XM5" vs "WH-1000XM4" and "iPhone 16" vs
// "iPhone 16 Pro" are near-neighbours too. The discriminator gate refuses
// a merge when the model-identifying tokens of two names disagree.

// tierWords are model-tier qualifiers: presence asymmetry ("Pro" on one
// side only) marks different SKUs of a product line, so the sets must be
// exactly equal for a merge.
var tierWords = map[string]struct{}{
	"pro": {}, "max": {}, "plus": {}, "ultra": {}, "mini": {},
	"se": {}, "fe": {}, "lite": {}, "xl": {}, "ti": {}, "air": {},
}

// alnumTokens splits text into lowercase alphanumeric tokens — the shared
// tokenisation for every gate in the package.
func alnumTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// digitTokens is the set of tokens carrying at least one digit — model
// numbers and capacity codes ("1000xm5", "512gb", "16"). Separators are
// already consumed by tokenisation, so "WH-1000XM5" yields "1000xm5".
func digitTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range alnumTokens(s) {
		for _, r := range t {
			if r >= '0' && r <= '9' {
				out[t] = struct{}{}
				break
			}
		}
	}
	return out
}

// tierSet extracts the model-tier words present in text.
func tierSet(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range alnumTokens(s) {
		if _, ok := tierWords[t]; ok {
			out[t] = struct{}{}
		}
	}
	return out
}

// joinSorted renders a token set as one sorted concatenation — the
// canonical form a fragmented model code collapses to: {1000, xm5} and
// {1000xm5} both join to "1000xm5".
func joinSorted(set map[string]struct{}) string {
	ts := make([]string, 0, len(set))
	for t := range set {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return strings.Join(ts, "")
}

// subsetOf reports whether a ⊆ b.
func subsetOf(a, b map[string]struct{}) bool {
	for t := range a {
		if _, ok := b[t]; !ok {
			return false
		}
	}
	return true
}

// joinedCompatible compares the sorted-joined token bags: equal after
// collapsing fragmentation, or the shorter join is a ≥4-char suffix of
// the longer — the glued-brand-prefix case ("WH1000XM5" ⊃ "1000XM5").
func joinedCompatible(a, b map[string]struct{}) bool {
	ja, jb := joinSorted(a), joinSorted(b)
	if ja == jb {
		return true
	}
	shorter, longer := ja, jb
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	return len(shorter) >= 4 && strings.HasSuffix(longer, shorter)
}

// keyConflicts reports whether an incoming exact key contradicts keys a
// group already claims: same keyspace, different value — "gtin:X" never
// joins a group claiming "gtin:Y" (a product has one GTIN), while a
// different keyspace ("mpn:M") is no conflict — one product legitimately
// carries several identifier types.
func keyConflicts(key string, keys []string) bool {
	ns, _, _ := strings.Cut(key, ":")
	for _, k := range keys {
		kns, _, _ := strings.Cut(k, ":")
		if kns == ns && k != key {
			return true
		}
	}
	return false
}

// discsCompatible is the discriminator gate: digit-token sets must be
// subset-compatible, collapse to the same joined string (a store that
// hyphenates "WH 1000 XM5" must not fragment against "WH-1000XM5"), or
// suffix-contain the shorter join when it carries ≥4 chars (a glued brand
// prefix — "WH1000XM5" ends with "1000XM5" — is identity-preserving; a
// bare "xm5" or "s2" suffix is not), and tier-word sets must be equal —
// a "Pro" the other side lacks is a different product, not missing data.
func discsCompatible(aText, bText string) (ok bool, weak bool) {
	da, db := digitTokens(aText), digitTokens(bText)
	if len(da) > 0 && len(db) > 0 {
		if !subsetOf(da, db) && !subsetOf(db, da) && !joinedCompatible(da, db) {
			return false, false
		}
	}
	ta, tb := tierSet(aText), tierSet(bText)
	if !subsetOf(ta, tb) || !subsetOf(tb, ta) {
		return false, false
	}
	// weak identity: a model code only on one side — or on neither — is
	// thin evidence ("Sony wireless headphones" could be XM4 or XM5), so
	// the assigner raises the cosine bar for these merges.
	return true, len(da) == 0 || len(db) == 0
}
