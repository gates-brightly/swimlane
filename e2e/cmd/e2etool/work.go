package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

// The "real work" of dag99 lanes 1-9. Each command reads and writes files in
// $D (.scenario/dag) the way the lane script describes.

// ---- HTML walking --------------------------------------------------------

// handler receives parse events like Python's html.parser.HTMLParser: tag
// names lower-cased, attribute values and text unescaped, and a
// self-closing tag reported as a start followed by an end.
type handler interface {
	start(tag string, attrs map[string]string)
	end(tag string)
	text(data string)
}

func walk(r io.Reader, h handler) error {
	z := xhtml.NewTokenizer(r)
	for {
		switch z.Next() {
		case xhtml.ErrorToken:
			if z.Err() == io.EOF {
				return nil
			}
			return z.Err()
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			t := z.Token()
			attrs := map[string]string{}
			for _, a := range t.Attr {
				if _, dup := attrs[a.Key]; !dup {
					attrs[a.Key] = a.Val
				}
			}
			h.start(t.Data, attrs)
			if t.Type == xhtml.SelfClosingTagToken {
				h.end(t.Data)
			}
		case xhtml.EndTagToken:
			h.end(z.Token().Data)
		case xhtml.TextToken:
			h.text(string(z.Text()))
		}
	}
}

var skipTags = map[string]bool{"script": true, "style": true, "head": true, "noscript": true, "svg": true}

var externalRE = regexp.MustCompile(`^https?://`)

// ---- swim 1: aggregate ---------------------------------------------------

var (
	titleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	bodyRE  = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)
)

// aggregate: every page in $P into one combined.html, a <section> per page.
func cmdAggregate([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	pages, err := env("P")
	if err != nil {
		return err
	}
	job, err := env("SWIM_JOB")
	if err != nil {
		return err
	}
	paths, _ := filepath.Glob(filepath.Join(pages, "*.html"))
	sort.Strings(paths)
	var parts []string
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".html")
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := strings.ToValidUTF8(string(raw), "�")
		title := name
		if t := titleRE.FindStringSubmatch(src); t != nil {
			title = html.UnescapeString(strings.TrimSpace(t[1]))
		}
		body := src
		if b := bodyRE.FindStringSubmatch(src); b != nil {
			body = b[1]
		}
		parts = append(parts, fmt.Sprintf("<section data-source=%q>\n<h1>%s</h1>\n<p><em>source: %s</em></p>\n%s\n</section>",
			name, html.EscapeString(title), name, body))
		fmt.Printf("%s: '%s' (%d chars)\n", name, title, utf8.RuneCountInString(src))
	}
	doc := "<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>swim dag demo</title></head><body>\n" +
		strings.Join(parts, "\n<hr>\n") + "\n</body></html>\n"
	if err := writeAtomic(filepath.Join(d, "combined.html"), filepath.Join(d, ".combined."+job+".tmp"), []byte(doc)); err != nil {
		return err
	}
	fmt.Printf("wrote %s/combined.html: %d pages, %d chars\n", d, len(parts), utf8.RuneCountInString(doc))
	return nil
}

// ---- swim 2: markdown ----------------------------------------------------

type mdList struct {
	tag string
	n   int
}

type mdConv struct {
	out   strings.Builder
	skip  int
	lists []*mdList
	href  string
	pre   int
}

var (
	headingRE = regexp.MustCompile(`^h[1-6]$`)
	spaceRE   = regexp.MustCompile(`\s+`)
)

func (m *mdConv) w(s string) { m.out.WriteString(s) }
func (m *mdConv) block()     { m.w("\n\n") }

