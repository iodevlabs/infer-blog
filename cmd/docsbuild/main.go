// docsbuild assembles reviewed, repository-owned Markdown into public product docs.
// It intentionally has no dependency on the private Infer Origins portal.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"inferorigins.com/infer-utils/diagram"
	"inferorigins.com/infer-utils/markdown"
)

type page struct {
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Source string `json:"source"`
}
type group struct {
	Title string `json:"title"`
	Pages []page `json:"pages"`
}
type product struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Navigation  []group `json:"navigation"`
}
type manifest struct {
	Products  map[string]product `json:"products"`
	Redirects map[string]string  `json:"redirects,omitempty"`
}
type site struct {
	CompanyURL   string `json:"company_url"`
	PublicOrigin string `json:"public_origin"`
	Products     map[string]struct {
		Origin string `json:"origin"`
	} `json:"products"`
}
type indexRow struct {
	Product string `json:"product"`
	Section string `json:"section"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Text    string `json:"text"`
}
type ragRow struct {
	Product    string `json:"product"`
	Section    string `json:"section"`
	Title      string `json:"title"`
	Canonical  string `json:"canonical_url"`
	Repository string `json:"source_repository"`
	Source     string `json:"source_path"`
	Revision   string `json:"source_revision"`
	Visibility string `json:"visibility"`
	SHA        string `json:"sha256"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
var docPattern = regexp.MustCompile(`doc:([a-z0-9-]+)/([a-z0-9-]+)`)
var headingPattern = regexp.MustCompile(`(?m)^#\s+(.+)$`)

func main() {
	rootFlag := flag.String("root", ".", "CDeck checkout directory")
	dirFlag := flag.String("docs", "../inferorigins/infer-docs", "docs source directory relative to CDeck root")
	flag.Parse()
	if err := build(*rootFlag, *dirFlag); err != nil {
		fmt.Fprintln(os.Stderr, "docsbuild:", err)
		os.Exit(1)
	}
}
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func write(path string, data []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	return os.WriteFile(path, data, 0644)
}
func jsonWrite(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return write(path, append(b, '\n'))
}
func esc(v string) string { return html.EscapeString(v) }
func gitRev(root, repo string) string {
	if repo == "infer-docs" {
		if v := os.Getenv("PUBLIC_DOCS_REV"); v != "" {
			return v
		}
		return "working-tree"
	}
	b, e := exec.Command("git", "-C", filepath.Join(root, repo), "rev-parse", "HEAD").Output()
	if e != nil {
		return "working-tree"
	}
	return strings.TrimSpace(string(b))
}
func validOrigin(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Host != "" && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == ""
}

// validProductOrigin accepts an HTTPS host or the product's path on a shared host.
// The company origin and INFER_DOCS_BASE_URL retain their existing strict validation.
func validProductOrigin(baseURL, productID string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	return path == "" || path == "/"+productID
}
func sourcePath(root, source string) string {
	if strings.HasPrefix(source, "infer-docs/content/") {
		return filepath.Join(root, "..", "inferorigins", source)
	}
	return filepath.Join(root, source)
}

func validate(root string, m manifest, s site) error {
	if !validOrigin(s.CompanyURL) {
		return errors.New("site.json company_url must be an HTTPS origin")
	}
	if _, ok := m.Products["kforge"]; ok {
		return errors.New("KForge is an engine, not a public product")
	}
	keys := map[string]bool{}
	for id, p := range m.Products {
		if !slugPattern.MatchString(id) {
			return fmt.Errorf("invalid product: %s", id)
		}
		origin, ok := s.Products[id]
		if !ok || !validProductOrigin(origin.Origin, id) {
			return fmt.Errorf("missing or invalid origin for %s", id)
		}
		for _, g := range p.Navigation {
			for _, pg := range g.Pages {
				if !slugPattern.MatchString(pg.Slug) {
					return fmt.Errorf("invalid slug: %s", pg.Slug)
				}
				key := id + "/" + pg.Slug
				if keys[key] {
					return fmt.Errorf("duplicate page %s", key)
				}
				keys[key] = true
				if !(strings.HasPrefix(pg.Source, "infer-docs/content/") || strings.HasPrefix(pg.Source, "kforge/docs/public/") || strings.HasPrefix(pg.Source, "kforge-platform/docs/public/") || strings.HasPrefix(pg.Source, "infer-services/docs/public/")) {
					return fmt.Errorf("non-public source: %s", pg.Source)
				}
				if filepath.IsAbs(pg.Source) || strings.Contains(pg.Source, "..") || strings.Contains(pg.Source, "\\") {
					return fmt.Errorf("unsafe source: %s", pg.Source)
				}
				if _, e := os.Stat(sourcePath(root, pg.Source)); e != nil {
					return fmt.Errorf("missing %s: %w", pg.Source, e)
				}
			}
		}
	}
	// Keep existing documentation bookmarks working when a reader journey is consolidated.
	for from, to := range m.Redirects {
		parts := strings.Split(from, "/")
		target := strings.Split(to, "/")
		if len(parts) != 2 || len(target) != 2 ||
			!slugPattern.MatchString(parts[0]) || !slugPattern.MatchString(parts[1]) ||
			!slugPattern.MatchString(target[0]) || !slugPattern.MatchString(target[1]) {
			return fmt.Errorf("invalid documentation redirect: %s -> %s", from, to)
		}
		if keys[from] || !keys[to] || from == to {
			return fmt.Errorf("redirect must have an unused source and a published target: %s -> %s", from, to)
		}
		if _, ok := m.Products[parts[0]]; !ok {
			return fmt.Errorf("redirect source product is unknown: %s", from)
		}
	}
	for id, p := range m.Products {
		for _, g := range p.Navigation {
			for _, pg := range g.Pages {
				b, e := os.ReadFile(sourcePath(root, pg.Source))
				if e != nil {
					return e
				}
				for _, match := range docPattern.FindAllStringSubmatch(string(b), -1) {
					if !keys[match[1]+"/"+match[2]] {
						return fmt.Errorf("broken doc link in %s: %s", id+"/"+pg.Slug, match[0])
					}
				}
			}
		}
	}
	return nil
}
func nav(productID string, p product, current string, depth int) string {
	var b strings.Builder
	for _, group := range p.Navigation {
		b.WriteString(`<section class="nav-group"><h2>` + esc(group.Title) + `</h2>`)
		for _, pg := range group.Pages {
			target := pg.Slug + "/"
			if depth == 2 {
				target = "../" + pg.Slug + "/"
			}
			active := ""
			if pg.Slug == current {
				active = ` aria-current="page"`
			}
			b.WriteString(`<a href="` + esc(target) + `"` + active + `>` + esc(pg.Title) + `</a>`)
		}
		b.WriteString(`</section>`)
	}
	return b.String()
}

type portalSite struct {
	CompanyName string `json:"company_name"`
	DocsURL     string `json:"docs_url"`
	Description string `json:"description"`
	Socials     []struct {
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"socials"`
}
type shellData struct{ Site portalSite }

// portalShell executes the exact shared header and footer templates from portald.
// The public documentation output contains only rendered HTML and static assets.
func portalShell(portalRoot, companyURL, docsURL string) (string, string, error) {
	var data portalSite
	if err := readJSON(filepath.Join(portalRoot, "content/data/site.json"), &data); err != nil {
		return "", "", fmt.Errorf("portal site configuration: %w", err)
	}
	data.DocsURL = strings.TrimSuffix(docsURL, "/")
	render := func(filename, name string) (string, error) {
		contents, err := os.ReadFile(filepath.Join(portalRoot, "web/templates/partials", filename))
		if err != nil {
			return "", err
		}
		t, err := template.New(filename).Funcs(template.FuncMap{"year": func() int { return time.Now().Year() }}).Parse(string(contents))
		if err != nil {
			return "", err
		}
		var b bytes.Buffer
		if err = t.ExecuteTemplate(&b, name, shellData{Site: data}); err != nil {
			return "", err
		}
		// Shared templates use absolute-path site links. Resolve them to the
		// configured main portal origin, not to the docs host.
		s := b.String()
		origin := strings.TrimSuffix(companyURL, "/")
		s = strings.ReplaceAll(s, `href="/`, `href="`+origin+`/`)
		s = strings.ReplaceAll(s, `src="/`, `src="`+origin+`/`)
		return s, nil
	}
	h, err := render("header.html", "header")
	if err != nil {
		return "", "", err
	}
	f, err := render("footer.html", "footer")
	if err != nil {
		return "", "", err
	}
	return h, f, nil
}
func productTabs(m manifest, prefix string) string {
	var b strings.Builder
	b.WriteString(`<nav class="docs-products" aria-label="Documentation products">`)
	for _, id := range []string{"cdeck", "opendp"} {
		if p, ok := m.Products[id]; ok {
			b.WriteString(`<a href="` + prefix + id + `/">` + esc(p.Title) + `</a>`)
		}
	}
	b.WriteString(`</nav>`)
	return b.String()
}
func pageHTML(title, id string, m manifest, content, canonical, current string, depth int, source string, header, footer string) string {
	prefix := strings.Repeat("../", depth)
	p := m.Products[id]
	provenance := ""
	if source != "" {
		provenance = `<div class="source">Documentation source: <code>` + esc(source) + `</code></div>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="description" content="` + esc(p.Description) + `"><title>` + esc(title) + ` · ` + esc(p.Title) + ` · Infer Origins</title><link rel="canonical" href="` + esc(canonical) + `"><link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.11.1/styles/github.min.css"><link rel="stylesheet" href="` + prefix + `assets/site.css"><link rel="stylesheet" href="` + prefix + `assets/docs.css"></head><body data-mode="light" data-theme="signal"><a class="skip-link" href="#content">Skip to content</a>` + header + `<div class="docs-shell">` + productTabs(m, prefix) + `<div class="layout"><aside aria-label="Documentation navigation"><p class="product">` + esc(p.Title) + `</p>` + nav(id, p, current, depth) + `</aside><main id="content" tabindex="-1"><div id="search-results" hidden></div><label class="search-label">Search documentation<input id="search" type="search" placeholder="Search docs" autocomplete="off"></label><article class="article-content">` + content + `</article>` + provenance + `</main></div></div>` + footer + `<script src="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.11.1/highlight.min.js" defer></script><script src="` + prefix + `assets/site.js" defer></script><script src="` + prefix + `assets/search.js" defer></script><script src="` + prefix + `assets/article-enhance.js" defer></script></body></html>`
}
func build(root, docs string) error {
	absRoot, e := filepath.Abs(root)
	if e != nil {
		return e
	}
	here := filepath.Join(absRoot, docs)
	out := filepath.Join(here, "dist")
	portalRoot := filepath.Clean(filepath.Join(absRoot, "..", "inferorigins", "portald"))
	var m manifest
	var s site
	if e = readJSON(filepath.Join(here, "manifest.json"), &m); e != nil {
		return e
	}
	if e = readJSON(filepath.Join(here, "site.json"), &s); e != nil {
		return e
	}
	if e = validate(absRoot, m, s); e != nil {
		return e
	}
	override := strings.TrimSuffix(strings.TrimSpace(os.Getenv("INFER_DOCS_BASE_URL")), "/")
	if override != "" && !validOrigin(override) {
		return errors.New("INFER_DOCS_BASE_URL must be an HTTPS origin")
	}
	// Local preview changes navigation only. Canonical documentation URLs remain
	// configured for the production build, and no configuration file is rewritten.
	companyURL := s.CompanyURL
	if localOrigin := strings.TrimSpace(os.Getenv("INFER_LOCAL_PORTAL_ORIGIN")); localOrigin != "" {
		parsed, parseErr := url.Parse(localOrigin)
		if parseErr != nil || parsed.Scheme != "http" || parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
			return errors.New("INFER_LOCAL_PORTAL_ORIGIN must be an HTTP localhost origin")
		}
		companyURL = strings.TrimSuffix(localOrigin, "/")
	}
	header, footer, e := portalShell(portalRoot, companyURL, s.PublicOrigin)
	if e != nil {
		return e
	}
	if e = os.RemoveAll(out); e != nil {
		return e
	}
	for _, name := range []string{"docs.css", "search.js", "article-enhance.js"} {
		assetPath := filepath.Join(here, "assets", name)
		if name == "article-enhance.js" {
			assetPath = filepath.Join(portalRoot, "..", "infer-utils", "assets", name)
		}
		b, e := os.ReadFile(assetPath)
		if e != nil {
			return e
		}
		if e = write(filepath.Join(out, "assets", name), b); e != nil {
			return e
		}
	}
	// Reuse product banner images from the portal's existing solution catalog.
	productBanners := make(map[string]string)
	for _, id := range []string{"cdeck", "opendp"} {
		var solution struct {
			Image string `json:"image"`
		}
		if err := readJSON(filepath.Join(portalRoot, "content", "solutions", id+".json"), &solution); err != nil {
			return fmt.Errorf("solution banner %s: %w", id, err)
		}
		if !strings.HasPrefix(solution.Image, "/static/images/offerings/") || strings.Contains(solution.Image, "..") {
			return fmt.Errorf("invalid solution banner path for %s: %q", id, solution.Image)
		}
		filename := "product-" + id + filepath.Ext(solution.Image)
		data, err := os.ReadFile(filepath.Join(portalRoot, "web", "static", strings.TrimPrefix(solution.Image, "/static/")))
		if err != nil {
			return fmt.Errorf("read solution banner %s: %w", id, err)
		}
		if err := write(filepath.Join(out, "assets", filename), data); err != nil {
			return err
		}
		productBanners[id] = filename
	}
	// Reuse the portal's compiled CSS and its mobile navigation JavaScript.
	for _, asset := range []struct{ src, dst string }{
		{"web/static/css/site.css", "assets/site.css"},
		{"web/static/js/site.js", "assets/site.js"},
	} {
		b, err := os.ReadFile(filepath.Join(portalRoot, asset.src))
		if err != nil {
			return err
		}
		if err = write(filepath.Join(out, asset.dst), b); err != nil {
			return err
		}
	}
	rows := []indexRow{}
	rags := []ragRow{}
	var cards strings.Builder
	for _, id := range []string{"cdeck", "opendp"} {
		p, ok := m.Products[id]
		if !ok {
			continue
		}
		origin := strings.TrimSuffix(s.Products[id].Origin, "/")
		if override != "" {
			origin = override + "/" + id
		}
		first := ""
		if len(p.Navigation) > 0 && len(p.Navigation[0].Pages) > 0 {
			first = p.Navigation[0].Pages[0].Slug
		}
		landing := `<img class="docs-product-banner" src="../assets/` + esc(productBanners[id]) + `" alt="" loading="eager"><h1>` + esc(p.Title) + `</h1><p class="lead">` + esc(p.Description) + `</p><p><a href="` + first + `/">Get started →</a></p>`
		if e = write(filepath.Join(out, id, "index.html"), []byte(pageHTML(p.Title, id, m, landing, origin+"/", "", 1, "", header, footer))); e != nil {
			return e
		}
		cards.WriteString(`<a class="product-card" href="` + id + `/"><img class="docs-card-banner" src="assets/` + esc(productBanners[id]) + `" alt="" loading="lazy"><strong>` + esc(p.Title) + `</strong><span>` + esc(p.Description) + `</span></a>`)
		for _, g := range p.Navigation {
			for _, pg := range g.Pages {
				raw, e := os.ReadFile(sourcePath(absRoot, pg.Source))
				if e != nil {
					return e
				}
				for _, issue := range diagram.ValidateMarkdown(raw) {
                    location := fmt.Sprintf("%s:%d", pg.Source, issue.Line)
                    if issue.Level == "error" { return fmt.Errorf("Mermaid policy %s: %s", location, issue.Message) }
                    fmt.Fprintf(os.Stderr, "docsbuild: Mermaid policy warning %s: %s\n", location, issue.Message)
                }
                contentMarkdown := docPattern.ReplaceAllStringFunc(string(raw), func(v string) string {
					ss := docPattern.FindStringSubmatch(v)
					if ss[1] == id {
						return "../" + ss[2] + "/"
					}
					return "../../" + ss[1] + "/" + ss[2] + "/"
				})
				var rendered bytes.Buffer
				if e = markdown.Convert([]byte(contentMarkdown), &rendered); e != nil {
					return e
				}
				title := pg.Title
				if h := headingPattern.FindStringSubmatch(string(raw)); len(h) > 1 {
					title = h[1]
				}
				canon := origin + "/" + pg.Slug + "/"
				pageout := filepath.Join(out, id, pg.Slug, "index.html")
				if e = write(pageout, []byte(pageHTML(title, id, m, rendered.String(), canon, pg.Slug, 2, pg.Source, header, footer))); e != nil {
					return e
				}
				sum := sha256.Sum256(raw)
				repo := strings.Split(pg.Source, "/")[0]
				plain := strings.Join(strings.Fields(string(raw)), " ")
				if len(plain) > 20000 {
					plain = plain[:20000]
				}
				rows = append(rows, indexRow{id, g.Title, title, "/" + id + "/" + pg.Slug + "/", plain})
				rags = append(rags, ragRow{id, g.Title, title, canon, repo, pg.Source, gitRev(absRoot, repo), "public", fmt.Sprintf("%x", sum)})
			}
		}
	}
	// Redirect former public URLs without adding obsolete entries to navigation,
	// search indexes or the RAG manifest. Use local relative URLs in previews.
	for from, to := range m.Redirects {
		parts := strings.Split(from, "/")
		target := strings.Split(to, "/")
		href := strings.TrimSuffix(s.Products[target[0]].Origin, "/") + "/" + target[1] + "/"
		if override != "" {
			// Local preview serves both products under one host.
			href = "../../" + target[0] + "/" + target[1] + "/"
		}
		doc := `<!doctype html><html lang="en"><head><meta charset="utf-8">` +
			`<meta http-equiv="refresh" content="0;url=` + esc(href) + `">` +
			`<meta name="robots" content="noindex">` +
			`<title>Page moved</title></head><body><p>This documentation has moved to ` +
			`<a href="` + esc(href) + `">the updated guide</a>.</p></body></html>`
		if e = write(filepath.Join(out, parts[0], parts[1], "index.html"), []byte(doc)); e != nil {
			return e
		}
	}
	home := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Infer Origins Documentation</title><link rel="stylesheet" href="assets/site.css"><link rel="stylesheet" href="assets/docs.css"></head><body data-mode="light" data-theme="signal">` + header + `<div class="docs-shell">` + productTabs(m, "./") + `<main class="home"><h1>Documentation</h1><p class="lead">CDeck and OpenDP technical documentation, assembled from reviewed repository-owned sources.</p><div class="products">` + cards.String() + `</div></main></div>` + footer + `<script src="assets/site.js" defer></script></body></html>`
	if e = write(filepath.Join(out, "index.html"), []byte(home)); e != nil {
		return e
	}
	if e = jsonWrite(filepath.Join(out, "index.json"), rows); e != nil {
		return e
	}
	if e = jsonWrite(filepath.Join(out, "rag-manifest.json"), rags); e != nil {
		return e
	}
	fmt.Printf("Generated %d documentation pages, %d products: %s\n", len(rows), len(m.Products), out)
	return nil
}
