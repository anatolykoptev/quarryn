package orders

import "regexp"

// Carrier tracking extraction. UPS/USPS numbers are structurally
// distinctive; FedEx/DHL numeric shapes collide with order numbers, so
// they only count within proximity of a tracking keyword.
var (
	reUPS   = regexp.MustCompile(`\b1Z[0-9A-Z]{16}\b`)
	reUSPS  = regexp.MustCompile(`\b9[0-9]{21,23}\b`)
	reFedEx = regexp.MustCompile(`\b[0-9]{12}\b|\b[0-9]{15}\b|\b[0-9]{20}\b`)
	reDHL   = regexp.MustCompile(`\b[0-9]{10}\b`)
)

// trackKeyword windows the ambiguous-numeric match: a bare 10-digit run
// elsewhere in the email is noise, one near "tracking" is a number.
var trackKeyword = regexp.MustCompile(`(?i)track`)

// findTracking returns the first carrier-recognizable tracking number.
// UPS/USPS patterns match anywhere; FedEx/DHL only inside a keyword
// window (order numbers routinely share their shape).
func findTracking(text string) (tracking, carrier string) {
	if m := reUPS.FindString(text); m != "" {
		return m, "ups"
	}
	if m := reUSPS.FindString(text); m != "" {
		return m, "usps"
	}
	for _, kw := range trackKeyword.FindAllStringIndex(text, -1) {
		lo := kw[0] - 60
		if lo < 0 {
			lo = 0
		}
		hi := kw[1] + 60
		if hi > len(text) {
			hi = len(text)
		}
		win := text[lo:hi]
		if m := reFedEx.FindString(win); m != "" {
			return m, "fedex"
		}
		if m := reDHL.FindString(win); m != "" {
			return m, "dhl"
		}
	}
	return "", ""
}

// trackURL is the carrier's public tracking deep link — no API, the
// operator clicks through. Unknown carrier → empty.
func trackURL(carrier, tracking string) string {
	switch carrier {
	case "ups":
		return "https://www.ups.com/track?tracknum=" + tracking
	case "usps":
		return "https://tools.usps.com/go/TrackConfirmAction?tLabels=" + tracking
	case "fedex":
		return "https://www.fedex.com/fedextrack/?trknbr=" + tracking
	case "dhl":
		return "https://www.dhl.com/us-en/home/tracking/tracking-express.html?submit=1&tracking-id=" + tracking
	}
	return ""
}
