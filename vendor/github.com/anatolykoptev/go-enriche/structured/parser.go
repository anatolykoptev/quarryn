package structured

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/astappiev/microdata"
	"golang.org/x/net/html"
)

// Schema.org type names used for lookup.
const (
	typeArticle      = "Article"
	typeNewsArticle  = "NewsArticle"
	typeBlogPosting  = "BlogPosting"
	typeWebPage      = "WebPage"
	typeEvent        = "Event"
	typeOrganization = "Organization"
	typeCorporation  = "Corporation"
	typeGovOrg       = "GovernmentOrganization"
)

// placeTypes lists schema.org Place subtypes to search for.
var placeTypes = []string{
	"LocalBusiness", "Restaurant", "CafeOrCoffeeShop", "BarOrPub",
	"Hotel", "Store", "Place", "TouristAttraction", "Museum",
	"SportsActivityLocation", "EntertainmentBusiness",
}

// Parse extracts JSON-LD and Microdata from HTML.
func Parse(r io.Reader, contentType, pageURL string) (*Data, error) {
	md, err := microdata.ParseHTML(r, contentType, pageURL)
	if err != nil {
		return nil, fmt.Errorf("microdata parse: %w", err)
	}
	return &Data{raw: md}, nil
}

// ParseNode extracts JSON-LD and Microdata from an ALREADY-PARSED HTML node
// tree, mirroring Parse but skipping its reader/html.Parse step. For a
// caller that already holds a parsed tree — e.g. extract.schemaPlaceCandidates,
// which reuses a *goquery.Document's own root node — this avoids a redundant
// serialize-back-to-string-then-reparse round trip over the same page.
// astappiev/microdata's own ParseHTML does exactly this internally
// (html.Parse then ParseNode), so this is the same walk, just fed a tree the
// caller already has.
func ParseNode(root *html.Node, pageURL string) (*Data, error) {
	md, err := microdata.ParseNode(root, pageURL)
	if err != nil {
		return nil, fmt.Errorf("microdata parse node: %w", err)
	}
	return &Data{raw: md}, nil
}

// Raw returns the underlying microdata for advanced use.
func (d *Data) Raw() *microdata.Microdata { return d.raw }

// FirstPlace finds the first Place-like item.
func (d *Data) FirstPlace() *Place {
	for _, t := range placeTypes {
		if item := d.raw.GetFirstOfSchemaType(t); item != nil {
			return itemToPlace(item)
		}
	}
	return nil
}

// Places returns all Place entities found in structured data.
func (d *Data) Places() []*Place {
	var places []*Place
	for _, item := range d.raw.Items {
		for _, t := range placeTypes {
			if item.IsOfSchemaType(t) {
				places = append(places, itemToPlace(item))
				break
			}
		}
		if graph, ok := item.GetNested("@graph"); ok {
			for _, gitem := range graph.Items {
				for _, t := range placeTypes {
					if gitem.IsOfSchemaType(t) {
						places = append(places, itemToPlace(gitem))
						break
					}
				}
			}
		}
	}
	return places
}

// FirstArticle finds the first Article-like item.
func (d *Data) FirstArticle() *Article {
	for _, t := range []string{typeArticle, typeNewsArticle, typeBlogPosting, typeWebPage} {
		if item := d.raw.GetFirstOfSchemaType(t); item != nil {
			return itemToArticle(item)
		}
	}
	return nil
}

// FirstEvent finds the first Event item.
func (d *Data) FirstEvent() *Event {
	if item := d.raw.GetFirstOfSchemaType(typeEvent); item != nil {
		return itemToEvent(item)
	}
	return nil
}

// FirstOrganization finds the first Organization item.
func (d *Data) FirstOrganization() *Organization {
	for _, t := range []string{typeOrganization, typeCorporation, typeGovOrg} {
		if item := d.raw.GetFirstOfSchemaType(t); item != nil {
			return itemToOrganization(item)
		}
	}
	return nil
}

func itemToPlace(item *microdata.Item) *Place {
	p := &Place{
		Name:    propString(item, "name"),
		Type:    itemType(item),
		Phone:   propString(item, "telephone"),
		Website: propString(item, "url"),
		Hours:   extractHours(item),
	}
	p.Address = extractAddress(item)
	p.Price = extractPrice(item)
	return p
}

func itemToArticle(item *microdata.Item) *Article {
	return &Article{
		Headline:      propString(item, "headline", "name"),
		Author:        extractAuthor(item),
		Description:   propString(item, "description"),
		DatePublished: propString(item, "datePublished"),
		Image:         propString(item, "image"),
	}
}

