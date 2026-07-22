package scraper

import "errors"

// MicrosoftScraper is a documented stub, not a bug.
//
// Investigated live (July 2026): careers.microsoft.com now redirects to a
// "v2" site that migrated its job search backend to the Eightfold.ai
// platform (found a dedicated tenant at microsoft.eightfold.ai, confirmed
// by matching "ef-*" JS bundle naming). The old iCIMS "/widgets" POST
// endpoint referenced in older writeups no longer exists at all - it 404s.
//
// The Eightfold search API (`/api/apply/v2/jobs?domain=microsoft.com...`)
// is reachable but returns HTTP 403 "Not authorized for PCSX" for every
// plain HTTP request we tried: with/without cookies from a real page visit,
// with a matching Referer header, on both apply.careers.microsoft.com and
// microsoft.eightfold.ai directly. The site itself is a pure client-side
// SPA shell with no server-rendered job data anywhere we could find (no
// sitemap, no SSR'd job detail HTML) - this lines up with independent
// research (see plan notes) that Microsoft's careers frontend now mints a
// bearer token via JS before it's allowed to call its own search API,
// which a plain net/http client can't replicate without either reversing
// that token-minting logic or executing real JS.
//
// Returning a clear, descriptive error here (rather than silently empty
// results) means n8n's "Continue On Fail" error handling on this endpoint
// will correctly log this to the Errors sheet instead of masking it as
// "zero jobs found". If Microsoft coverage matters enough to invest in,
// the fallback is a headless browser (e.g. chromedp) just for this one
// site - everything else in this backend (contract, location filter,
// server routing) stays the same either way.
type MicrosoftScraper struct{}

func NewMicrosoftScraper() *MicrosoftScraper {
	return &MicrosoftScraper{}
}

func (m *MicrosoftScraper) Search(params SearchParams) ([]JobPosting, error) {
	return nil, errors.New(
		"microsoft careers search is gated behind a JS-minted auth token on their Eightfold-based site " +
			"(confirmed 403 'Not authorized for PCSX' on all plain HTTP attempts); " +
			"not scrapable without a headless browser - see comments in microsoft.go",
	)
}
