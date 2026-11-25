// E-002, finish line 3: Opening a report adds one to its times-opened count. Opening it again from the same visitor on the same day adds nothing, and a request from an obvious bot or crawler adds nothing.
//
// It builds ./cmd/hband from this tree, runs it with the HBAND_* settings in start, and drives it
// over HTTP as the VPS proxy forwards a browser, signing in through a stand-in GitHub of its own.
package e002openscountedonce

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

const site, chrome = "https://hband.test", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"

type account struct {
	id           int
	login, image string
}

var owner, stranger = account{4242, "report-author", "https://avatars.example/4242"}, account{9001, "passer-by", "https://avatars.example/9001"}

func hexes(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func mark(word string) string { return word + "-" + hexes(6) }

func report(title, marker, extra string) []byte {
	return []byte(`<!doctype html><html><head><meta charset="utf-8"><title>` + title + `</title><style>body{margin:2em}</style></head><body><h1>` +
		title + `</h1><p>` + marker + `</p>` + extra + `</body></html>`)
}

type page struct {
	url    string
	status int
	header http.Header
	text   string
}

// app is the server `go build ./cmd/hband` makes of this tree, on a free port with a fresh data
// folder, the owner pinned by GitHub user ID and a stand-in GitHub: a code it gave out buys a
// token at /login/oauth/access_token, and the token buys that account at /user.
type app struct {
	t          *testing.T
	data, port string
	gh         *httptest.Server
	mu         sync.Mutex
	grants     map[string]account
	cmd        *exec.Cmd
	done       chan bool
}

func start(t *testing.T, maxUpload int) *app {
	work, _ := os.MkdirTemp("", "hband-acceptance-")
	a := &app{t: t, data: filepath.Join(work, "data"), grants: map[string]account{}, done: make(chan bool)}
	t.Cleanup(func() {
		if a.halt(); a.gh != nil {
			a.gh.Close()
		}
		for i := 0; i < 20 && os.RemoveAll(work) != nil; i++ {
			time.Sleep(250 * time.Millisecond)
		}
	})
	exe, root := filepath.Join(work, "hband.exe"), filepath.Join("..", "..", "..")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", exe, "./cmd/hband")
	build.Dir, build.Env = root, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the product's build failed: `go build ./cmd/hband` in %s: %v\n%s", root, err, out)
	}
	a.gh = httptest.NewServer(http.HandlerFunc(a.github))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.port = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	log, _ := os.Create(filepath.Join(work, "server.log"))
	os.MkdirAll(a.data, 0o755)
	a.cmd = exec.Command(exe)
	a.cmd.Dir, a.cmd.Stdout, a.cmd.Stderr = work, log, log
	a.cmd.Env = append(os.Environ(), "HBAND_ADDR=127.0.0.1:"+a.port, "HBAND_DATA_DIR="+a.data, "HBAND_BASE_URL="+site,
		"HBAND_OWNER_GITHUB_ID=4242", "HBAND_GITHUB_CLIENT_ID=hband-test", "HBAND_GITHUB_CLIENT_SECRET=hband-test-secret",
		"HBAND_GITHUB_URL="+a.gh.URL, "HBAND_GITHUB_API_URL="+a.gh.URL, "HBAND_MAX_UPLOAD_BYTES="+strconv.Itoa(maxUpload),
		"HBAND_TRUSTED_PROXIES=127.0.0.1")
	if err := a.cmd.Start(); err != nil {
		t.Fatalf("the server did not start: %v", err)
	}
	go func() {
		a.cmd.Wait()
		log.Close()
		close(a.done)
	}()
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(250 * time.Millisecond) {
		if _, err := a.browser("192.0.2.1", chrome).send("GET", "/", nil, nil); err == nil {
			return a
		}
		select {
		case <-a.done:
			f, _ := os.Open(log.Name())
			said, _ := io.ReadAll(f)
			f.Close()
			t.Fatalf("the server exited (%v):\n%s", a.cmd.ProcessState, said)
		default:
			if time.Now().After(deadline) {
				t.Fatalf("the server did not answer in a minute")
			}
		}
	}
}

func (a *app) halt() {
	if a.cmd != nil && a.cmd.Process != nil {
		a.cmd.Process.Kill()
		<-a.done
	}
}

