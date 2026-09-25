package sources

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

const (
	// slickdealsDefaultFeedURL is the newsearch.php endpoint that serves RSS
	// when rss=1 is passed — frontpage mode (no query) or keyword search.
	slickdealsDefaultFeedURL = "https://slickdeals.net/newsearch.php"
	// slickdealsFetchTimeoutSecs bounds the wowa fetch of the feed.
	slickdealsFetchTimeoutSecs = 30
)

// Extraction patterns for fields slickdeals embeds in the item description
// HTML rather than exposing as RSS elements.
var (
	// Price: "$19.99" / "Price: $1,299.00" — first dollar amount wins.
	slickdealsPriceRe = regexp.MustCompile(`\$\s*([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	// Thumbs: "Thumbs: 42" / "thumbs up: 42" / "42 thumbs" — both orders.
	slickdealsThumbsLabelRe = regexp.MustCompile(`(?i)thumbs?[^0-9]{0,10}(\d+)`)
	slickdealsThumbsFirstRe = regexp.MustCompile(`(?i)(\d+)\s*thumbs?`)
	// Merchant: "Merchant: Amazon" up to markup/punctuation boundary.
	slickdealsMerchantRe = regexp.MustCompile(`(?i)merchant\s*:\s*([^<,;\n]+)`)
)

// slickdealsAdapter reads the slickdeals frontpage/search RSS feed. Third-
// party traffic goes through go-wowa Fetch (ADR-1) — the adapter never dials
// slickdeals.net directly. Ships dark when no Fetcher is wired.
type slickdealsAdapter struct {
	fetch       Fetcher
	feedURL     string
	timeoutSecs int
}

// NewSlickdeals returns the slickdeals RSS adapter. feedURL overrides the
// feed endpoint (tests); empty selects the production default.
func NewSlickdeals(fetcher Fetcher, feedURL string) Adapter {
	if feedURL == "" {
		feedURL = slickdealsDefaultFeedURL
	}
	return &slickdealsAdapter{
		fetch:       fetcher,
		feedURL:     feedURL,
		timeoutSecs: slickdealsFetchTimeoutSecs,
	}
}

// Name implements sources.Source.
func (a *slickdealsAdapter) Name() string { return "slickdeals" }

// Spec implements Adapter.
func (a *slickdealsAdapter) Spec() SourceSpec { return SourceSpec{FetchClass: FetchClassFetch} }

// Enabled implements Adapter — needs a wowa fetcher, nothing else.
func (a *slickdealsAdapter) Enabled() bool { return a.fetch != nil }

// rssDocument is the subset of RSS 2.0 we consume.
type rssDocument struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// Search fetches the slickdeals RSS feed for the query (empty query =
// frontpage mode) via wowa and maps items to results.
func (a *slickdealsAdapter) Search(ctx context.Context, q sources.Query) ([]sources.Result, error) {
	if !a.Enabled() {
		return nil, fmt.Errorf("slickdeals: adapter disabled (no wowa fetcher)")
	}
	u := a.feedURL + "?searcharea=host&searchin=first&rss=1"
	if q.Text == "" {
		u += "&mode=frontpage"
	} else {
		u += "&q=" + url.QueryEscape(q.Text)
	}

	resp, err := a.fetch.Fetch(ctx, wowa.FetchRequest{
		URL:         u,
		TimeoutSecs: a.timeoutSecs,
	})
	if err != nil {
		return nil, fmt.Errorf("slickdeals: wowa fetch: %w", err)
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("slickdeals: upstream status %d", resp.Status)
	}

	var doc rssDocument
	if err := xml.Unmarshal([]byte(resp.Body), &doc); err != nil {
		return nil, fmt.Errorf("slickdeals: parse RSS: %w", err)
	}

	out := make([]sources.Result, 0, len(doc.Channel.Items))
	for _, it := range doc.Channel.Items {
		md := map[string]string{}
		if m := slickdealsPriceRe.FindStringSubmatch(it.Description); len(m) == 2 {
			md[MetaPrice] = strings.ReplaceAll(m[1], ",", "")
		}
		if thumbs, ok := slickdealsThumbs(it.Description); ok {
			md[MetaThumbs] = strconv.Itoa(thumbs)
		}
		if m := slickdealsMerchantRe.FindStringSubmatch(it.Description); len(m) == 2 {
			md[MetaMerchant] = strings.TrimSpace(m[1])
		}
		md[MetaAvailability] = AvailabilityInStock // frontpage deals are live
		out = append(out, sources.Result{
			Title:    strings.TrimSpace(it.Title),
			URL:      strings.TrimSpace(it.Link),
			Content:  strings.TrimSpace(it.Title),
			Metadata: md,
		})
	}
	return out, nil
}

// slickdealsThumbs extracts the thumbs-up count from an item description,
// accepting both "Thumbs: 42" and "42 thumbs" orders.
func slickdealsThumbs(desc string) (int, bool) {
	if m := slickdealsThumbsLabelRe.FindStringSubmatch(desc); len(m) == 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return v, true
		}
	}
	if m := slickdealsThumbsFirstRe.FindStringSubmatch(desc); len(m) == 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return v, true
		}
	}
	return 0, false
}
