// Package sources implements the candidate-sourcing stage of
// quarryn: marketplace adapters (ADR-16) plus the merge/dedup
// funnel (ADR-8) that fans a query out to every enabled adapter, SSRF-checks
// every candidate URL (ADR-14), fuses and dedups the result sets, and caps
// the pool for downstream scoring.
//
// Adapters implement go-engine's sources.Source and declare their fetch
// capability via SourceSpec (ADR-13). Product fields ride in
// sources.Result.Metadata under the Meta* keys and are lifted into typed
// Candidate fields at the funnel boundary — never flattened to markdown
// (the go-job lesson: typed records end to end).
package sources

import (
	"context"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-kit/wowa"
)

// FetchClass declares how an adapter retrieves upstream data (ADR-13).
// Downstream stages use it to decide which wowa path a re-fetch or
// enrichment may escalate to.
type FetchClass string

const (
	// FetchClassAPI is a first-party JSON API over direct HTTPS (eBay
	// Browse, Etsy v3). Credentials come from env; the adapter ships dark
	// without them.
	FetchClassAPI FetchClass = "api"
	// FetchClassFetch is third-party content pulled through go-wowa's
	// /api/v1/fetch (ADR-1: third-party traffic never leaves this service
	// directly).
	FetchClassFetch FetchClass = "fetch"
	// FetchClassRender is third-party content needing JS rendering via
	// go-wowa /api/v1/render. No v1 adapter uses it; declared so the enum is
	// complete for later adapters.
	FetchClassRender FetchClass = "render"
)

// SourceSpec is an adapter's static capability declaration (ADR-13).
type SourceSpec struct {
	// FetchClass declares how the adapter fetches upstream data.
	FetchClass FetchClass
	// Manifest is the adapter's declared contract — allowed egress hosts
	// and the user-session flag. Enforced by the funnel at the boundary.
	Manifest Manifest
	// ResolveOutbound marks deal-aggregator sources whose listing URL is a
	// thread/discussion page rather than a buyable product page (slickdeals
	// /f/ threads). SERP-complete cards from such sources still take the
	// interact tier (top-N) to resolve the outbound merchant link and
	// verify against the real store page.
	ResolveOutbound bool
}

// Adapter is a marketplace source connector: a go-engine sources.Source
// plus the self-description the registry and funnel need — its capability
// Spec, and Enabled so credential-less adapters ship dark: present in the
// registry, skipped at dispatch (ADR-16).
type Adapter interface {
	sources.Source
	// Spec returns the adapter's static capability declaration.
	Spec() SourceSpec
	// Enabled reports whether the adapter has enough configuration to run
	// (API key present, wowa fetcher wired, shop list non-empty). Disabled
	// adapters are logged and skipped — never an error.
	Enabled() bool
}

// Fetcher is the narrow slice of go-kit/wowa that fetch-class adapters use.
// *wowa.Client satisfies it; tests point a real client at a stubbed
// /api/v1/fetch.
type Fetcher interface {
	Fetch(ctx context.Context, req wowa.FetchRequest) (*wowa.FetchResponse, error)
}

// Canonical Metadata keys adapters emit. The funnel lifts keys that have a
// Candidate field into typed values; the rest pass through into
// Candidate.Metadata so adapters can add fields without a schema change.
const (
	// MetaSource names the adapter a result came from. Set by the funnel at
	// collection time — adapters do not set it.
	MetaSource = "source"
	// MetaPrice is a decimal string in MetaCurrency units ("19.99").
	MetaPrice = "price"
	// MetaCurrency is an ISO 4217 code ("USD").
	MetaCurrency = "currency"
	// MetaCondition is the item condition ("NEW", "USED", "REFURBISHED").
	MetaCondition = "condition"
	// MetaAvailability is one of the Availability* values.
	MetaAvailability = "availability"
	// MetaSeller is the seller username or shop domain.
	MetaSeller = "seller"
	// MetaBuyingOptions is a comma-joined list ("FIXED_PRICE,BEST_OFFER").
	MetaBuyingOptions = "buying_options"
	// MetaDiscountPct is the percent off list price, 0-100 decimal string.
	MetaDiscountPct = "discount_pct"
	// MetaThumbs is an integer community score (slickdeals thumbs-up count).
	MetaThumbs = "thumbs"
	// MetaMerchant is the deal merchant name (slickdeals).
	MetaMerchant = "merchant"
	// MetaListingID is the upstream listing/product id.
	MetaListingID = "listing_id"
	// MetaImageURL is the primary product image.
	MetaImageURL = "image_url"
)

// Availability vocabulary for MetaAvailability.
const (
	AvailabilityInStock    = "in_stock"
	AvailabilityOutOfStock = "out_of_stock"
)
