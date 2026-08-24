package scraper

import "testing"

func TestEveryRegistrationHasAGroup(t *testing.T) {
	counts := map[int]int{}
	seen := map[string]bool{}
	for _, reg := range Registry() {
		if reg.Slug == "" {
			t.Fatal("registration with empty slug")
		}
		if seen[reg.Slug] {
			t.Errorf("duplicate slug %q", reg.Slug)
		}
		seen[reg.Slug] = true
		if reg.Group < GroupCore || reg.Group > GroupProduct {
			t.Errorf("%s has group %d, want 1-3", reg.Slug, reg.Group)
		}
		counts[reg.Group]++
	}
	if counts[GroupCore] == 0 || counts[GroupEnterprise] == 0 || counts[GroupProduct] == 0 {
		t.Errorf("a group is empty: %+v", counts)
	}
	t.Logf("core=%d enterprise=%d product=%d", counts[GroupCore], counts[GroupEnterprise], counts[GroupProduct])
}
