package api

import (
	"context"
	"strconv"

	mcpserver "github.com/anatolykoptev/go-mcpserver"
	"github.com/anatolykoptev/quarryn/internal/extract"
	"github.com/anatolykoptev/quarryn/internal/group"
	"github.com/anatolykoptev/quarryn/internal/rank"
	"github.com/anatolykoptev/quarryn/internal/trust"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const productSearchDesc = "Search marketplace adapters for products matching a query and rank them " +
	"against your criteria. Criteria mix deterministic constraints " +
	"(price_max:500, price_min:100, currency:usd, brand:sony, not_keyword:refurbished, availability:in_stock, condition:used) " +
	"with free-text subjective criteria judged per product (\"good battery life\", \"durable build\"). " +
	"Returns ranked products with fused scores, per-criterion verdicts and deal signals " +
	"(discount_pct, thumbs, rating). degraded:true means the judge service was unreachable and " +
	"ranking ran on deterministic features only. Each result's url is the purchase page " +
	"(merchant URL when an outbound hop resolved, e.g. a deal-aggregator thread); " +
	"source_url keeps the originating listing when they differ. Read-only; latency is " +
	"tens of seconds — it scrapes real marketplace pages."

func registerProductSearch(srv *mcp.Server, d deps) {
	mcpserver.AddTool(srv, &mcp.Tool{
		Name:        toolProductSearch,
		Description: productSearchDesc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in productSearchInput) (*mcp.CallToolResult, error) {
		return handleProductSearch(ctx, d, in)
	})
}

// handleProductSearch is the tool body: funnel → extract → match through
// the retained searcher, then rank fusion and the public projection. The
// pipeline init error and empty-query input are tool errors, not panics.
func handleProductSearch(ctx context.Context, d deps, in productSearchInput) (*mcp.CallToolResult, error) {
	if d.initErr != nil {
		return d.unavailable(), nil
	}
	if in.Query == "" {
		return errResult("query is required"), nil
	}
	limit := clampMaxResults(in.MaxResults)
	out, err := d.searcher.SearchDetailed(ctx, in.Query, in.Criteria, limit)
	if err != nil {
		logToolError(toolProductSearch, err)
		return errResult(err.Error()), nil
	}

	ranked := rank.Rank(out.Candidates, out.Questions, out.Degraded, d.weights, d.passMin)
	resp := searchOutput{
		RequestID:     out.RequestID,
		Results:       make([]productResult, 0, min(limit, len(ranked))),
		Sources:       out.Sources,
		Degraded:      out.Degraded,
		DegradeReason: out.DegradeReason,
	}
	for i, r := range ranked {
		if i >= limit {
			break
		}
		resp.Results = append(resp.Results, project(r, d.trust))
	}
	resp.Brief = composeBrief(in.Query, out.Plan, ranked, out.Sources, d.trust)
	if d.grouper != nil {
		assigns := d.grouper.Assign(ctx, productsOf(ranked[:len(resp.Results)]))
		applyAssignments(resp.Results, assigns)
		resp.Groups = buildAssignedGroups(resp.Results)
	} else {
		resp.Groups = buildGroups(resp.Results)
	}
	if in.Group {
		resp.Products = collapseResults(resp.Results)
		// An empty non-nil slice keeps "results":[] out of the response —
		// the collapsed rows live in products[], and a null field would
		// read as a shape violation to clients.
		resp.Results = []productResult{}
	}
	return jsonResult(resp)
}

// productsOf maps ranked results to their extract.Products — the
// assigner's input stays index-aligned with resp.Results.
func productsOf(rs []rank.Result) []extract.Product {
	out := make([]extract.Product, len(rs))
	for i := range rs {
		out[i] = rs[i].Judged.Product
	}
	return out
}

// applyAssignments writes the durable group identity onto each result.
func applyAssignments(results []productResult, assigns []group.Assignment) {
	for i := range results {
		if i < len(assigns) && assigns[i].GroupID > 0 {
			results[i].GroupID = assigns[i].GroupID
		}
	}
}

// buildAssignedGroups clusters results by their persisted group id —
// the embedding-tier counterpart of buildGroups. A group surfaces a
// "key" when any member carried an exact identifier (the claim it was
// resolved under), and match="exact" marks that provenance; pure
// vector-matched groups report match="embedding" and no key.
//
// Results left unassigned — store outage or a claim that failed — still
// fall back to ephemeral exact-key grouping, so a registry problem can
// never remove offers from the comparison the pre-registry buildGroups
// would have produced.
func buildAssignedGroups(results []productResult) []productGroup {
	byID := make(map[int64][]productResult)
	var order []int64
	var orphan []productResult
	for _, r := range results {
		if r.GroupID == 0 {
			orphan = append(orphan, r)
			continue
		}
		if _, seen := byID[r.GroupID]; !seen {
			order = append(order, r.GroupID)
		}
		byID[r.GroupID] = append(byID[r.GroupID], r)
	}
	var groups []productGroup
	for _, id := range order {
		members := byID[id]
		stores := make(map[string]struct{}, len(members))
		g := productGroup{ID: id}
		for _, m := range members {
			stores[m.Source] = struct{}{}
			if g.Key == "" {
				g.Key = m.GroupKey
			}
			if m.GroupKey != "" {
				g.Match = "exact"
			}
			g.Offers = append(g.Offers, groupOffer{
				URL:          m.URL,
				Source:       m.Source,
				PriceMinor:   m.PriceMinor,
				Currency:     m.Currency,
				Availability: m.Availability,
				Passed:       m.Passed,
			})
		}
		if len(stores) < 2 {
			continue
		}
		if g.Match == "" {
			g.Match = "embedding"
		}
		g.BestOffer = bestOffer(g.Offers)
		groups = append(groups, g)
	}
	return append(groups, buildGroups(orphan)...)
}

// project maps one ranked candidate to the egress-safe result shape.
// PublicProduct is the only product data that may leave the box; the
// purchase URL and adapter name ride alongside it. URL carries the
// merchant page when an outbound hop resolved (matching what a user
// wants to open); the aggregator thread survives in SourceURL.
func project(r rank.Result, tp *trust.Provider) productResult {
	jc := r.Judged
	url, source := jc.Product.BuyURL, ""
	if url == "" {
		url = jc.URL
	} else if url != jc.URL {
		source = jc.URL
	}
	return productResult{
		URL:             url,
		SourceURL:       source,
		Adapter:         jc.Source,
		BuyURL:          jc.Product.BuyURL,
		Trust:           string(tp.ClassifyMerchant(jc.Product.BuyURL, jc.URL)),
		PublicProduct:   jc.ProductPublic(),
		Score:           r.Score,
		Confidence:      r.Confidence,
		Passed:          jc.Passed,
		MatchedCriteria: r.MatchedCriteria,
		DealSignals:     r.DealSignals,
		ExcludedReason:  string(r.ExcludedReason),
		ExcludedDetail:  r.ExcludedDetail,
		UnjudgedReason:  string(r.UnjudgedReason),
		GroupKey:        jc.Product.GroupKey(),
	}
}

// buildGroups clusters results sharing an exact-identifier GroupKey
// (issue #98) and emits the clusters spanning ≥2 distinct stores —
// same-store duplicates are still tagged per-result via group_key but
// don't make a comparison row. best_offer is the cheapest offer still
// passing the caller's criteria; mixed-currency clusters get none.
func buildGroups(results []productResult) []productGroup {
	byKey := make(map[string][]productResult)
	var order []string
	for _, r := range results {
		if r.GroupKey == "" {
			continue
		}
		if _, seen := byKey[r.GroupKey]; !seen {
			order = append(order, r.GroupKey)
		}
		byKey[r.GroupKey] = append(byKey[r.GroupKey], r)
	}
	var groups []productGroup
	for _, key := range order {
		members := byKey[key]
		stores := make(map[string]struct{}, len(members))
		g := productGroup{Key: key, Match: "exact"}
		for _, m := range members {
			stores[m.Source] = struct{}{}
			g.Offers = append(g.Offers, groupOffer{
				URL:          m.URL,
				Source:       m.Source,
				PriceMinor:   m.PriceMinor,
				Currency:     m.Currency,
				Availability: m.Availability,
				Passed:       m.Passed,
			})
		}
		if len(stores) < 2 {
			continue
		}
		g.BestOffer = bestOffer(g.Offers)
		groups = append(groups, g)
	}
	return groups
}

// collapseResults merges results into the group:true view — one row per
// product identity. Clustering prefers the durable group id; results the
// registry left unassigned fall back to the ephemeral exact key, and a
// result carrying neither forms a singleton row. The row's embedded
// productResult is the member behind best_offer when one exists (its
// price fields are what a buyer would act on), else the highest-ranked
// member. Row order follows first member appearance — the same rank
// ordering results[] would have had.
func collapseResults(results []productResult) []collapsedProduct {
	byCluster := make(map[string][]int, len(results))
	order := make([]string, 0, len(results))
	for i, r := range results {
		var k string
		switch {
		case r.GroupID > 0:
			k = "id:" + strconv.FormatInt(r.GroupID, 10)
		case r.GroupKey != "":
			k = "key:" + r.GroupKey
		default:
			k = "one:" + strconv.Itoa(i) // singleton — unidentifiable result
		}
		if _, seen := byCluster[k]; !seen {
			order = append(order, k)
		}
		byCluster[k] = append(byCluster[k], i)
	}
	out := make([]collapsedProduct, 0, len(order))
	for _, k := range order {
		members := byCluster[k]
		rep := members[0]
		offers := make([]groupOffer, 0, len(members))
		var stores []string
		seenStore := make(map[string]bool, len(members))
		for _, mi := range members {
			m := results[mi]
			offers = append(offers, groupOffer{
				URL:          m.URL,
				Source:       m.Source,
				PriceMinor:   m.PriceMinor,
				Currency:     m.Currency,
				Availability: m.Availability,
				Passed:       m.Passed,
			})
			if !seenStore[m.Source] {
				seenStore[m.Source] = true
				stores = append(stores, m.Source)
			}
		}
		best := bestOffer(offers)
		if best != nil {
			for _, mi := range members {
				if results[mi].URL == best.URL {
					rep = mi // the best offer's product is the row's face
					break
				}
			}
		}
		out = append(out, collapsedProduct{
			productResult: results[rep],
			Stores:        stores,
			Offers:        offers,
			BestOffer:     best,
		})
	}
	return out
}

// bestOffer picks the cheapest still-passing offer, requiring one shared
// currency — comparing raw minor units across currencies would be a lie.
// A priced, passing offer always beats an unpriced or excluded one; all
// else equal, the first (highest-ranked) offer wins.
func bestOffer(offers []groupOffer) *groupOffer {
	currency := ""
	for _, o := range offers {
		if o.Currency == "" {
			continue
		}
		if currency == "" {
			currency = o.Currency
		} else if o.Currency != currency {
			return nil
		}
	}
	var best *groupOffer
	for i := range offers {
		o := &offers[i]
		if !o.Passed || o.PriceMinor == nil {
			continue
		}
		if best == nil || *o.PriceMinor < *best.PriceMinor {
			best = o
		}
	}
	return best
}
