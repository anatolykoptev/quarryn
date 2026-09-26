// Package money keeps prices as integer minor units through the pipeline:
// "19.99" USD is 1999, ¥1999 JPY is 1999. Float64 pricing was a latent
// comparison bug — strconv.ParseFloat("29.99") lands 1e-15 under the
// literal and could flip a price_max boundary. Decimal strings convert at
// the ingest seams (schema.org, LLM payloads, adapter MetaPrice) and back
// at egress; nothing between them sees a float.
package money

import (
	"strconv"
	"strings"
)

// Decimals returns the ISO 4217 exponent — how many minor units sit in one
// major unit. Default 2; the tables below are the exceptions.
func Decimals(currency string) int {
	switch currency {
	// Zero-decimal currencies: the minor unit IS the major unit.
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW",
		"PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	// Three decimals: Kuwaiti/Bahraini/etc dinars.
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	// Four decimals: CLF (Chilean unit of account), UYW (Uruguay index).
	case "CLF", "UYW":
		return 4
	default:
		return 2
	}
}

// ToMinor converts a decimal amount string ("19.99", "1 299,00", "$27.96")
// into minor units for currency. The parse is digit-level: no float64 ever
// forms, so "29.99" is exactly 2999 — never 2998.9999. Returns false when
// no numeric token exists.
func ToMinor(s, currency string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	intPart, fracPart, neg, ok := splitDecimal(s)
	if !ok {
		return 0, false
	}
	dec := Decimals(currency)
	// Truncate or pad the fraction to the currency's exponent; a residual
	// digit beyond the exponent is scraper noise, not precision.
	if len(fracPart) > dec {
		fracPart = fracPart[:dec]
	}
	for len(fracPart) < dec {
		fracPart += "0"
	}
	whole, err := strconv.ParseInt(intPart+fracPart, 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		whole = -whole
	}
	return whole, true
}

// FromFloat converts an already-decoded float64 (LLM payloads, constraint
// bounds) into minor units via its shortest decimal form — "19.99" parses
// digit-exact, so no float error survives the conversion.
func FromFloat(f float64, currency string) (int64, bool) {
	return ToMinor(strconv.FormatFloat(f, 'f', -1, 64), currency)
}

// Decimal renders minor units as the decimal float the wire and jeff-facing
// structures expect ("price": 19.99). The float is output-only — it is
// never compared or fed back into an ingest path.
func Decimal(minor int64, currency string) float64 {
	div := 1.0
	for i := 0; i < Decimals(currency); i++ {
		div *= 10
	}
	return float64(minor) / div
}

// Format renders minor units as a decimal string honoring the currency's
// exponent: 3000 JPY -> "3000", 1999 USD -> "19.99". Used in reason
// details where a float artifact ("19.989999") would be a bug in the text
// itself.
func Format(minor int64, currency string) string {
	dec := Decimals(currency)
	neg := minor < 0
	if neg {
		minor = -minor
	}
	digits := strconv.FormatInt(minor, 10)
	for len(digits) <= dec {
		digits = "0" + digits
	}
	whole, frac := digits[:len(digits)-dec], digits[len(digits)-dec:]
	if dec == 0 {
		frac = ""
	}
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

// DecimalPtr renders *minor as the egress decimal pointer; nil stays nil —
// an absent price must not materialize as 0.00.
func DecimalPtr(minor *int64, currency string) *float64 {
	if minor == nil {
		return nil
	}
	f := Decimal(*minor, currency)
	return &f
}

// splitDecimal extracts the integer and fraction digits of the first
// numeric token in s. Comma and dot are both accepted as the decimal
// separator — whichever appears LAST is the separator (so "1,299.00" and
// "1 299,00" both parse as 1299.00); the other is treated as thousands
// grouping and dropped.
func splitDecimal(s string) (intDigits, fracDigits string, neg, ok bool) {
	start := strings.IndexFunc(s, func(r rune) bool {
		return r >= '0' && r <= '9'
	})
	if start < 0 {
		return "", "", false, false
	}
	neg = strings.ContainsRune(s[:start], '-')
	token := numericToken(s[start:])
	return splitToken(token, neg)
}

// numericToken returns the longest run of digits and separator/grouping
// characters starting at s[0] (which is a digit by construction).
func numericToken(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == ',' || c == ' ' || c == '\u00a0' || c == '\'' {
			continue
		}
		return s[:i]
	}
	return s
}

// splitToken divides a numeric token into integer and fraction digit
// strings. Whichever of '.' or ',' appears LAST is the decimal separator;
// the rest is thousands grouping and dropped.
func splitToken(token string, neg bool) (intDigits, fracDigits string, _ bool, ok bool) {
	lastSep := -1
	for i := 0; i < len(token); i++ {
		if token[i] == '.' || token[i] == ',' {
			lastSep = i
		}
	}
	var intB, fracB strings.Builder
	for i := 0; i < len(token); i++ {
		if c := token[i]; c >= '0' && c <= '9' {
			if lastSep >= 0 && i > lastSep {
				fracB.WriteByte(c)
			} else {
				intB.WriteByte(c)
			}
		}
	}
	intDigits, fracDigits = intB.String(), fracB.String()
	// "1,299" is ambiguous: US thousands or EU decimal. A 3-digit tail is
	// far more often grouping than a milliprice — fold it into the integer.
	if len(fracDigits) == 3 && intDigits != "" {
		intDigits += fracDigits
		fracDigits = ""
	}
	if intDigits == "" {
		intDigits = "0"
	}
	return intDigits, fracDigits, neg, true
}
