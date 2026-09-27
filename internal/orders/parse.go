package orders

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ParseEmail turns a raw RFC822 message into a Parsed order candidate.
// It never fails on content shape — the worst outcome is a row carrying
// just sender+subject+date with UnparsedReason set.
func ParseEmail(raw []byte) (*Parsed, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	p := &Parsed{
		Subject: msg.Header.Get("Subject"),
	}
	if from := msg.Header.Get("From"); from != "" {
		if a, err := mail.ParseAddress(from); err == nil {
			p.EmailFrom = a.Address
		} else {
			p.EmailFrom = from
		}
	}
	if d := msg.Header.Get("Date"); d != "" {
		if t, err := mail.ParseDate(d); err == nil {
			p.PlacedAt = &t
		}
	}
	p.RetailerDomain, p.RetailerName = senderRule(p.EmailFrom, p.Subject)
	if p.RetailerDomain == "" {
		p.RetailerDomain = domainOf(p.EmailFrom)
	}

	text := extractText(msg)
	p.OrderNo = findOrderNo(p.RetailerDomain, text)
	p.TotalMinor, p.Currency = findTotal(text)
	p.Label = findLabel(p.RetailerDomain, p.RetailerName, p.Subject, text)
	p.TrackingNo, p.Carrier = findTracking(text)
	if p.TrackingNo != "" {
		p.TrackURL = trackURL(p.Carrier, p.TrackingNo)
	}
	if p.OrderNo == "" && p.TotalMinor == nil {
		p.UnparsedReason = "no order number or total recognized"
	}
	return p, nil
}

// extractText walks MIME parts preferring text/plain; falls back to
// text/html stripped to text. Plain beats html because html flattening
// mangles table layouts (order summaries are tables).
func extractText(msg *mail.Message) string {
	ct := msg.Header.Get("Content-Type")
	mt, params, _ := mime.ParseMediaType(ct)
	if strings.HasPrefix(mt, "multipart/") {
		r := multipart.NewReader(msg.Body, params["boundary"])
		var plain, htmlTxt string
		for {
			part, err := r.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(io.LimitReader(part, 1<<20))
			pt := part.Header.Get("Content-Type")
			switch {
			case strings.HasPrefix(pt, "text/plain"):
				plain = string(b)
			case strings.HasPrefix(pt, "text/html"):
				htmlTxt = htmlToText(b)
			case strings.HasPrefix(pt, "multipart/"):
				// nested multipart (mixed > alternative) — recurse through a
				// synthetic message carrying the part body.
				mt2, ps2, _ := mime.ParseMediaType(pt)
				if strings.HasPrefix(mt2, "multipart/") {
					sub := multipart.NewReader(bytes.NewReader(b), ps2["boundary"])
					plain, htmlTxt = walkParts(sub, plain, htmlTxt)
				}
			}
		}
		if plain != "" {
			return plain
		}
		return htmlTxt
	}
	b, _ := io.ReadAll(io.LimitReader(msg.Body, 1<<20))
	if strings.HasPrefix(mt, "text/html") {
		return htmlToText(b)
	}
	return string(b)
}

func walkParts(r *multipart.Reader, plain, htmlTxt string) (string, string) {
	for {
		part, err := r.NextPart()
		if err != nil {
			return plain, htmlTxt
		}
		b, _ := io.ReadAll(io.LimitReader(part, 1<<20))
		pt := part.Header.Get("Content-Type")
		if strings.HasPrefix(pt, "text/plain") && plain == "" {
			plain = string(b)
		}
		if strings.HasPrefix(pt, "text/html") && htmlTxt == "" {
			htmlTxt = htmlToText(b)
		}
	}
}

// htmlToText flattens an HTML email body to line-oriented text. Tags
// that carry layout meaning (block elements, table cells/rows) become
// newlines; the rest collapses — order confirmations are tables and the
// row boundary is where "Order Total" meets "$99.00".
func htmlToText(b []byte) string {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return string(b)
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "br", "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "table":
				out.WriteByte('\n')
			case "td", "th":
				out.WriteByte(' ')
			case "style", "script", "head":
				return
			}
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return normalizeLines(out.String())
}

var wsRun = regexp.MustCompile(`[ \t]+`)

func normalizeLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(wsRun.ReplaceAllString(line, " "))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}