func (a *app) github(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sent, _ := io.ReadAll(r.Body)
	asked, _ := url.ParseQuery(string(sent) + "&" + r.URL.RawQuery)
	auth := strings.Fields("- " + r.Header.Get("Authorization"))
	code, tok := asked.Get("code"), auth[len(auth)-1]
	w.Header().Set("Content-Type", "application/json")
	if u, ok := a.grants[code]; ok && !strings.HasPrefix(code, "gho_") && r.Method == "POST" &&
		strings.Contains(r.URL.Path, "/login/oauth/access_token") {
		delete(a.grants, code)
		tok = "gho_" + hexes(16)
		a.grants[tok] = u
		if !strings.Contains(r.Header.Get("Accept"), "json") {
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			io.WriteString(w, "access_token="+tok+"&token_type=bearer&scope=")
			return
		}
		fmt.Fprintf(w, `{"access_token": %q, "token_type": "bearer", "scope": ""}`, tok)
	} else if u, ok := a.grants[tok]; ok && strings.HasPrefix(tok, "gho_") && strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/user") {
		json.NewEncoder(w).Encode(map[string]any{"id": u.id, "login": u.login, "avatar_url": u.image, "name": u.login, "type": "User"})
	} else {
		w.WriteHeader(401)
		io.WriteString(w, `{"message": "Bad credentials"}`)
	}
}

var plain = &http.Transport{DisableKeepAlives: true, DisableCompression: true}

// via sends a browser's request as the VPS proxy forwards it: to this server, with the public
// host, https, and the visitor's address in X-Forwarded-For and X-Real-IP.
type via struct{ port, ip, agent string }

func (v via) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", "127.0.0.1:"+v.port
	for k, x := range map[string]string{"User-Agent": v.agent, "Accept": "text/html,*/*", "X-Forwarded-For": v.ip, "X-Real-IP": v.ip, "X-Forwarded-Proto": "https"} {
		r.Header.Set(k, x)
	}
	return plain.RoundTrip(r)
}

type browser struct {
	t *testing.T
	c *http.Client
}

func (a *app) browser(ip, agent string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{a.t, &http.Client{Jar: jar, Timeout: time.Minute, Transport: via{a.port, ip, agent},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	r, err2 := url.Parse(strings.TrimSpace(ref))
	if err != nil || err2 != nil {
		return "", fmt.Errorf("%q from %q does not parse", ref, base)
	}
	return b.ResolveReference(r).String(), nil
}

func (b *browser) send(method, target string, body []byte, headers map[string]string) (*page, error) {
	to, err := resolve(site+"/", target)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(method, to, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	return &page{to, resp.StatusCode, resp.Header, string(text)}, err
}

func (b *browser) do(method, target string, body []byte, headers map[string]string) *page {
	b.t.Helper()
	pg, err := b.send(method, target, body, headers)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, target, err)
	}
	return pg
}

func (b *browser) get(target string) *page {
	b.t.Helper()
	return b.do("GET", target, nil, nil)
}

var attrRE = regexp.MustCompile(`([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`)

func tags(text, name string) (out []map[string]string) {
	for _, m := range regexp.MustCompile(`(?i)<`+name+`\b((?:[^>"']|"[^"]*"|'[^']*')*)>`).FindAllStringSubmatch(text, -1) {
		attrs := map[string]string{}
		for _, a := range attrRE.FindAllStringSubmatch(m[1], -1) {
			attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2] + a[3] + a[4])
		}
		out = append(out, attrs)
	}
	return out
}

type frame struct {
	attrs map[string]string
	got   *page
}

func (b *browser) frames(pg *page) (out []frame) {
	b.t.Helper()
	for _, attrs := range tags(pg.text, "iframe") {
		f, src := frame{attrs, nil}, strings.TrimSpace(attrs["src"])
		if to, err := resolve(pg.url, src); err == nil && src != "" && !regexp.MustCompile(`(?i)^(about|data|javascript|blob):`).MatchString(src) {
			f.got = b.get(to)
		}
		out = append(out, f)
	}
	return out
}