func itemToEvent(item *microdata.Item) *Event {
	return &Event{
		Name:      propString(item, "name"),
		StartDate: propString(item, "startDate"),
		EndDate:   propString(item, "endDate"),
		Location:  extractEventLocation(item),
		Price:     extractPrice(item),
	}
}

func itemToOrganization(item *microdata.Item) *Organization {
	return &Organization{
		Name:       propString(item, "name"),
		URL:        propString(item, "url"),
		Phone:      propString(item, "telephone"),
		Address:    extractAddress(item),
		Hours:      extractHours(item),
		LegalName:  propString(item, "legalName"),
		HasLegalID: itemHasLegalID(item),
	}
}

// reLegalID matches a Russian legal-entity identifier (ИНН / ОГРН / ОГРНИП / КПП).
// These name a registered legal entity, never a place to visit, so their presence
// anywhere in an Organization item corroborates that the item's address is the
// registered/legal seat. The tokens are bound with a Cyrillic-safe boundary
// (start-of-string or a separator on each side) so an ordinary word merely
// embedding the letters does not spuriously match — the same boundary discipline
// extract.reLegalAddressMarker uses for company-form tokens.
var reLegalID = regexp.MustCompile(
	`(?i)(?:^|[\s,.;:«"(])(?:инн|огрнип|огрн|кпп)(?:[\s,.;:»")]|$)`,
)

// reLegalIDPropKey matches a schema.org property KEY that carries a legal-entity
// identifier: the Russian forms above plus the schema.org analogues taxID / vatID.
// A property key is an exact (case-insensitive) match — keys are machine tokens,
// not free text, so no boundary is needed.
var reLegalIDPropKey = regexp.MustCompile(`(?i)^(?:инн|огрнип|огрн|кпп|taxid|vatid)$`)

// itemHasLegalID reports whether a Russian legal-entity identifier
// (ИНН/ОГРН/ОГРНИП/КПП) or its schema.org analogue (taxID/vatID) appears anywhere
// in the item — as a property key, a string property value, or inside any nested
// item. It walks nested items recursively (e.g. a PostalAddress that prints an ИНН
// in its streetAddress, or a contactPoint carrying the registration IDs). Used to
// corroborate the legal-address PROVENANCE arm: a bare Organization block with no
// such identifier and no separate Place block must keep its (venue) address in the
// map slot rather than have it demoted to LegalAddress. See extract.applyOrgFacts.
func itemHasLegalID(item *microdata.Item) bool {
	if item == nil {
		return false
	}
	for key, values := range item.Properties {
		if reLegalIDPropKey.MatchString(strings.TrimSpace(key)) {
			return true
		}
		if valuesHaveLegalID(values) {
			return true
		}
	}
	return false
}

// valuesHaveLegalID reports whether any value in a property's value list carries a
// legal-entity ID — recursing into nested items and matching the ID token in string
// values. Split out of itemHasLegalID to keep each function's nesting shallow.
func valuesHaveLegalID(values microdata.ValueList) bool {
	for _, v := range values {
		switch val := v.(type) {
		case *microdata.Item:
			if itemHasLegalID(val) {
				return true
			}
		case string:
			if reLegalID.MatchString(val) {
				return true
			}
		case fmt.Stringer:
			if reLegalID.MatchString(val.String()) {
				return true
			}
		}
	}
	return false
}

// propString returns the first string value for any of the given keys.
func propString(item *microdata.Item, keys ...string) *string {
	for _, key := range keys {
		val, ok := item.GetProperty(key)
		if !ok {
			continue
		}
		var s string
		switch v := val.(type) {
		case string:
			s = strings.TrimSpace(v)
		case fmt.Stringer:
			s = strings.TrimSpace(v.String())
		default:
			s = strings.TrimSpace(fmt.Sprint(v))
		}
		if s != "" {
			return &s
		}
	}
	return nil
}

// itemType returns the first schema.org type stripped of the prefix.
func itemType(item *microdata.Item) *string {
	if len(item.Types) == 0 {
		return nil
	}
	t := item.Types[0]
	t = strings.TrimPrefix(t, "https://schema.org/")
	t = strings.TrimPrefix(t, "http://schema.org/")
	return &t
}

