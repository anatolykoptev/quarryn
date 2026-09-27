package orders

import (
	"strings"
	"testing"
	"time"
)

const amazonEML = `From: auto-confirm@amazon.com
Subject: Ordered: "USB-C Cable"
Date: Mon, 15 Sep 2025 10:30:00 +0000
Content-Type: text/plain; charset=utf-8

Hello,

Your order has been placed.

Order #112-1234567-7654321
Order Total $42.99

Thanks,
Amazon
`

const amazonShipEML = `From: shipment-tracking@amazon.com
Subject: Your Amazon order has shipped
Date: Wed, 17 Sep 2025 08:00:00 +0000
Content-Type: text/plain; charset=utf-8

Good news — your order has shipped.

Order #112-1234567-7654321
Tracking number: 1Z999AA10123456784
Carrier: UPS
`

const ebayEML = `From: ebay@ebay.com
Subject: Order confirmed
Date: Fri, 19 Sep 2025 12:00:00 +0000
Content-Type: text/plain; charset=utf-8

Order number: 03-12345-67890
Order total: $15.50
`

const htmlEML = `From: orders@walmart.com
Subject: Thanks for your order
Content-Type: text/html; charset=utf-8

<html><body>
<p>Order number</p><p>5820123456</p>
<table><tr><td>Total</td><td>$199.00</td></tr></table>
</body></html>`

const multipartEML = `From: auto-confirm@amazon.com
Subject: Ordered: "Speaker"
Content-Type: multipart/alternative; boundary=XYZ

--XYZ
Content-Type: text/plain

Order #114-5555666-7778888
Order Total $89.95
--XYZ
Content-Type: text/html

<html><body><p>Order #114-5555666-7778888</p></body></html>
--XYZ--
`

const junkEML = `From: newsletter@randomblog.io
Subject: Weekly digest
Content-Type: text/plain

Hello! Here are this week's links.
`

func TestParseAmazonConfirm(t *testing.T) {
	p, err := ParseEmail([]byte(amazonEML))
	if err != nil {
		t.Fatal(err)
	}
	if p.OrderNo != "112-1234567-7654321" {
		t.Errorf("order_no = %q", p.OrderNo)
	}
	if p.TotalMinor == nil || *p.TotalMinor != 4299 {
		t.Errorf("total = %v, want 4299", p.TotalMinor)
	}
	if p.Currency != "USD" {
		t.Errorf("currency = %q", p.Currency)
	}
	if p.RetailerDomain != "amazon.com" || p.RetailerName != "Amazon" {
		t.Errorf("retailer = %q/%q", p.RetailerDomain, p.RetailerName)
	}
	if p.PlacedAt == nil {
		t.Error("placed_at not parsed from Date header")
	}
}

func TestParseShippedWithTracking(t *testing.T) {
	p, err := ParseEmail([]byte(amazonShipEML))
	if err != nil {
		t.Fatal(err)
	}
	if p.TrackingNo != "1Z999AA10123456784" || p.Carrier != "ups" {
		t.Errorf("tracking = %q %q", p.Carrier, p.TrackingNo)
	}
	if !strings.Contains(p.TrackURL, "1Z999AA10123456784") {
		t.Errorf("track_url = %q", p.TrackURL)
	}
}

func TestParseEbay(t *testing.T) {
	p, _ := ParseEmail([]byte(ebayEML))
	if p.OrderNo != "03-12345-67890" {
		t.Errorf("ebay order_no = %q", p.OrderNo)
	}
	if p.TotalMinor == nil || *p.TotalMinor != 1550 {
		t.Errorf("total = %v", p.TotalMinor)
	}
}

// Html flattening must keep cell boundaries — the total lives in the
// cell after "Total".
func TestParseHTMLTotal(t *testing.T) {
	p, err := ParseEmail([]byte(htmlEML))
	if err != nil {
		t.Fatal(err)
	}
	if p.OrderNo != "5820123456" {
		t.Errorf("walmart order_no = %q", p.OrderNo)
	}
	if p.TotalMinor == nil || *p.TotalMinor != 19900 {
		t.Errorf("total = %v", p.TotalMinor)
	}
}

func TestParseMultipartPrefersPlain(t *testing.T) {
	p, err := ParseEmail([]byte(multipartEML))
	if err != nil {
		t.Fatal(err)
	}
	if p.OrderNo != "114-5555666-7778888" {
		t.Errorf("order_no = %q", p.OrderNo)
	}
	if p.TotalMinor == nil || *p.TotalMinor != 8995 {
		t.Errorf("total = %v", p.TotalMinor)
	}
}

// Junk mail must not fabricate an order — the row lands unparsed so
// nothing is silently dropped.
func TestParseJunkIsUnparsed(t *testing.T) {
	p, err := ParseEmail([]byte(junkEML))
	if err != nil {
		t.Fatal(err)
	}
	if p.OrderNo != "" || p.TotalMinor != nil {
		t.Errorf("junk parsed fields: %+v", p)
	}
	if p.UnparsedReason == "" {
		t.Error("junk email should carry UnparsedReason")
	}
	if p.RetailerDomain != "randomblog.io" {
		t.Errorf("domain = %q", p.RetailerDomain)
	}
}

// FedEx/DHL shapes collide with order numbers — the keyword window is
// the gate: bare 10-digit order numbers must not become DHL tracking.
func TestTrackingKeywordWindow(t *testing.T) {
	text := "Order number 1234567890. It will arrive soon."
	if tn, _ := findTracking(text); tn != "" {
		t.Fatalf("bare order number matched tracking: %q", tn)
	}
	text = "Order number 1234567890. Tracking number 1234567890, FedEx."
	if tn, c := findTracking(text); tn == "" || c == "" {
		t.Fatalf("keyword-window tracking not found")
	}
}

func TestReturnByKnownRetailer(t *testing.T) {
	placed := time.Date(2025, 9, 15, 0, 0, 0, 0, time.UTC)
	rb := returnBy("amazon.com", placed)
	if rb == nil {
		t.Fatal("returnBy nil for amazon")
	}
	if d := rb.Sub(placed); d != 30*24*time.Hour {
		t.Errorf("amazon window = %v, want 30d", d)
	}
	if rb := returnBy("unknown-shop.io", placed); rb != nil {
		t.Error("unknown retailer must not get a guessed window")
	}
}
