package scraper

import "errors"

// UberScraper is a documented stub, not a bug - same situation as
// MicrosoftScraper, investigated live (July 2026).
//
// Uber's old careers site (www.uber.com/.../careers/list) 301-redirects
// unconditionally to jobs.uber.com, which is a Next.js "App Router" SPA:
// the initial HTML response has no server-rendered job data anywhere (no
// __NEXT_DATA__ blob, no job titles/links in the raw HTML at all) - the
// results grid is fetched client-side by JS after hydration. Guessing at
// that internal API directly (/api/jobs, /api/search, /api/v1/jobs,
// /graphql, and the legacy /api/loadSearchJobsResults endpoint that still
// answers on www.uber.com) all either 403 or explicitly report a missing
// CSRF token, meaning it needs a real browser session/token-minting flow,
// not just a plain HTTP GET. Uber also isn't on a third-party ATS we could
// fall back to - boards.greenhouse.io/uber redirects away too, confirming
// they migrated off Greenhouse entirely.
//
// Returning a descriptive error here (rather than silently empty results)
// means n8n's "Continue On Fail" error handling on this endpoint will
// correctly log this to the Errors sheet instead of masking it as "zero
// jobs found". Same fallback as Microsoft if Uber coverage matters enough
// to invest in: a headless browser (e.g. chromedp) just for this one site.
type UberScraper struct{}

func NewUberScraper() *UberScraper {
	return &UberScraper{}
}

func (u *UberScraper) Search(params SearchParams) ([]JobPosting, error) {
	return nil, errors.New(
		"uber careers search (jobs.uber.com) renders its results client-side via JS after page load, " +
			"with no server-rendered job data and a CSRF/session-gated internal API; " +
			"not scrapable without a headless browser - see comments in uber.go",
	)
}
