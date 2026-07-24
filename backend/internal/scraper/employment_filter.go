package scraper

import "regexp"

// internshipTitleRe matches internship/co-op/apprenticeship phrasing in a
// job title. Word-boundaries keep it from matching inside unrelated words
// like "Internal Tools Engineer" or "Cooperative Robotics".
//
// Neither Amazon's nor Google's public search surface exposes a reliable
// employment-type facet we can filter on server-side: Amazon's
// `job_schedule_type` field comes back "full-time" even for internships,
// and its `is_intern`/`university_job` fields are always null in the
// search API; Google's careers search has no equivalent facet at all. So
// this is a best-effort, title-only pre-filter to cut volume/cost early
// (fewer Gemini calls downstream) - n8n's "Filter Jobs" node re-checks the
// same pattern and is the authoritative filter.
var internshipTitleRe = regexp.MustCompile(`(?i)\b(intern(s|ship)?|co-?op|apprentice(ship)?)\b`)

// FilterOutInternships drops postings that look like internships/co-ops/
// apprenticeships based on their title, keeping fresher-friendly full-time
// and contract roles (including ones with little or no experience
// required) in the result set.
func FilterOutInternships(jobs []JobPosting) []JobPosting {
	filtered := make([]JobPosting, 0, len(jobs))
	for _, job := range jobs {
		if internshipTitleRe.MatchString(job.Title) {
			continue
		}
		filtered = append(filtered, job)
	}
	return filtered
}