// signIn is a browser that went through GitHub sign-in as user, and the answer when GitHub sent it back.
func (a *app) signIn(user account, ip string) (*browser, *page) {
	a.t.Helper()
	b := a.browser(ip, chrome)
	start := b.get("/auth/github/login")
	to := start.header.Get("Location")
	u, err := url.Parse(to)
	if start.status/100 != 3 || err != nil || !strings.HasPrefix(to, a.gh.URL+"/login/oauth/authorize") {
		a.t.Fatalf("GET /auth/github/login answered %d %q, not GitHub's authorize page", start.status, to)
	}
	code, back := hexes(10), u.Query().Get("redirect_uri")
	a.mu.Lock()
	a.grants[code] = user
	a.mu.Unlock()
	if back == "" {
		back = site + "/auth/github/callback"
	}
	cb, err := url.Parse(back)
	if err != nil {
		a.t.Fatalf("the redirect_uri %q: %v", back, err)
	}
	q := cb.Query()
	q.Set("code", code)
	q.Set("state", u.Query().Get("state"))
	cb.RawQuery, cb.Fragment = q.Encode(), ""
	return b, b.get(cb.String())
}

// form is what the admin page's forms send besides their own fields: its CSRF token, from hidden
// inputs or a meta tag sent as X-CSRF-Token.
func (b *browser) form() (url.Values, map[string]string) {
	b.t.Helper()
	pg, fields, headers := b.get("/admin"), url.Values{}, map[string]string{}
	for _, n := range tags(pg.text, "input") {
		if strings.EqualFold(n["type"], "hidden") && regexp.MustCompile(`(?i)csrf|xsrf|token|authenticity`).MatchString(n["name"]) {
			fields.Set(n["name"], n["value"])
		}
	}
	for _, n := range tags(pg.text, "meta") {
		if _, set := headers["X-CSRF-Token"]; !set && regexp.MustCompile(`(?i)csrf|xsrf`).MatchString(n["name"]) {
			headers["X-CSRF-Token"] = n["content"]
		}
	}
	return fields, headers
}

type part struct {
	name string
	data []byte
	kind string
}

