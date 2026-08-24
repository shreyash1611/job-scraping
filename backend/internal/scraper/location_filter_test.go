package scraper

import "testing"

// requestedLocations mirrors what the n8n Search Config node sends.
var requestedLocations = []string{
	"Remote", "India", "Bengaluru", "Bangalore", "Hyderabad", "Gurugram", "Gurgaon", "IN",
}

func TestFilterByLocation(t *testing.T) {
	cases := []struct {
		location string
		keep     bool
		why      string
	}{
		// Real India locations, in the various shapes the sources emit.
		{"Bengaluru, Karnataka, India", true, "plain India location"},
		{"India, Bengaluru", true, "NVIDIA puts the country first"},
		{"Bangalore, IND", true, "Zscaler abbreviates the country"},
		{"Hyderabad, Telangana, India", true, "Uber and Google shape"},
		{"Hyderabad, India", true, "AMD shape"},
		{"Gurgaon, India", true, ""},
		{"Bangalore, India; Chennai, India", true, "multi-location posting"},
		{"Remote", true, "unqualified remote is open enough to surface"},
		{"Remote - India", true, "remote but pinned here"},
		{"IN-Pune", true, "Snowflake-style India ISO prefix"},
		{"IN Remote India", true, "Confluent-style India remote"},
		{"US-IN-Remote", false, "Indiana, not India"},

		// Everything below reached the sheet as an India role before the
		// word-boundary fix.
		{"US-WA-Bellevue; US-CA-Menlo Park", false, "no India anywhere"},
		{"DE-Berlin-Trion Building", false, `"IN" matched Berl-in-`},
		{"Dublin, Ireland", false, `"IN" matched Dubl-in-`},
		{"Washington, DC", false, `"IN" matched Wash-in-gton`},
		{"Mabalacat City, Pampanga Province", false, `"IN" matched Prov-in-ce`},
		{"Indianapolis, Indiana, US", false, `"India" matched Indiana`},
		{"Remote - Indiana, USA", false, "Indiana again, via remote"},
		{"US - Remote", false, "remote, but not to a candidate here"},
		{"PL-Poland-Remote", false, "remote in Poland"},
		{"GB Remote United Kingdom", false, "remote in the UK"},
		{"Remote, United States", false, "remote in the US"},
		{"San Francisco", false, ""},
		{"", false, "no location at all"},
	}

	for _, tc := range cases {
		jobs := []JobPosting{{Company: "Test", URL: "u", Location: tc.location}}
		got := len(FilterByLocation(jobs, requestedLocations)) == 1
		if got != tc.keep {
			verb := "dropped"
			if got {
				verb = "kept"
			}
			t.Errorf("location %q was %s, want keep=%v (%s)", tc.location, verb, tc.keep, tc.why)
		}
	}
}

func TestFilterByLocationNoLocationsPassesEverything(t *testing.T) {
	jobs := []JobPosting{{URL: "a", Location: "Berlin"}, {URL: "b", Location: ""}}
	if got := FilterByLocation(jobs, nil); len(got) != 2 {
		t.Errorf("with no requested locations, got %d jobs, want all %d", len(got), len(jobs))
	}
}
