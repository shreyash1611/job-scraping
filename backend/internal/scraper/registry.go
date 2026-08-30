package scraper

import "sort"

// Groups split the registry into batches that can be scraped independently.
//
// This exists so the n8n side can run three separate workflows on staggered
// crons instead of one long one, while every company still lives in exactly
// one place here - adding a company means picking its group, not editing a
// list inside a workflow JSON.
const (
	// GroupCore is the original set: the four bespoke scrapers plus the
	// first Workday and Greenhouse tenants that were added alongside them.
	GroupCore = 1

	// GroupEnterprise is the large-cap hardware, enterprise and fintech
	// names. These are the slow ones - Workday tenants that paginate and
	// then fetch a detail page per job.
	GroupEnterprise = 2

	// GroupProduct is the SaaS and product companies on Greenhouse and
	// Ashby. It holds the most companies by count but is the cheapest to
	// run, because those boards return everything in a single request.
	GroupProduct = 3

	// GroupIndia is the India-heavy set: domestic product companies plus
	// the banks and funds that hire engineers here. It is a separate n8n
	// workflow on purpose, even when a company happens to sit on Greenhouse
	// or Lever like the product group.
	GroupIndia = 4
)

// Registration binds a route slug to the scraper that serves it.
//
// The registry exists so that adding a company is a one-line table entry
// instead of an edit in three places. main.go builds its per-company routes,
// its fan-out route and its group filtering from this list, so nothing has to
// be kept in sync by hand.
type Registration struct {
	Slug  string
	Group int
	New   func() Scraper
}

// Registry returns every company this backend can scrape, ordered by slug.
func Registry() []Registration {
	var all []Registration
	all = append(all, coreRegistrations()...)
	all = append(all, workdayRegistrations()...)
	all = append(all, greenhouseRegistrations()...)
	all = append(all, ashbyRegistrations()...)
	all = append(all, oracleRegistrations()...)
	all = append(all, talentAPIRegistrations()...)
	all = append(all, smartRecruitersRegistrations()...)
	all = append(all, leverRegistrations()...)
	all = append(all, deshawRegistrations()...)

	sort.Slice(all, func(i, j int) bool { return all[i].Slug < all[j].Slug })
	return all
}

// RegistryForGroup returns the companies in one group, or everything when
// group is zero.
func RegistryForGroup(group int) []Registration {
	all := Registry()
	if group == 0 {
		return all
	}

	filtered := make([]Registration, 0, len(all))
	for _, reg := range all {
		if reg.Group == group {
			filtered = append(filtered, reg)
		}
	}
	return filtered
}

// coreRegistrations covers the four companies with a bespoke scraper each,
// because none of them run on a recognisable ATS.
func coreRegistrations() []Registration {
	return []Registration{
		{Slug: "google", Group: GroupCore, New: func() Scraper { return NewGoogleScraper() }},
		{Slug: "amazon", Group: GroupCore, New: func() Scraper { return NewAmazonScraper() }},
		{Slug: "microsoft", Group: GroupCore, New: func() Scraper { return NewMicrosoftScraper() }},
		{Slug: "apple", Group: GroupCore, New: func() Scraper { return NewAppleScraper() }},
	}
}
