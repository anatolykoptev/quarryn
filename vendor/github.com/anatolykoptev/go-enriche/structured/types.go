package structured

import "github.com/astappiev/microdata"

// Data wraps parsed microdata with typed accessors.
type Data struct {
	raw *microdata.Microdata
}

// Place represents a schema.org Place/LocalBusiness.
type Place struct {
	Name    *string
	Type    *string
	Address *string
	Phone   *string
	Website *string
	Hours   *string
	Price   *string
}

// Article represents a schema.org Article/NewsArticle.
type Article struct {
	Headline      *string
	Author        *string
	Description   *string
	DatePublished *string
	Image         *string
}

// Event represents a schema.org Event.
type Event struct {
	Name      *string
	StartDate *string
	EndDate   *string
	Location  *string
	Price     *string
}

// Organization represents a schema.org Organization.
//
// LegalName and HasLegalID are corroborant signals for the legal-address
// PROVENANCE arm (see extract.applyOrgFacts). An Organization itemtype CAN be the
// registered legal entity, but a bare Organization block whose streetAddress is
// in fact the venue's visiting address (no separate Place block, no legal
// identifiers) must NOT have that address demoted out of the map slot. These
// fields let the extract layer require a corroborating legal signal before
// treating the Org streetAddress as legal-by-provenance.
type Organization struct {
	Name    *string
	URL     *string
	Phone   *string
	Address *string
	Hours   *string

	// LegalName is the schema.org legalName property when present — its presence
	// is itself a strong signal the Org block describes a registered legal entity.
	LegalName *string
	// HasLegalID reports whether a Russian legal-entity identifier
	// (ИНН/ОГРН/ОГРНИП/КПП) or its schema.org analogue (taxID/vatID) appears
	// anywhere in the Organization item -- as a property key, a property value, or
	// inside any nested item. Presence corroborates that the Org address is the
	// registered/legal seat.
	HasLegalID bool
}
