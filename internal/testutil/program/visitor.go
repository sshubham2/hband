package program

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
)

// Page is one answer, as it came: nothing in it has been followed.
type Page struct {
	Status   int
	Header   http.Header
	Body     string
	Location string // the Location header as sent, possibly relative
	URL      string // the URL asked for, fragment included
}

// Doc is the page's body as an element tree.
func (pg Page) Doc() *Node { return Dom(pg.Body) }

// Visitor is a browser-like client of one Program: a cookie jar, and every
// answer it got in Seen. It never writes a cookie itself; the session comes only
// from the OAuth round trip through the stand-in GitHub.
type Visitor struct {
	P     *Program
	Login string // who SignIn signed in; "" for an anonymous visitor
	Seen  []Page

	client *http.Client
	jar    *cookiejar.Jar
}

// rewrite sends requests for the public host to the program's real listener and
// puts the original request back on the response, so the cookie jar keeps the
// https URL (and with it the Secure cookies). Every other address passes
// straight through, and no proxy is ever used.
type rewrite struct {
	addr string
	next *http.Transport
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != publicHost {
		return r.next.RoundTrip(req)
	}
	c := req.Clone(req.Context())
	c.URL.Scheme, c.URL.Host = "http", r.addr
	c.Host = publicHost
	resp, err := r.next.RoundTrip(c)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

// Anonymous is a visitor who is not signed in.
func (p *Program) Anonymous() *Visitor {
	jar, _ := cookiejar.New(nil)
	next := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	var rt http.RoundTripper = next
	if !p.loopback {
		rt = rewrite{addr: p.Addr, next: next}
	}
	return &Visitor{P: p, jar: jar, client: &http.Client{
		Transport: rt, Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// SignIn signs a visitor in as u through the stand-in GitHub: it follows the
// program's own redirect to GitHub's authorize page, and back. It fails t unless
// the visitor lands on a page below 400 holding the program's session cookie.
func (p *Program) SignIn(t T, u fakegithub.User) *Visitor {
	t.Helper()
	if u.AvatarURL == "" {
		u.AvatarURL = "https://avatars.example/u/" + itoa(u.ID) + "?v=4"
	}
	v := p.Anonymous()
	v.Login = u.Login
	p.Fake.SetUser(u)
	// ported:E3-226,E41-222,E42-224,E5-224,E61-224,E62-225
	got := p.Approve(t, v, v.Get(t, "/auth/github/login"))
	// ported:E3-227,E41-223,E42-225,E5-225,E61-225,E62-226
	if got.Status >= 400 {
		t.Fatalf("program: signing in as %s landed on %d: %.300s", u.Login, got.Status, got.Body)
		return nil
	}
	base, _ := url.Parse(p.Base)
	has := false
	for _, c := range v.jar.Cookies(base) {
		if strings.HasSuffix(c.Name, "hband_sid") {
			has = true
		}
	}
	if !has {
		t.Fatalf("program: signing in as %s left no session cookie in the jar (status %d)", u.Login, got.Status)
	}
	return v
}

// Owner signs in as the owner the program pins.
func (p *Program) Owner(t T) *Visitor {
	t.Helper()
	return p.SignIn(t, fakegithub.User{ID: OwnerID, Login: "report-author"})
}

// Approve is GitHub's half of a sign-in that start began: start must be a
// redirect to the stand-in's authorize page; the stand-in approves whoever it
// was last told to, and the visitor is followed back, through every redirect,
// to where it lands.
func (p *Program) Approve(t T, v *Visitor, start Page) Page {
	t.Helper()
	// ported:E3-233,E41-229,E42-231,E5-231,E61-231,E62-232
	if !(start.Status >= 300 && start.Status < 400 && strings.HasPrefix(start.Location, p.Fake.URL()+"/login/oauth/authorize")) {
		t.Fatalf("program: %s answered %d %q, not a redirect to GitHub's authorize page", start.URL, start.Status, start.Location)
		return Page{}
	}
	pg := start
	for i := 0; i < 10 && pg.Status >= 300 && pg.Status < 400 && pg.Location != ""; i++ {
		pg = v.Get(t, resolveRef(pg.URL, pg.Location))
	}
	return pg
}

// Request builds a request for path (or a full URL) on the program's base.
func (v *Visitor) Request(method, path string, body io.Reader) *http.Request {
	req, _ := http.NewRequest(method, resolveRef(v.P.Base+"/", path), body)
	return req
}

// Do sends req and returns the answer. It never follows a redirect.
func (v *Visitor) Do(t T, req *http.Request) Page {
	t.Helper()
	resp, err := v.client.Do(req)
	if err != nil {
		t.Fatalf("program: %s %s: %v", req.Method, req.URL, err)
		return Page{}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	pg := Page{Status: resp.StatusCode, Header: resp.Header, Body: string(body), Location: resp.Header.Get("Location"), URL: req.URL.String()}
	v.Seen = append(v.Seen, pg)
	return pg
}

// Get asks for path and returns the answer as it came, redirect and all.
func (v *Visitor) Get(t T, path string) Page {
	t.Helper()
	return v.Do(t, v.Request(http.MethodGet, path, nil))
}

// Post sends a urlencoded form and returns the answer as it came.
func (v *Visitor) Post(t T, path string, form url.Values) Page {
	t.Helper()
	req := v.Request(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return v.Do(t, req)
}

// Upload sends a multipart form: the fields, and one file part of type text/html.
func (v *Visitor) Upload(t T, path string, fields map[string]string, fileField, fileName string, data []byte) Page {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, val := range fields {
		w.WriteField(k, val)
	}
	if fileField != "" {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+fileField+`"; filename="`+fileName+`"`)
		h.Set("Content-Type", "text/html")
		part, _ := w.CreatePart(h)
		part.Write(data)
	}
	w.Close()
	req := v.Request(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return v.Do(t, req)
}

// GetFollow asks for path and follows the program's own redirects (303 after a
// post, for one) to the page they end on. It follows only hops to the program's
// own host, port included, and fails t on a hop to the sign-in route: an
// anonymous visitor that followed it would be signed in by the stand-in, which
// approves everyone. A redirect to another address is returned as it came.
func (v *Visitor) GetFollow(t T, path string) Page {
	t.Helper()
	return v.follow(t, v.Get(t, path))
}

// PostFollow is Post followed the way GetFollow follows.
func (v *Visitor) PostFollow(t T, path string, form url.Values) Page {
	t.Helper()
	return v.follow(t, v.Post(t, path, form))
}

func (v *Visitor) follow(t T, pg Page) Page {
	t.Helper()
	base, _ := url.Parse(v.P.Base)
	for i := 0; i < 8; i++ {
		if pg.Status < 300 || pg.Status >= 400 || pg.Location == "" {
			return pg
		}
		to := resolveRef(pg.URL, pg.Location)
		u, err := url.Parse(to)
		if err != nil || u.Host != base.Host {
			return pg
		}
		if strings.HasPrefix(u.Path, "/auth/github/login") {
			t.Fatalf("program: %s redirects to the sign-in route %s; following it would sign the visitor in", pg.URL, to)
			return pg
		}
		pg = v.Get(t, to)
	}
	return pg
}

// Publish uploads html as the owner's published report, then waits just over a
// second so that the next report is plainly newer.
func (v *Visitor) Publish(t T, slug, title, html string) {
	t.Helper()
	got := v.Upload(t, "/admin/reports", map[string]string{"title": title, "slug": slug, "status": "published"},
		"file", slug+".html", []byte(html))
	// ported:E3-247,E41-243,E42-245,E5-245,E61-245,E62-246
	if got.Status != http.StatusSeeOther {
		t.Fatalf("program: publishing %s answered %d, want 303: %.300s", slug, got.Status, got.Body)
		return
	}
	time.Sleep(1100 * time.Millisecond)
}

// resolveRef resolves ref against base the way a browser does, keeping any
// fragment.
func resolveRef(base, ref string) string {
	b, err := url.Parse(base)
	r, err2 := url.Parse(strings.TrimSpace(ref))
	if err != nil || err2 != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
