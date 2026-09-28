package match

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Deterministic spec matching (issue #111): subjective criteria like
// "64GB", "48–64GB" or "M5 Pro" went to jeff verbatim, and the judge
// passed contradictions on vibes — a 24GB listing scored 0.69 against
// "48 or 64GB". Spec tokens are machine-checkable facts: parse them out
// of free-text criteria into constraints the prefilter applies BEFORE
// the ask. A criterion built ONLY of spec tokens earns no jeff question
// at all — the deterministic check is cheaper and cannot be charmed.

// SizeReq is a GB-normalized size requirement. A bare "64GB" is the
// interval [64,64]; a range "48–64GB" is [48,64]. An alternatives list
// ("32GB or 64GB") instead fills Points — the set is discrete because a
// config like 48GB sits inside the span yet satisfies neither option.
// GB↔TB normalize to gigabytes at ×1024 — listings and criteria mix
// units ("1TB SSD" vs "1024GB").
type SizeReq struct {
	MinGB  int64   `json:"min_gb"`
	MaxGB  int64   `json:"max_gb"`
	Points []int64 `json:"points,omitempty"` // discrete alternatives; empty = interval
}

// ChipReq pins an Apple-silicon generation floor: criterion "M5 Pro"
// needs gen 5 at tier ≥ pro. Tier order base<pro<max<ultra; a stronger
// tier of the same generation satisfies a lower ask ("M5" accepts "M5
// Max"), a weaker or older chip never does.
type ChipReq struct {
	Gen     int `json:"gen"`
	MinTier int `json:"min_tier"`
}

// String renders a size requirement compactly for exclusion detail.
func (r SizeReq) String() string {
	if len(r.Points) > 0 {
		parts := make([]string, len(r.Points))
		for i, v := range r.Points {
			parts[i] = strconv.FormatInt(v, 10)
		}
		return strings.Join(parts, "|") + "GB"
	}
	if r.MinGB == r.MaxGB {
		return strconv.FormatInt(r.MinGB, 10) + "GB"
	}
	return strconv.FormatInt(r.MinGB, 10) + "-" + strconv.FormatInt(r.MaxGB, 10) + "GB"
}

var chipTier = map[string]int{"": 0, "pro": 1, "max": 2, "ultra": 3}

// sizeRangeRe matches "48-64GB" / "48–64 GB" / "48—64 gb".
var sizeRangeRe = regexp.MustCompile(`(?i)(\d+)\s*[-–—]\s*(\d+)\s*(gb|tb)\b`)

// sizeOrRe matches "48GB or 64GB" / "32 gb / 64 gb" alternatives.
var sizeOrRe = regexp.MustCompile(`(?i)(\d+)\s*(gb|tb)\s*(?:or|/)\s*(\d+)\s*(gb|tb)\b`)

// sizeRe matches a bare "64GB" / "1 TB" mention. Run after the range and
// or patterns have consumed theirs.
var sizeRe = regexp.MustCompile(`(?i)\b(\d+)\s*(gb|tb)\b`)

// chipRe matches Apple-silicon tokens: m4, M5 Pro, M3 MAX…
var chipRe = regexp.MustCompile(`(?i)\bm\s*(\d)\s*(pro|max|ultra)?\b`)

func gbOf(n, unit string) int64 {
	v, _ := strconv.ParseInt(n, 10, 64)
	if strings.EqualFold(unit, "tb") {
		return v * 1024
	}
	return v
}

// parseSpec scans a criterion for machine-checkable spec tokens. It
// returns the parsed requirements plus the criterion text with every
// matched span removed — an empty residue means the criterion was pure
// spec and earns no jeff question.
func parseSpec(c string) (sizes []SizeReq, chips []ChipReq, residue string) {
	residue = c
	for _, m := range sizeRangeRe.FindAllStringSubmatch(residue, -1) {
		lo, hi := gbOf(m[1], m[3]), gbOf(m[2], m[3])
		if lo > hi {
			lo, hi = hi, lo
		}
		sizes = append(sizes, SizeReq{MinGB: lo, MaxGB: hi})
	}
	residue = sizeRangeRe.ReplaceAllString(residue, " ")
	for _, m := range sizeOrRe.FindAllStringSubmatch(residue, -1) {
		a, b := gbOf(m[1], m[2]), gbOf(m[3], m[4])
		sizes = append(sizes, SizeReq{MinGB: min(a, b), MaxGB: max(a, b), Points: []int64{a, b}})
	}
	residue = sizeOrRe.ReplaceAllString(residue, " ")
	for _, m := range sizeRe.FindAllStringSubmatch(residue, -1) {
		v := gbOf(m[1], m[2])
		sizes = append(sizes, SizeReq{MinGB: v, MaxGB: v})
	}
	residue = sizeRe.ReplaceAllString(residue, " ")
	for _, m := range chipRe.FindAllStringSubmatch(residue, -1) {
		gen, _ := strconv.Atoi(m[1])
		chips = append(chips, ChipReq{Gen: gen, MinTier: chipTier[strings.ToLower(m[2])]})
	}
	residue = chipRe.ReplaceAllString(residue, " ")
	return sizes, chips, residue
}

// specOnly reports whether a criterion carried nothing but spec tokens —
// residue without letters means there is no subjective clause left for
// jeff to judge.
func specOnly(residue string) bool {
	return !strings.ContainsFunc(residue, unicode.IsLetter)
}

// productSizes extracts every GB-normalized size a product discloses —
// name, description and the variant matrix titles all count as config
// evidence.
func productSizes(text string) map[int64]bool {
	set := map[int64]bool{}
	for _, m := range sizeRe.FindAllStringSubmatch(text, -1) {
		set[gbOf(m[1], m[2])] = true
	}
	return set
}

// chipMention is one m-series token found in product text.
type chipMention struct{ gen, tier int }

func productChips(text string) []chipMention {
	var out []chipMention
	for _, m := range chipRe.FindAllStringSubmatch(text, -1) {
		gen, _ := strconv.Atoi(m[1])
		out = append(out, chipMention{gen, chipTier[strings.ToLower(m[2])]})
	}
	return out
}

// sortedKeys renders a size set deterministically for exclusion detail.
func sortedKeys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}
