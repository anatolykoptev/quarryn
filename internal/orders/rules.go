package orders

import (
	"regexp"
	"strings"
	"time"

	"github.com/anatolykoptev/quarryn/internal/money"
)

// Retailer rules: keyed on the sender's registrable domain. Each rule
// gives a display name, the order-number regex (first capture wins), and
// the return window in days. Domains are eTLD+1 — sub-senders
// (auto-confirm@, orders@, updates@) share them.
type retailerRule struct {
	Domain     string
	Name       string
	OrderNo    *regexp.Regexp
	ReturnDays int
}

var rules = []retailerRule{
	{Domain: "amazon.com", Name: "Amazon",
		OrderNo:    regexp.MustCompile(`(?i)\border\s*(?:number|#|no\.?)[:\s]*([0-9]{3}-[0-9]{7}-[0-9]{7})`),
		ReturnDays: 30},
	{Domain: "amazon.ca", Name: "Amazon CA",
		OrderNo:    regexp.MustCompile(`(?i)\border\s*(?:number|#|no\.?)[:\s]*([0-9]{3}-[0-9]{7}-[0-9]{7})`),
		ReturnDays: 30},
	{Domain: "amazon.co.uk", Name: "Amazon UK",
		OrderNo:    regexp.MustCompile(`(?i)\border\s*(?:number|#|no\.?)[:\s]*([0-9]{3}-[0-9]{7}-[0-9]{7})`),
		ReturnDays: 30},
	{Domain: "ebay.com", Name: "eBay",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*([0-9]{2}-[0-9]{5}-[0-9]{5})`),
		ReturnDays: 30},
	{Domain: "walmart.com", Name: "Walmart",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*([0-9]{6,})`),
		ReturnDays: 90},
	{Domain: "bestbuy.com", Name: "Best Buy",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*(BBY[0-9-]+)`),
		ReturnDays: 15},
	{Domain: "target.com", Name: "Target",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*([0-9]{6,})`),
		ReturnDays: 90},
	{Domain: "etsy.com", Name: "Etsy",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*([0-9]{6,})`),
		ReturnDays: 30},
	{Domain: "newegg.com", Name: "Newegg",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*([0-9]{6,})`),
		ReturnDays: 30},
	{Domain: "costco.com", Name: "Costco",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*([0-9]{6,})`),
		ReturnDays: 90},
	{Domain: "apple.com", Name: "Apple",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*(W[0-9]{8,})`),
		ReturnDays: 14},
	{Domain: "bhphotovideo.com", Name: "B&H",
		OrderNo:    regexp.MustCompile(`(?i)order\s*(?:number|#|no\.?)[:\s]*([0-9]{7,})`),
		ReturnDays: 30},
}

// genericOrderNo is the unknown-sender fallback — "Order #12345" shapes
// are near-universal; the gate is requiring the literal keyword.
var genericOrderNo = regexp.MustCompile(
	`(?i)order\s*(?:number|#|no\.?|id)[:\s#]*([A-Z0-9][-A-Z0-9]{4,24}[A-Z0-9])`)

// senderRule maps the From address to the retailer rule by registrable
// domain (subdomain-tolerant: mail order confirmations come from
// <something>@<mailer>.<domain>).
func senderRule(from, _ string) (domain, name string) {
	d := domainOf(from)
	for _, r := range rules {
		if d == r.Domain || strings.HasSuffix(d, "."+r.Domain) {
			return r.Domain, r.Name
		}
	}
	return "", ""
}

func findRule(domain string) *retailerRule {
	for i := range rules {
		if rules[i].Domain == domain {
			return &rules[i]
		}
	}
	return nil
}

// findOrderNo applies the retailer regex first, then the generic one.
func findOrderNo(domain, text string) string {
	if r := findRule(domain); r != nil {
		if m := r.OrderNo.FindStringSubmatch(text); m != nil {
			return m[1]
		}
	}
	if m := genericOrderNo.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// Total line patterns — "Order Total", "Grand Total", "Total:" followed
// by a currency amount. Money parse reuses internal/money (no floats).
var (
	totalLine = regexp.MustCompile(
		`(?im)(?:order\s+total|grand\s+total|total(?:\s+charged|\s+due)?)[^\n$€£]{0,30}([$€£])\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
	symbolCurrency = map[string]string{"$": "USD", "€": "EUR", "£": "GBP"}
)

func findTotal(text string) (*int64, string) {
	m := totalLine.FindStringSubmatch(text)
	if m == nil {
		return nil, ""
	}
	cur := symbolCurrency[m[1]]
	amount := strings.ReplaceAll(m[2], ",", "")
	if minor, ok := money.ToMinor(amount, cur); ok {
		return &minor, cur
	}
	return nil, ""
}

// findLabel prefers the retailer display name; unknown senders get the
// subject tail (the first line is usually "Order Confirmation for …").
func findLabel(domain, name, subject, _ string) string {
	if name != "" {
		return name
	}
	s := strings.TrimSpace(subject)
	if len(s) > 80 {
		s = s[:80]
	}
	if s != "" {
		return s
	}
	return domain
}

// returnBy computes the return-window deadline from the rules table;
// unknown retailer → nil (honest absence, not a guessed 30d).
func returnBy(domain string, placedAt time.Time) *time.Time {
	r := findRule(domain)
	if r == nil {
		return nil
	}
	t := placedAt.Add(time.Duration(r.ReturnDays) * 24 * time.Hour)
	return &t
}
