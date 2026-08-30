package scraper

import (
	"strings"
	"unicode"
)

// FilterByLocation keeps only the jobs whose Location names at least one of
// the requested places. Aliases for one city ("Bangalore"/"Bengaluru") are the
// caller's job - it should list every spelling it cares about.
//
// Matching is per word rather than by raw substring. Substring matching looked
// correct until the number of sources grew, at which point it was quietly
// waving through a lot of nonsense: a request for "India" matched Indiana and
// Indianapolis, and the country code "IN" matched Berlin, Dublin, Washington
// and "Pampanga Province" - all of which reached the sheet labelled as India
// roles.
func FilterByLocation(jobs []JobPosting, locations []string) []JobPosting {
	if len(locations) == 0 {
		return jobs
	}

	var (
		phrases         [][]string
		remoteRequested bool
	)
	for _, loc := range locations {
		loc = strings.TrimSpace(strings.ToLower(loc))
		if loc == "" {
			continue
		}
		// "Remote" is a work arrangement, not a place, and is handled
		// separately - see locationMatches.
		if loc == "remote" {
			remoteRequested = true
			continue
		}
		if words := locationWords(loc); len(words) > 0 {
			phrases = append(phrases, words)
		}
	}

	filtered := make([]JobPosting, 0, len(jobs))
	for _, job := range jobs {
		if locationMatches(job.Location, phrases, remoteRequested) {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

func locationMatches(location string, phrases [][]string, remoteRequested bool) bool {
	words := locationWords(location)
	if len(words) == 0 {
		return false
	}

	for _, phrase := range phrases {
		if isIndiaCountryCode(phrase) {
			if looksLikeIndiaCountryCode(words) {
				return true
			}
			continue
		}
		if containsPhrase(words, phrase) {
			return true
		}
	}

	if !remoteRequested {
		return false
	}

	// Bare "Remote" is open enough to surface. "Remote - India" already
	// matched a phrase above. "US - Remote" / "PL-Poland-Remote" name
	// somewhere else and stay dropped.
	sawRemote, otherWords := false, 0
	for _, word := range words {
		if word == "remote" {
			sawRemote = true
			continue
		}
		otherWords++
	}
	return sawRemote && otherWords == 0
}

// isIndiaCountryCode is the ISO / Greenhouse abbreviation, not the word
// "India". Substring matching "IN" used to keep Berlin and Indiana; this
// only fires for the exact token, and looksLikeIndiaCountryCode still
// rejects US-state uses of it.
func isIndiaCountryCode(phrase []string) bool {
	return len(phrase) == 1 && (phrase[0] == "in" || phrase[0] == "ind")
}

func looksLikeIndiaCountryCode(words []string) bool {
	hasIND := false
	inAtEdge := false
	for i, word := range words {
		if word == "ind" {
			hasIND = true
		}
		if word == "in" && (i == 0 || i == len(words)-1) {
			// Two-letter "IN" is only a country code at the edge of the
			// token list (IN-Pune, Pune, IN). The English preposition in
			// "Hybrid in Santa Clara" sits in the middle and is not India.
			inAtEdge = true
		}
	}
	if !hasIND && !inAtEdge {
		return false
	}
	for _, word := range words {
		switch word {
		case "us", "usa", "united", "indiana", "indianapolis":
			return false
		}
	}
	return true
}

// locationWords splits a location into lowercase alphanumeric words so that
// every separator sites use - "US-WA-Bellevue", "Bangalore, IND",
// "India | Remote" - tokenizes the same way.
func locationWords(location string) []string {
	return strings.FieldsFunc(strings.ToLower(location), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// containsPhrase reports whether phrase appears as consecutive whole words in
// words, which is what makes "india" miss "Indiana" while still matching
// "Bengaluru, Karnataka, India".
func containsPhrase(words, phrase []string) bool {
	if len(phrase) == 0 || len(phrase) > len(words) {
		return false
	}
	for i := 0; i+len(phrase) <= len(words); i++ {
		matched := true
		for j, want := range phrase {
			if words[i+j] != want {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
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
