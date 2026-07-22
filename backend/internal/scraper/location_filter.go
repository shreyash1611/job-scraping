package scraper

import "strings"

// FilterByLocation keeps only the jobs whose Location field contains (case
// insensitively) at least one of the requested location keywords as a
// substring. Aliases for the same city (e.g. "Bangalore"/"Bengaluru" or
// "Gurugram"/"Gurgaon") are handled by the caller including every spelling
// it cares about in locations - this function just does plain substring
// matching. If locations is empty, no filtering happens and all jobs pass
// through unchanged.
func FilterByLocation(jobs []JobPosting, locations []string) []JobPosting {
	if len(locations) == 0 {
		return jobs
	}

	needles := make([]string, len(locations))
	for i, loc := range locations {
		needles[i] = strings.ToLower(strings.TrimSpace(loc))
	}

	filtered := make([]JobPosting, 0, len(jobs))
	for _, job := range jobs {
		haystack := strings.ToLower(job.Location)
		for _, needle := range needles {
			if needle == "" {
				continue
			}
			if strings.Contains(haystack, needle) {
				filtered = append(filtered, job)
				break
			}
		}
	}
	return filtered
}

// DedupeByURL removes jobs with duplicate URLs, keeping the first occurrence.
func DedupeByURL(jobs []JobPosting) []JobPosting {
	seen := make(map[string]struct{}, len(jobs))
	deduped := make([]JobPosting, 0, len(jobs))
	for _, job := range jobs {
		if job.URL == "" {
			continue
		}
		if _, ok := seen[job.URL]; ok {
			continue
		}
		seen[job.URL] = struct{}{}
		deduped = append(deduped, job)
	}
	return deduped
}
