# Job scraper backend

Lightweight Go HTTP server that scrapes Google, Amazon, and Microsoft careers
pages for the n8n job-scraping workflow. Plain HTTP requests against each
site's own internal data source - no headless browser.

## Run

```bash
cd backend
go run .
```

Server listens on `:8080`.

## Endpoints

- `GET /health` - liveness check
- `GET /scrape/google?roles=<role>&roles=<role>&locations=<loc>&locations=<loc>`
- `GET /scrape/amazon?roles=...&locations=...`
- `GET /scrape/microsoft?roles=...&locations=...`

`roles` is required (at least one). `locations` is optional - when provided,
results are filtered to jobs whose location contains (case-insensitively) at
least one of the given strings. Include every spelling/alias you care about
(e.g. both `Bangalore` and `Bengaluru`) since matching is plain substring,
not a lookup table.

All three endpoints return a JSON array of:

```json
{
  "company": "Google",
  "title": "Software Engineer, Site Reliability Engineering",
  "url": "https://...",
  "location": "Bengaluru, Karnataka, India",
  "description": "...",
  "postedDate": "2026-07-20"
}
```

On failure, they return HTTP 502 with `{"error": "..."}`.

## Example

```bash
curl "http://localhost:8080/scrape/amazon?roles=Software+Engineer&locations=India&locations=Remote"
```

## Status per site

- **Amazon** - works. Uses `amazon.jobs/en/search.json`; adds `country=IND`
  server-side when "India" is requested to keep result volume sane before
  our own location filter runs.
- **Google** - works. Google Careers has no REST API; job data is embedded
  in the search results page as an `AF_initDataCallback` JS blob, which we
  regex-extract and parse as JSON. Field positions were reverse-engineered
  from a live response and could shift if Google changes the page.
- **Microsoft** - not implemented and will be WIP by me. Their careers site now runs on
  Eightfold.ai and gates its job search API behind a JS-minted auth token
  (confirmed via direct testing: every plain HTTP attempt gets a 403 "Not
  authorized for PCSX", with or without cookies/referer). No server-rendered
  job data exists to fall back to either. Calling `/scrape/microsoft` returns
  a descriptive error rather than fake/empty results - this is caught by the
  n8n workflow's error handling and logged to the Errors sheet rather than
  breaking the run. A headless browser (e.g. chromedp) is the fallback if
  Microsoft coverage becomes worth the extra weight. If you have any experience on scraping eightfold sites, please reach out to me on my LinkedIn.
