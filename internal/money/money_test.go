package money

import "testing"

func TestToMinor(t *testing.T) {
	cases := []struct {
		in   string
		cur  string
		want int64
	}{
		{"278.00", "USD", 27800},
		{"$1,299.00", "USD", 129900},
		{"1.299,00 €", "EUR", 129900},
		{"1 299,00", "EUR", 129900},
		{"12,50", "EUR", 1250},
		{"USD 9.99", "USD", 999},
		{"1,299", "USD", 129900}, // 3-digit tail: US thousands grouping
		{"1999", "JPY", 1999},    // zero-decimal: minor == major
		{"1999", "USD", 199900},
		{"0.01", "USD", 1},
		{"-5.00", "USD", -500},
	}
	for _, c := range cases {
		got, ok := ToMinor(c.in, c.cur)
		if !ok || got != c.want {
			t.Errorf("ToMinor(%q, %q) = %v,%v want %v", c.in, c.cur, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "no price", "abc"} {
		if _, ok := ToMinor(bad, "USD"); ok {
			t.Errorf("ToMinor(%q) unexpectedly succeeded", bad)
		}
	}
}

// TestToMinorFloatPrecision is the regression this package exists for:
// ParseFloat("29.99")*100 is 2998.999… — a price equal to a price_max bound
// would float-compare as strictly greater and get excluded. Digit-level
// parsing must land on exactly these literals.
func TestToMinorFloatPrecision(t *testing.T) {
	for s, want := range map[string]int64{
		"29.99":  2999,
		"19.95":  1995,
		"0.29":   29,
		"119.95": 11995,
	} {
		if got, ok := ToMinor(s, "USD"); !ok || got != want {
			t.Errorf("ToMinor(%q) = %d,%v want %d", s, got, ok, want)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		minor int64
		cur   string
		want  string
	}{
		{1999, "USD", "19.99"},
		{3000, "JPY", "3000"},
		{129900, "USD", "1299.00"},
		{1, "USD", "0.01"},
		{-500, "USD", "-5.00"},
		{12345, "KWD", "12.345"},
	}
	for _, c := range cases {
		if got := Format(c.minor, c.cur); got != c.want {
			t.Errorf("Format(%d, %q) = %q, want %q", c.minor, c.cur, got, c.want)
		}
	}
}
