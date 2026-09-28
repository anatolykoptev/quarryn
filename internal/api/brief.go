package api

import (
	"github.com/anatolykoptev/quarryn/internal/match"
	"github.com/anatolykoptev/quarryn/internal/rank"
	pssources "github.com/anatolykoptev/quarryn/internal/sources"
	"github.com/anatolykoptev/quarryn/internal/trust"
)

// Code composes the brief, never the model (ADR-3's judge decides fit;
// this stage narrates the decision). Three groups mirror how candidates
// actually leave the pipeline: finalists passed everything, excluded hit
// a deterministic bound, rejected failed a subjective criterion, unjudged
// never reached the judge. An excluded or unjudged candidate carries its
// machine-readable reason verbatim — the brief adds no new verdicts.
type searchBrief struct {
	Query       string                   `json:"query"`
	Constraints match.Constraints        `json:"constraints"`
	Subjective  []string                 `json:"subjective,omitempty"`
	Finalists   []briefFinalist          `json:"finalists"`
	Excluded    []briefDropped           `json:"excluded,omitempty"`
	Rejected    []briefDropped           `json:"rejected,omitempty"`
	Unjudged    []briefDropped           `json:"unjudged,omitempty"`
	Coverage    []pssources.SourceStatus `json:"coverage,omitempty"`
}

type briefFinalist struct {
	Rank       int      `json:"rank"`
	Name       string   `json:"name"`
	URL        string   `json:"url"` // purchase page — merchant URL when resolved, else listing
	SourceURL  string   `json:"source_url,omitempty"`
	BuyURL     string   `json:"buy_url,omitempty"`
	Adapter    string   `json:"adapter"`
	OfferID    string   `json:"offer_id,omitempty"`
	Price      *float64 `json:"price,omitempty"`
	PriceMinor *int64   `json:"price_minor,omitempty"`
	Currency   string   `json:"currency,omitempty"`
	Score      float64  `json:"score"`
	Trust      string   `json:"trust,omitempty"`
}

// briefDropped is one appendix line: what left the funnel and why. Code is
// the stable match.ReasonCode value; Detail names the offending term.
type briefDropped struct {
	Name           string   `json:"name"`
	URL            string   `json:"url"`
	Adapter        string   `json:"adapter"`
	Code           string   `json:"code"`
	Detail         string   `json:"detail,omitempty"`
	FailedCriteria []string `json:"failed_criteria,omitempty"`
}

// briefFinalistsCap mirrors the reader's working set — a brief longer than
// the top few winners stops being a brief.
const briefFinalistsCap = 5

func composeBrief(query string, plan match.Plan, ranked []rank.Result, sources []pssources.SourceStatus, tp *trust.Provider) *searchBrief {
	b := &searchBrief{
		Query:       query,
		Constraints: plan.Constraints,
		Coverage:    sources,
		Finalists:   []briefFinalist{},
	}
	for _, q := range plan.Questions {
		b.Subjective = append(b.Subjective, q.Criterion)
	}
	for _, r := range ranked {
		jc := r.Judged
		pub := jc.ProductPublic()
		switch {
		case jc.Passed:
			if len(b.Finalists) >= briefFinalistsCap {
				continue
			}
			url, source := jc.Product.BuyURL, ""
			if url == "" {
				url = jc.URL
			} else if url != jc.URL {
				source = jc.URL
			}
			b.Finalists = append(b.Finalists, briefFinalist{
				Rank:       len(b.Finalists) + 1,
				Name:       pub.Name,
				URL:        url,
				SourceURL:  source,
				BuyURL:     jc.Product.BuyURL,
				Adapter:    jc.Source,
				OfferID:    pub.OfferID,
				Price:      pub.Price,
				PriceMinor: pub.PriceMinor,
				Currency:   pub.Currency,
				Score:      r.Score,
				Trust:      string(tp.ClassifyMerchant(jc.Product.BuyURL, jc.URL)),
			})
		case jc.Excluded:
			b.Excluded = append(b.Excluded, briefDropped{
				Name: pub.Name, URL: jc.URL, Adapter: jc.Source,
				Code: string(jc.ExcludeReason), Detail: jc.ExcludeDetail,
			})
		case jc.UnjudgedReason != "":
			b.Unjudged = append(b.Unjudged, briefDropped{
				Name: pub.Name, URL: jc.URL, Adapter: jc.Source,
				Code: string(jc.UnjudgedReason),
			})
		default:
			b.Rejected = append(b.Rejected, briefDropped{
				Name: pub.Name, URL: jc.URL, Adapter: jc.Source,
				Code: "criteria_unmet", FailedCriteria: failedCriteria(r),
			})
		}
	}
	return b
}

// failedCriteria lists the subjective criteria the judge scored below the
// pass threshold — the judge-facing counterpart of an exclusion detail.
func failedCriteria(r rank.Result) []string {
	var out []string
	for _, v := range r.MatchedCriteria {
		if !v.Pass {
			out = append(out, v.Criterion)
		}
	}
	return out
}
