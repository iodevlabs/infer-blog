package main

import (
	"strings"
	"testing"
)

func TestProductScopedNavigation(t *testing.T) {
	p := product{Navigation: []group{{Title: "Get Started", Pages: []page{{Slug: "overview", Title: "Overview"}, {Slug: "workloads", Title: "Workloads"}}}}}
	landing := nav("cdeck", p, "", 1)
	if !strings.Contains(landing, `href="overview/"`) || strings.Contains(landing, `href="../overview/"`) {
		t.Fatalf("landing navigation escapes product: %s", landing)
	}
	article := nav("cdeck", p, "overview", 2)
	if !strings.Contains(article, `href="../workloads/"`) || !strings.Contains(article, `aria-current="page"`) {
		t.Fatalf("article navigation invalid: %s", article)
	}
}
func TestOriginValidation(t *testing.T) {
	for _, good := range []string{"https://docs.cdeck.infer.build", "https://docs.opendp.inferorigins.nl/"} {
		if !validOrigin(good) {
			t.Errorf("rejected origin %s", good)
		}
	}
	for _, bad := range []string{"http://docs.example.com", "https://example.com/private", "javascript:alert(1)"} {
		if validOrigin(bad) {
			t.Errorf("accepted invalid origin %s", bad)
		}
	}
}

func TestDocsProductTabs(t *testing.T) {
	m := manifest{Products: map[string]product{"cdeck": {Title: "CDeck"}, "opendp": {Title: "OpenDP"}}}
	got := productTabs(m, "../../")
	for _, want := range []string{`href="../../cdeck/"`, `href="../../opendp/"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}

func TestProductOriginValidation(t *testing.T) {
	for _, tc := range []struct {
		origin  string
		product string
		valid   bool
	}{
		{"https://docs.cdeck.inferorigins.com", "cdeck", true},
		{"https://docs.inferorigins.com/cdeck", "cdeck", true},
		{"https://docs.inferorigins.com/opendp/", "opendp", true},
		{"https://docs.inferorigins.com/opendp", "cdeck", false},
		{"https://docs.inferorigins.com/cdeck/extra", "cdeck", false},
		{"https://docs.inferorigins.com/cdeck?draft=1", "cdeck", false},
		{"http://localhost:8088/cdeck", "cdeck", false},
	} {
		if got := validProductOrigin(tc.origin, tc.product); got != tc.valid {
			t.Errorf("validProductOrigin(%q, %q) = %t; want %t", tc.origin, tc.product, got, tc.valid)
		}
	}
}