// extractAddress builds an address string from nested PostalAddress or plain string.
func extractAddress(item *microdata.Item) *string {
	nested, ok := item.GetNestedItem("address")
	if ok {
		parts := make([]string, 0, addressPartCount)
		for _, key := range []string{"streetAddress", "addressLocality", "addressRegion", "postalCode"} {
			if s := propString(nested, key); s != nil {
				parts = append(parts, *s)
			}
		}
		if len(parts) > 0 {
			joined := strings.Join(parts, ", ")
			return &joined
		}
	}
	return propString(item, "address")
}

const addressPartCount = 4

// extractPrice gets price from offers or priceRange.
func extractPrice(item *microdata.Item) *string {
	if nested, ok := item.GetNestedItem("offers"); ok {
		if p := propString(nested, "price"); p != nil {
			return p
		}
	}
	return propString(item, "priceRange")
}

// extractHours builds an opening-hours string from schema.org data, preferring
// the structured openingHoursSpecification array (one item per day-range with
// dayOfWeek / opens / closes) and falling back to the flat openingHours
// property (which may itself be a single string or an array of "Mo-Fr 10:00-22:00"
// strings). Returns nil when neither form is present. The output is a single
// human-readable string ("Mo-Fr 10:00-22:00; Sa 11:00-20:00") the content
// layer can render or normalize downstream.
func extractHours(item *microdata.Item) *string {
	if s := hoursFromSpecification(item); s != nil {
		return s
	}
	return hoursFromOpeningHours(item)
}

// hoursFromSpecification reads the structured openingHoursSpecification array.
// Each nested OpeningHoursSpecification carries dayOfWeek (one or more) plus
// opens/closes times. Builds one "<days> <opens>-<closes>" clause per spec and
// joins them with "; ". Returns nil when no usable clause is produced.
func hoursFromSpecification(item *microdata.Item) *string {
	data, ok := item.GetNested("openingHoursSpecification")
	if !ok {
		return nil
	}
	var clauses []string
	for _, spec := range data.Items {
		clause := hoursClause(spec)
		if clause != "" {
			clauses = append(clauses, clause)
		}
	}
	if len(clauses) == 0 {
		return nil
	}
	joined := strings.Join(clauses, "; ")
	return &joined
}

// hoursClause renders one OpeningHoursSpecification item as "<days> <opens>-<closes>".
// dayOfWeek values are abbreviated to their last path segment (schema.org URLs
// like https://schema.org/Monday -> "Monday"). A clause with no day or no time
// pair is dropped (returns "").
func hoursClause(spec *microdata.Item) string {
	var days []string
	if props, ok := spec.GetProperties("dayOfWeek"); ok {
		for _, d := range props {
			if name := dayName(d); name != "" {
				days = append(days, name)
			}
		}
	}
	opens := propString(spec, "opens")
	closes := propString(spec, "closes")
	if opens == nil || closes == nil {
		return ""
	}
	timeRange := *opens + "-" + *closes
	if len(days) == 0 {
		return timeRange
	}
	return strings.Join(days, ",") + " " + timeRange
}

// dayName extracts a readable day name from a dayOfWeek value, which may be a
// plain string ("Monday") or a schema.org URL ("https://schema.org/Monday").
func dayName(v interface{}) string {
	s, ok := v.(string)
	if !ok {
		if st, isStringer := v.(fmt.Stringer); isStringer {
			s = st.String()
		} else {
			return ""
		}
	}
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// hoursFromOpeningHours reads the flat openingHours property, which may be a
// single string or an array of day-range strings ("Mo-Fr 10:00-22:00"). Joins
// multiple values with "; ". Returns nil when absent.
func hoursFromOpeningHours(item *microdata.Item) *string {
	props, ok := item.GetProperties("openingHours")
	if !ok {
		return nil
	}
	var parts []string
	for _, v := range props {
		s := ""
		switch val := v.(type) {
		case string:
			s = strings.TrimSpace(val)
		case fmt.Stringer:
			s = strings.TrimSpace(val.String())
		default:
			s = strings.TrimSpace(fmt.Sprint(val))
		}
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	joined := strings.Join(parts, "; ")
	return &joined
}

// extractAuthor gets author name from nested Person or plain string.
func extractAuthor(item *microdata.Item) *string {
	if nested, ok := item.GetNestedItem("author"); ok {
		return propString(nested, "name")
	}
	return propString(item, "author")
}

// extractEventLocation gets location name from nested Place or plain string.
func extractEventLocation(item *microdata.Item) *string {
	if nested, ok := item.GetNestedItem("location"); ok {
		return propString(nested, "name")
	}
	return propString(item, "location")
}
