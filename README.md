## Status per site

- **Amazon** - works. Uses `amazon.jobs/en/search.json`; adds `country=IND`
  server-side when "India" is requested to keep result volume sane before
  our own location filter runs.
- **Google** - works. Google Careers has no REST API; job data is embedded
  in the search results page as an `AF_initDataCallback` JS blob, which we
  regex-extract and parse as JSON. Field positions were reverse-engineered
  from a live response and could shift if Google changes the page.
- **Microsoft** - works, no auth needed. Their careers site runs on
  Eightfold.ai, and Eightfold's standard search API (`/api/apply/v2/jobs`) is
  a dead end: it returns 403 "Not authorized for PCSX" on every plain HTTP
  attempt, and still does even when replaying a real browser's live
  `x-csrf-token` and cookies - so the 403 isn't a CSRF/session problem and
  minting a token doesn't help. The way in is `/api/pcsx/search`, which the
  site's own `robots.txt` explicitly allows and which needs no token, cookies
  or CSRF header. Its result count matches the number the real UI shows for
  the same query. Descriptions aren't in that response and no JSON endpoint
  exists for a single job, so they come from the `JobPosting` JSON-LD block
  each job page server-renders. Page size is fixed at 10 server-side.
- **Apple** - Added HTML scraping and flatten the data with LLM node on N8N to match our sheet  pattern in JSON format
- **Nike, KLA, Cisco, Adobe, Sprinklr, Rakuten** - all work, all Workday, all
  one shared scraper (`workday.go`) configured per tenant. Cisco and Adobe
  look custom (`careers.cisco.com` / `careers.adobe.com` are Phenom
  front-ends) but their apply links point back at their Workday tenants, so
  they need no special handling. Workday caps `limit` at 20 - asking for 21
  is an HTTP 400 - and its search response carries no description and only a
  relative date ("Posted Today"), so both come from a per-job detail call.
  Rakuten splits its openings across five career sites on one tenant and only
  `RakutenSymphony` had any India presence, which is the one wired up.
- **Databricks, Okta** - both work, both Greenhouse, one shared scraper
  (`greenhouse.go`). Cheapest source here by far: a single request with
  `?content=true` returns the whole board with descriptions inline, so there
  are no detail fetches and no pagination. There's no server-side search, so
  role matching happens locally.
- **Uber** - not implemented. `jobs.uber.com` renders results client-side with
  no server-rendered job data, the documented `/api/loadSearchJobsResults`
  endpoint now 404s, and their sitemap contains only driver-marketing pages
  with no job details. Needs a headless browser. `/scrape/uber` returns a
  descriptive error so n8n logs it to the Errors sheet instead of masking it
  as zero results.

## N8N Workflow

 -**Please REACH OUT TO ME ON LINKEDIN FOR THE N8N JSON**