func (m *mdConv) start(tag string, a map[string]string) {
	if skipTags[tag] {
		m.skip++
		return
	}
	if m.skip > 0 {
		return
	}
	switch {
	case headingRE.MatchString(tag):
		m.block()
		lvl, _ := strconv.Atoi(tag[1:])
		m.w(strings.Repeat("#", lvl) + " ")
	case tag == "p" || tag == "div" || tag == "section" || tag == "dl" || tag == "table":
		m.block()
	case tag == "dt" || tag == "tr":
		m.w("\n")
	case tag == "dd":
		m.w("\n: ")
	case tag == "td" || tag == "th":
		m.w(" | ")
	case tag == "br":
		m.w("  \n")
	case tag == "hr":
		m.block()
		m.w("---")
		m.block()
	case tag == "strong" || tag == "b":
		m.w("**")
	case tag == "em" || tag == "i":
		m.w("*")
	case tag == "code" && m.pre == 0:
		m.w("`")
	case tag == "pre":
		m.pre++
		m.block()
		m.w("```\n")
	case tag == "ul" || tag == "ol":
		m.lists = append(m.lists, &mdList{tag: tag})
		m.w("\n")
	case tag == "li":
		depth := max(len(m.lists)-1, 0)
		kind := &mdList{tag: "ul"}
		if len(m.lists) > 0 {
			kind = m.lists[len(m.lists)-1]
		}
		kind.n++
		bullet := "- "
		if kind.tag == "ol" {
			bullet = fmt.Sprintf("%d. ", kind.n)
		}
		m.w("\n" + strings.Repeat("  ", depth) + bullet)
	case tag == "a":
		m.href = a["href"]
		m.w("[")
	case tag == "img":
		m.w(fmt.Sprintf("![%s](%s)", a["alt"], a["src"]))
	}
}

func (m *mdConv) end(tag string) {
	if skipTags[tag] {
		m.skip = max(m.skip-1, 0)
		return
	}
	if m.skip > 0 {
		return
	}
	switch {
	case headingRE.MatchString(tag) || tag == "p" || tag == "div" || tag == "section" || tag == "table":
		m.block()
	case tag == "strong" || tag == "b":
		m.w("**")
	case tag == "em" || tag == "i":
		m.w("*")
	case tag == "code" && m.pre == 0:
		m.w("`")
	case tag == "pre":
		m.pre--
		m.w("\n```")
		m.block()
	case tag == "ul" || tag == "ol":
		if len(m.lists) > 0 {
			m.lists = m.lists[:len(m.lists)-1]
		}
		m.block()
	case tag == "a":
		if m.href != "" {
			m.w("](" + m.href + ")")
		} else {
			m.w("]")
		}
		m.href = ""
	}
}

func (m *mdConv) text(data string) {
	if m.skip > 0 {
		return
	}
	if m.pre > 0 {
		m.w(data)
	} else {
		m.w(spaceRE.ReplaceAllString(data, " "))
	}
}

var (
	trailingRE  = regexp.MustCompile(`[ \t]+\n`)
	blankRunsRE = regexp.MustCompile(`\n{3,}`)
)

// stripStrayIndent removes spaces or tabs after a newline unless the next
// character starts a list item (- or a digit) or is whitespace.
func stripStrayIndent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteByte(s[i])
		if s[i] != '\n' {
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
			j++
		}
		if j > i+1 && j < len(s) {
			c := s[j]
			if c != '-' && (c < '0' || c > '9') && c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != '\v' {
				i = j - 1 // drop the indentation
			}
		}
	}
	return b.String()
}

// markdown: $IN (a private copy of combined.html) -> $OUT/combined.md.
func cmdMarkdown([]string) error {
	in, err := env("IN")
	if err != nil {
		return err
	}
	outDir, err := env("OUT")
	if err != nil {
		return err
	}
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	m := &mdConv{}
	if err := walk(f, m); err != nil {
		return err
	}
	md := m.out.String()
	md = trailingRE.ReplaceAllString(md, "\n")
	md = stripStrayIndent(md)
	md = blankRunsRE.ReplaceAllString(md, "\n\n")
	md = strings.TrimSpace(md) + "\n"
	dst := filepath.Join(outDir, "combined.md")
	if err := os.WriteFile(dst, []byte(md), 0o644); err != nil {
		return err
	}
	lines := strings.Count(md, "\n")
	fmt.Printf("wrote %s: %d lines, %d chars\n", dst, lines, utf8.RuneCountInString(md))
	return nil
}

