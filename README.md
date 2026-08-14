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
  Update- Found the request format for Microsoft to find data from EightFold in the network console.
- **Apple** - Added HTML scraping and flatten the data with LLM node on N8N to match our sheet  pattern in JSON format

## N8N Workflow

 -**Please REACH OUT TO ME ON LINKEDIN FOR THE N8N JSON**