func (b *browser) upload(slug, title, status string, files ...part) (*page, error) {
	b.t.Helper()
	fields, headers := b.form()
	fields.Set("title", title)
	fields.Set("slug", slug)
	fields.Set("status", status)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k := range fields {
		w.WriteField(k, fields.Get(k))
	}
	for _, f := range files {
		p, _ := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="` + f.name + `"`}, "Content-Type": {f.kind}})
		p.Write(f.data)
	}
	w.Close()
	headers["Content-Type"] = w.FormDataContentType()
	return b.send("POST", "/admin/reports", body.Bytes(), headers)
}

func (b *browser) publish(slug, title string, doc []byte, status string) {
	b.t.Helper()
	got, err := b.upload(slug, title, status, part{slug + ".html", doc, "text/html"})
	if err != nil {
		b.t.Fatalf("uploading %s (%s): %v", slug, status, err)
	}
	if got.status < 200 || got.status >= 400 {
		b.t.Fatalf("uploading %s (%s) answered %d: %.300s", slug, status, got.status, got.text)
	}
}

// node is an element of a page, or a run of its text (tag "").
type node struct {
	tag, data string
	attrs     map[string]string
	up        *node
	kids      []*node
}

var (
	junk   = regexp.MustCompile(`(?is)<!--.*?-->|<script\b.*?</script\s*>|<style\b.*?</style\s*>|<[!?][^>]*>`)
	pieces = regexp.MustCompile(`<(/?)([a-zA-Z][^\s/>]*)((?:[^>"']|"[^"]*"|'[^']*')*)>|[^<]+|<`)
	ends   = map[string]string{"li": " li ", "tr": " tr td th ", "td": " td th ", "th": " td th ", "p": " p "}
	empty  = " area base br col embed hr img input link meta source track wbr "
)

// dom is a page's elements as a tree, forgiving of the end tags HTML may leave out.
func dom(text string) *node {
	root := &node{tag: "#root"}
	cur := root
	for _, m := range pieces.FindAllStringSubmatch(junk.ReplaceAllString(text, " "), -1) {
		name := strings.ToLower(m[2])
		switch {
		case name == "":
			cur.kids = append(cur.kids, &node{data: html.UnescapeString(m[0]), up: cur})
		case m[1] == "/":
			for n := cur; n.up != nil; n = n.up {
				if n.tag == name {
					cur = n.up
					break
				}
			}
		default:
			for cur.up != nil && strings.Contains(ends[name], " "+cur.tag+" ") {
				cur = cur.up
			}
			n := &node{tag: name, attrs: map[string]string{}, up: cur}
			for _, a := range attrRE.FindAllStringSubmatch(m[3], -1) {
				n.attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2] + a[3] + a[4])
			}
			if cur.kids = append(cur.kids, n); !strings.HasSuffix(m[3], "/") && !strings.Contains(empty, " "+name+" ") {
				cur = n
			}
		}
	}
	return root
}

// all lists the elements under n with that tag, or every one for "*".
func (n *node) all(tag string) (out []*node) {
	for _, k := range n.kids {
		if k.tag != "" && (k.tag == tag || tag == "*") {
			out = append(out, k)
		}
		out = append(out, k.all(tag)...)
	}
	return out
}

func (n *node) within(tags string) *node {
	for p := n.up; p != nil; p = p.up {
		if strings.Contains(tags, " "+p.tag+" ") {
			return p
		}
	}
	return nil
}

// text is what an element reads as: its text, with each child's aria-label, title and text.
func (n *node) text() string {
	var out []string
	for _, k := range n.kids {
		if out = append(out, k.data); k.tag != "" {
			out = append(out, k.attrs["aria-label"], k.attrs["title"], k.text())
		}
	}
	return strings.Join(strings.Fields(strings.Join(out, " ")), " ")
}

func slugOf(a *node) string {
	if u, err := url.Parse(strings.TrimSpace(a.attrs["href"])); err == nil {
		if m := regexp.MustCompile(`^/reports/([^/]+)/?$`).FindStringSubmatch(u.EscapedPath()); m != nil {
			s, _ := url.PathUnescape(m[1])
			return s
		}
	}
	return ""
}

// listed reads the show-all page: report slugs in the order their links come, and each one's row:
// its tr, li or article, else the largest element around its link that links no other report.
func listed(pg *page) (order []string, rows map[string]*node) {
	rows = map[string]*node{}
	for _, a := range dom(pg.text).all("a") {
		slug := slugOf(a)
		alone := func(n *node) bool {
			for _, x := range n.all("a") {
				if s := slugOf(x); s != "" && s != slug {
					return false
				}
			}
			return true
		}
		if slug == "" || rows[slug] != nil {
			continue
		}
		box := a.within(" tr li article ")
		if box == nil || !alone(box) {
			for box = a; box.up.tag != "#root" && alone(box.up); box = box.up {
			}
		}
		order, rows[slug] = append(order, slug), box
	}
	return order, rows
}

var (
	dates = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:[t ][\d:.]+(?:z|[+-][\d:]+)?)?` +
		`|\b\d{1,2}(?:st|nd|rd|th)?\s+(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?,?\s+\d{4}` +
		`|\b(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+\d{1,2}(?:st|nd|rd|th)?,?\s+\d{4}` +
		`|\b\d{1,2}[/.]\d{1,2}[/.]\d{2,4}\b|\b\d{1,2}:\d{2}(?::\d{2})?\b`)
	icons  = map[rune]int{0x1F44D: 1, 0x1F4AC: 2, 0x1F441: 3}
	labels = map[string]int{"like": 1, "likes": 1, "comment": 2, "comments": 2, "times opened": 3, "opened": 3, "open": 3, "opens": 3, "view": 3, "views": 3}
)

func init() {
	for _, w := range []string{"thumbup", "thumbups", "thumbsup", "thumbsups"} {
		labels[w], labels[strings.Replace(w, "up", " up", 1)], labels[strings.Replace(w, "up", "-up", 1)] = 1, 1, 1
	}
}

// rowText is a row's text; in a table, each cell after its column's heading.
func rowText(row *node) string {
	cells := func(tr *node, heads bool) (out []string) {
		for _, c := range tr.kids {
			if heads && c.tag != "" && c.tag != "th" {
				return nil
			} else if c.tag == "td" || c.tag == "th" {
				out = append(out, c.text())
			}
		}
		return out
	}
	if table := row.within(" table "); row.tag == "tr" && table != nil {
		for _, tr := range table.all("tr") {
			if heads, mine := cells(tr, true), cells(row, false); tr != row && len(heads) > 0 {
				if len(heads) != len(mine) {
					break
				}
				for i := range mine {
					mine[i] = heads[i] + " " + mine[i]
				}
				return strings.Join(mine, " ; ")
			}
		}
	}
	return row.text()
}

func letter(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) && !unicode.IsDigit(r) }

// counts reads a row's likes, visible comments and times opened as a person reads them: each
// label's number beside it, on the same side for every label where that reads.
func counts(t *testing.T, row *node) [3]int {
	t.Helper()
	text := rowText(row)
	rs := []rune(dates.ReplaceAllString(strings.ToLower(text), " ") + "  ")
	type word struct{ label, n, at int }
	var ws []word // label 1, 2, 3 for likes, comments, times opened; -1 a number; 0 any other word
	end := func(i int) int {
		for letter(rs[i]) {
			i++
		}
		return i
	}
	for i := 0; i < len(rs)-2; {
		w, j := word{icons[rs[i]], 0, len(ws)}, i+1
		switch {
		case w.label > 0:
			if i > 0 && letter(rs[i-1]) || letter(rs[j]) {
				w.label = -2
			}
		case rs[i] >= '0' && rs[i] <= '9':
			for w.label, j = -1, i; rs[j] >= '0' && rs[j] <= '9' || rs[j] == ','; j++ {
				if rs[j] != ',' {
					w.n = w.n*10 + int(rs[j]-'0')
				}
			}
		case letter(rs[i]):
			j = end(i)
			w.label = labels[string(rs[i:j])]
			if k := end(j + 1); (rs[j] == ' ' || rs[j] == '-') && k > j+1 && labels[string(rs[i:k])] > 0 {
				w.label, j = labels[string(rs[i:k])], k
			}
		default:
			w.label = -2
		}
		if i = j; w.label != -2 {
			ws = append(ws, w)
		}
	}
	var near [4][]word
	for i, w := range ws {
		for _, j := range []int{i - 1, i + 1} {
			if w.label > 0 && j >= 0 && j < len(ws) && ws[j].label == -1 {
				near[w.label] = append(near[w.label], word{j - i, ws[j].n, j})
			}
		}
	}
	found := map[[3]int]bool{}
	for even := 1; even >= 0 && len(found) == 0; even-- {
		for _, a := range near[1] {
			for _, b := range near[2] {
				for _, c := range near[3] {
					if a.at != b.at && a.at != c.at && b.at != c.at && (even == 0 || a.label == b.label && b.label == c.label) {
						found[[3]int{a.n, b.n, c.n}] = true
					}
				}
			}
		}
	}
	for f := range found {
		if len(found) == 1 {
			return f
		}
	}
	t.Fatalf("this row's likes, comments and times opened cannot be read apart: %q", text)
	return [3]int{}
}

// open is what a browser does to open a report: the page, then what its iframes load.
func (b *browser) open(path string, must bool) {
	b.t.Helper()
	if pg := b.get(path); pg.status == 200 {
		b.frames(pg)
	} else if must {
		b.t.Fatalf("%s answered %d", path, pg.status)
	}
}

// numbers is the likes, visible comments and times opened the show-all page shows a visitor for one report.
func (a *app) numbers(slug string) [3]int {
	a.t.Helper()
	pg := a.browser("192.0.2.99", chrome).get("/reports")
	if _, rows := listed(pg); pg.status == 200 && rows[slug] != nil {
		return counts(a.t, rows[slug])
	}
	a.t.Fatalf("GET /reports answered %d, with no row linking to /reports/%s", pg.status, slug)
	return [3]int{}
}

func TestE002OpensCountedOnce(t *testing.T) {
	a := start(t, 10<<20)
	own, _ := a.signIn(owner, "192.0.2.10")
	own.publish("e002-counted-report", "Counted Survey", report("Counted Survey", mark("COUNTED"), ""), "published")
	time.Sleep(1200 * time.Millisecond)
	own.publish("e002-other-report", "Other Survey", report("Other Survey", mark("OTHER"), ""), "published")
	path, reader := "/reports/e002-counted-report", a.browser("203.0.113.10", chrome)
	for _, step := range []struct {
		why   string
		opens int
		do    func()
	}{
		{"a report nobody opened shows opens", 0, func() {}},
		{"opening the report did not add one to its count", 1, func() { reader.open(path, true) }},
		{"the same visitor opening it again the same day added to it", 1, func() { reader.open(path, true); reader.open(path, true) }},
		{"a crawler's request added to the count", 1, func() {
			a.browser("203.0.113.20", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)").open(path, false)
			a.browser("203.0.113.21", "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)").open(path, false)
		}},
		{"another visitor's open did not add one", 2, func() {
			a.browser("203.0.113.30", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0").open(path, true)
		}},
	} {
		if step.do(); a.numbers("e002-counted-report")[2] != step.opens {
			t.Fatalf("%s: its row shows %d times opened, not %d", step.why, a.numbers("e002-counted-report")[2], step.opens)
		}
	}
	if n := a.numbers("e002-other-report")[2]; n != 0 {
		t.Fatalf("opening one report added to another report's count: it shows %d", n)
	}
}