// ---- swim 3: stats -------------------------------------------------------

type page struct {
	Source        string `json:"source"`
	Title         string `json:"title"`
	Words         int    `json:"words"`
	LinksInternal int    `json:"links_internal"`
	LinksExternal int    `json:"links_external"`
}

type statsFile struct {
	RunID string  `json:"run_id"`
	Job   string  `json:"job"`
	Pages []*page `json:"pages"`
}

type statsWalker struct {
	pages []*page
	cur   *page
	skip  int
	inH1  bool
}

func (s *statsWalker) start(tag string, a map[string]string) {
	if skipTags[tag] {
		s.skip++
	}
	src, hasSrc := a["data-source"]
	switch {
	case tag == "section" && hasSrc:
		s.cur = &page{Source: src}
		s.pages = append(s.pages, s.cur)
	case s.cur != nil && tag == "h1" && s.cur.Title == "":
		s.inH1 = true
	case s.cur != nil && tag == "a" && a["href"] != "":
		if externalRE.MatchString(a["href"]) {
			s.cur.LinksExternal++
		} else {
			s.cur.LinksInternal++
		}
	}
}

func (s *statsWalker) end(tag string) {
	if skipTags[tag] {
		s.skip = max(s.skip-1, 0)
	}
	if tag == "h1" {
		s.inH1 = false
	}
}

func (s *statsWalker) text(data string) {
	if s.skip > 0 || s.cur == nil {
		return
	}
	if s.inH1 {
		s.cur.Title += strings.TrimSpace(data)
	}
	s.cur.Words += len(strings.Fields(data))
}

