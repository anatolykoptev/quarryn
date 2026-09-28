package match

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Deterministic spec matching (issue #111): subjective criteria like
// "64GB", "48–64GB" or "M5 Pro" went to jeff verbatim, and the judge
// passed contradictions on vibes — a 24GB listing scored 0.69 against
// "48 or 64GB". Spec tokens are machine-checkable facts: parse them out
// of free-text criteria into constraints the prefilter applies BEFORE
// the ask. The jeff question stays — it still judges products whose
// disclosure is inconclusive.

// SizeClass separates RAM mentions from storage mentions: a listing that
// says "24GB RAM + 64GB storage" must not satisfy a "64GB RAM" ask.
// ClassAny is used when the criterion gave no hint either way.
type SizeClass int

const (
	// ClassAny marks a size mention or ask with no RAM/storage hint.
	ClassAny SizeClass = iota
	// ClassRAM marks "64GB RAM" / "unified memory" mentions.
	ClassRAM
	// ClassStorage marks "1TB SSD" / "storage" mentions.
	ClassStorage
)

// SizeReq is a GB-normalized size requirement. A bare "64GB" is the
// interval [64,64]; a range "48–64GB" is [48,64]. An alternatives list
// ("32GB or 64GB") instead fills Points — the set is discrete because a
// config like 48GB sits inside the span yet satisfies neither option.
// GB↔TB normalize to gigabytes at ×1024 — listings and criteria mix
// units ("1TB SSD" vs "1024GB").
type SizeReq struct {
	MinGB  int64     `json:"min_gb"`
	MaxGB  int64     `json:"max_gb"`
	Points []int64   `json:"points,omitempty"` // discrete alternatives; empty = interval
	Class  SizeClass `json:"class,omitempty"`
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

// satisfiedBy reports whether one disclosed size meets the requirement,
// honoring the class hint: a RAM ask accepts ram-labeled or unlabeled
// mentions but not storage-labeled ones.
func (r SizeReq) satisfiedBy(m sizeMention) bool {
	if r.Class != ClassAny && m.class != ClassAny && m.class != r.Class {
		return false
	}
	if len(r.Points) > 0 {
		return slices.Contains(r.Points, m.gb)
	}
	return m.gb >= r.MinGB && m.gb <= r.MaxGB
}

// answerableBy reports whether the row even discloses a size the
// requirement could speak to — an all-storage row cannot contradict a
// RAM ask, it is simply inconclusive on it.
func (r SizeReq) answerableBy(ms []sizeMention) bool {
	for _, m := range ms {
		if r.Class == ClassAny || m.class == ClassAny || m.class == r.Class {
			return true
		}
	}
	return false
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

var ramHintRe = regexp.MustCompile(`(?i)\b(ram|memory|unified)\b`)
var storageHintRe = regexp.MustCompile(`(?i)\b(ssd|storage|disk|hdd|nvme|flash)\b`)

func gbOf(n, unit string) int64 {
	v, _ := strconv.ParseInt(n, 10, 64)
	if strings.EqualFold(unit, "tb") {
		return v * 1024
	}
	return v
}

// sizeClassOf finds the class-hint keyword NEAREST to the mention within
// ±48 runes: in "24GB RAM 64GB storage" both windows see both words, so
// presence-order voting is wrong. On a distance tie the hint AFTER the
// number wins — English sizes read "64GB RAM" far more often than
// "RAM 64GB".
func sizeClassOf(text string, start, end int) SizeClass {
	lo, hi := start-48, end+48
	if lo < 0 {
		lo = 0
	}
	if hi > len(text) {
		hi = len(text)
	}
	bestDist, bestSide, cls := 1<<30, 0, ClassAny // side: -1 left, +1 right
	consider := func(re *regexp.Regexp, c SizeClass) {
		for _, m := range re.FindAllStringIndex(text[lo:hi], -1) {
			h0, h1 := lo+m[0], lo+m[1]
			d, side := spanDist(start, end, h0, h1), -1
			if h0 >= end {
				side = 1
			}
			if d < bestDist || (d == bestDist && side > bestSide) {
				bestDist, bestSide, cls = d, side, c
			}
		}
	}
	consider(ramHintRe, ClassRAM)
	consider(storageHintRe, ClassStorage)
	return cls
}

// spanDist is the gap between two byte spans (0 when they overlap or
// touch), used to pick the class hint closest to a size mention.
func spanDist(a0, a1, b0, b1 int) int {
	if a1 < b0 {
		return b0 - a1
	}
	if b1 < a0 {
		return a0 - b1
	}
	return 0
}

// criterionClass applies one class hint to every size a criterion
// emits — "64GB RAM" marks the ask RAM; "1TB SSD" marks storage.
func criterionClass(c string) SizeClass {
	if ramHintRe.MatchString(c) {
		return ClassRAM
	}
	if storageHintRe.MatchString(c) {
		return ClassStorage
	}
	return ClassAny
}

// parseSpec scans a criterion for machine-checkable spec tokens and
// returns the parsed requirements.
func parseSpec(c string) (sizes []SizeReq, chips []ChipReq) {
	class := criterionClass(c)
	for _, m := range sizeRangeRe.FindAllStringSubmatch(c, -1) {
		lo, hi := gbOf(m[1], m[3]), gbOf(m[2], m[3])
		if lo > hi {
			lo, hi = hi, lo
		}
		sizes = append(sizes, SizeReq{MinGB: lo, MaxGB: hi, Class: class})
	}
	rest := sizeRangeRe.ReplaceAllString(c, " ")
	for _, m := range sizeOrRe.FindAllStringSubmatch(rest, -1) {
		a, b := gbOf(m[1], m[2]), gbOf(m[3], m[4])
		sizes = append(sizes, SizeReq{MinGB: min(a, b), MaxGB: max(a, b),
			Points: []int64{a, b}, Class: class})
	}
	rest = sizeOrRe.ReplaceAllString(rest, " ")
	for _, m := range sizeRe.FindAllStringSubmatch(rest, -1) {
		v := gbOf(m[1], m[2])
		sizes = append(sizes, SizeReq{MinGB: v, MaxGB: v, Class: class})
	}
	for _, m := range chipRe.FindAllStringSubmatch(c, -1) {
		gen, _ := strconv.Atoi(m[1])
		chips = append(chips, ChipReq{Gen: gen, MinTier: chipTier[strings.ToLower(m[2])]})
	}
	return sizes, chips
}

// sizeMention is one size a product row discloses, with its class hint.
type sizeMention struct {
	gb    int64
	class SizeClass
}

func rowSizes(text string) []sizeMention {
	idx := sizeRe.FindAllStringSubmatchIndex(text, -1)
	out := make([]sizeMention, 0, len(idx))
	for _, m := range idx {
		out = append(out, sizeMention{
			gb:    gbOf(text[m[2]:m[3]], text[m[4]:m[5]]),
			class: sizeClassOf(text, m[0], m[1]),
		})
	}
	return out
}

// chipMention is one m-series token found in product text.
type chipMention struct{ gen, tier int }

func rowChips(text string) []chipMention {
	var out []chipMention
	for _, m := range chipRe.FindAllStringSubmatch(text, -1) {
		gen, _ := strconv.Atoi(m[1])
		out = append(out, chipMention{gen, chipTier[strings.ToLower(m[2])]})
	}
	return out
}