// stats: per-page title, words and link counts from $IN -> $D/stats.json.
func cmdStats([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	in, err := env("IN")
	if err != nil {
		return err
	}
	runID, err := env("RUN_ID")
	if err != nil {
		return err
	}
	job, err := env("SWIM_JOB")
	if err != nil {
		return err
	}
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	s := &statsWalker{}
	if err := walk(f, s); err != nil {
		return err
	}
	out := statsFile{RunID: runID, Job: job, Pages: s.pages}
	if out.Pages == nil {
		out.Pages = []*page{}
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	if err := writeAtomic(filepath.Join(d, "stats.json"), filepath.Join(d, ".stats.tmp"), data); err != nil {
		return err
	}
	for _, p := range s.pages {
		fmt.Printf("%-8s words=%5d int=%3d ext=%3d  %s\n", p.Source, p.Words, p.LinksInternal, p.LinksExternal, p.Title)
	}
	if len(s.pages) == 0 {
		return fmt.Errorf("no pages found")
	}
	return nil
}

func readStats(d string) (statsFile, error) {
	var s statsFile
	data, err := os.ReadFile(filepath.Join(d, "stats.json"))
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// stats-pages: how many pages stats.json has.
func cmdStatsPages([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	s, err := readStats(d)
	if err != nil {
		return err
	}
	fmt.Println(len(s.Pages))
	return nil
}

// ---- swim 4: report ------------------------------------------------------

// report: stats table (swim 3) + converted content (swim 2) -> report.md.
func cmdReport([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	job, err := env("SWIM_JOB")
	if err != nil {
		return err
	}
	stats, err := readStats(d)
	if err != nil {
		return err
	}
	mdRaw, err := os.ReadFile(filepath.Join(d, "combined.md"))
	if err != nil {
		return err
	}
	rows := []string{"| source | title | words | internal links | external links |", "|---|---|---:|---:|---:|"}
	var words, in, ext int
	for _, p := range stats.Pages {
		rows = append(rows, fmt.Sprintf("| %s | %s | %d | %d | %d |", p.Source, p.Title, p.Words, p.LinksInternal, p.LinksExternal))
		words, in, ext = words+p.Words, in+p.LinksInternal, ext+p.LinksExternal
	}
	rows = append(rows, fmt.Sprintf("| **total** | %d pages | %d | %d | %d |", len(stats.Pages), words, in, ext))
	body := strings.TrimPrefix(strings.ReplaceAll(string(mdRaw), "\n# ", "\n## "), "# ")
	report := fmt.Sprintf("# Swim diamond demo report\n\nRoot run: `%s`  \nJoin job: `%s`\n\n", stats.RunID, job) +
		"## Stats (swim 3)\n\n" + strings.Join(rows, "\n") + "\n\n## Content (swim 2)\n\n## " + body
	if err := os.WriteFile(filepath.Join(d, "report.md"), []byte(report), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s/report.md: %d lines\n", d, len(strings.Split(strings.TrimSuffix(report, "\n"), "\n")))
	return nil
}

// ---- swim 5: links -------------------------------------------------------

type linksWalker struct {
	src  string
	rows [][3]string
}

func (l *linksWalker) start(tag string, a map[string]string) {
	if src, ok := a["data-source"]; tag == "section" && ok {
		l.src = src
	} else if tag == "a" && a["href"] != "" && l.src != "" {
		kind := "internal"
		if externalRE.MatchString(a["href"]) {
			kind = "external"
		}
		l.rows = append(l.rows, [3]string{l.src, kind, a["href"]})
	}
}
func (l *linksWalker) end(string)  {}
func (l *linksWalker) text(string) {}

// links: every <a href> in combined.html, per source page -> links.tsv.
func cmdLinks([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(d, "combined.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	l := &linksWalker{}
	if err := walk(f, l); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("source\tkind\thref\n")
	ext := 0
	for _, r := range l.rows {
		b.WriteString(strings.Join(r[:], "\t") + "\n")
		if r[1] == "external" {
			ext++
		}
	}
	if err := writeAtomic(filepath.Join(d, "links.tsv"), filepath.Join(d, ".links.tmp"), []byte(b.String())); err != nil {
		return err
	}
	fmt.Printf("%d links: %d external\n", len(l.rows), ext)
	if len(l.rows) == 0 {
		return fmt.Errorf("no links found")
	}
	return nil
}

// readLinks parses links.tsv into rows of source, kind, href.
func readLinks(d string) ([]map[string]string, error) {
	f, err := os.Open(filepath.Join(d, "links.tsv"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	var rows []map[string]string
	for _, rec := range recs[1:] {
		row := map[string]string{}
		for i, k := range recs[0] {
			if i < len(rec) {
				row[k] = rec[i]
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// counted is a count that remembers first appearance, so ties sort like
// Python's Counter.most_common (first seen first).
type counted struct {
	key   string
	n     int
	first int
}

func mostCommon(keys []string, limit int) []counted {
	idx := map[string]*counted{}
	var all []*counted
	for i, k := range keys {
		c, ok := idx[k]
		if !ok {
			c = &counted{key: k, first: i}
			idx[k] = c
			all = append(all, c)
		}
		c.n++
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].n > all[b].n })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]counted, len(all))
	for i, c := range all {
		out[i] = *c
	}
	return out
}

// ---- swim 6: word frequency ----------------------------------------------

var (
	linkTargetRE = regexp.MustCompile(`\]\([^)]*\)`)
	wordRE       = regexp.MustCompile(`[a-z][a-z']{3,}`)
	stopWords    = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields("the and that this with from have been were they their them there which what when your about into will would could should these those other than then also such only some more most very upon over under after before while being does done") {
		stopWords[w] = true
	}
}

// wordfreq: top 15 words in combined.md (links and stopwords removed).
func cmdWordfreq([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(d, "combined.md"))
	if err != nil {
		return err
	}
	md := linkTargetRE.ReplaceAllString(string(raw), "]")
	var words []string
	for _, w := range wordRE.FindAllString(strings.ToLower(md), -1) {
		if !stopWords[w] {
			words = append(words, w)
		}
	}
	top := mostCommon(words, 15)
	var b strings.Builder
	b.WriteString("## Word frequency (swim 6)\n\n| word | count |\n|---|---:|\n")
	for _, c := range top {
		fmt.Fprintf(&b, "| %s | %d |\n", c.key, c.n)
	}
	if err := os.WriteFile(filepath.Join(d, "wordfreq.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Println(b.String())
	if len(top) == 0 {
		return fmt.Errorf("no words")
	}
	return nil
}

// ---- swim 7: reconcile and domains ---------------------------------------

// reconcile: external links per page in links.tsv (swim 5) must equal
// stats.json (swim 3).
func cmdReconcile([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	stats, err := readStats(d)
	if err != nil {
		return err
	}
	rows, err := readLinks(d)
	if err != nil {
		return err
	}
	want := map[string]int{}
	srcs := map[string]bool{}
	for _, p := range stats.Pages {
		want[p.Source] = p.LinksExternal
		srcs[p.Source] = true
	}
	got := map[string]int{}
	for _, r := range rows {
		if r["kind"] == "external" {
			got[r["source"]]++
			srcs[r["source"]] = true
		}
	}
	var names []string
	for s := range srcs {
		names = append(names, s)
	}
	sort.Strings(names)
	bad := 0
	for _, s := range names {
		m := "ok"
		if want[s] != got[s] {
			m = "MISMATCH"
			bad++
		}
		fmt.Printf("%-8s stats=%3d  links.tsv=%3d  %s\n", s, want[s], got[s], m)
	}
	if bad > 0 {
		return exitCode(1)
	}
	return nil
}

// domains: external link domains from links.tsv -> domains.md.
func cmdDomains([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	rows, err := readLinks(d)
	if err != nil {
		return err
	}
	var hosts []string
	for _, r := range rows {
		if r["kind"] != "external" {
			continue
		}
		host := ""
		if u, err := url.Parse(r["href"]); err == nil {
			host = u.Host
		}
		hosts = append(hosts, host)
	}
	var b strings.Builder
	b.WriteString("## External link domains (swim 7)\n\n| domain | links |\n|---|---:|\n")
	for _, c := range mostCommon(hosts, 0) {
		fmt.Fprintf(&b, "| %s | %d |\n", c.key, c.n)
	}
	if err := os.WriteFile(filepath.Join(d, "domains.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Println(b.String())
	return nil
}

// ---- swim 8: headers -----------------------------------------------------

// headers: status, server and content-type per $H/<site>.txt -> headers.md.
func cmdHeaders([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	hdir, err := env("H")
	if err != nil {
		return err
	}
	paths, _ := filepath.Glob(filepath.Join(hdir, "*.txt"))
	sort.Strings(paths)
	var rows []string
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		text := strings.ToValidUTF8(strings.ReplaceAll(string(raw), "\r\n", "\n"), "�")
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		hdr := map[string]string{}
		for _, l := range lines[1:] {
			k, v, _ := strings.Cut(l, ":")
			if v = strings.TrimSpace(v); v != "" {
				hdr[strings.ToLower(strings.TrimSpace(k))] = v
			}
		}
		status := "?"
		if len(lines) > 0 {
			status = strings.TrimSpace(lines[0])
		}
		get := func(k string) string {
			if v, ok := hdr[k]; ok {
				return v
			}
			return "-"
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s |", strings.TrimSuffix(filepath.Base(p), ".txt"), status, get("server"), get("content-type")))
	}
	out := "## Response headers (swim 8)\n\n| site | status | server | content-type |\n|---|---|---|---|\n" + strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(d, "headers.md"), []byte(out), 0o644); err != nil {
		return err
	}
	fmt.Println(out)
	if len(rows) == 0 {
		return fmt.Errorf("no headers")
	}
	return nil
}
