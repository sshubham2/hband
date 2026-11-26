package fakegithub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A picked account keeps its own id under the id-shape switches: /user answers
// the picked choice's id as a string or a float, never the id SetUser named.
func TestPickedUserKeepsItsIDUnderIDShapeSwitches(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*Switches)
		want any
	}{
		{"string", func(s *Switches) { s.UserIDString = true }, "4242"},
		{"float", func(s *Switches) { s.UserIDFloat = true }, float64(4242)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := New(cid, secret, redirect)
			defer f.Close()
			f.OfferChoices(Choice{Label: "Continue as the owner", User: User{ID: 4242, Login: "report-author"}})
			f.Configure(tc.set)
			q := authQuery(func(q url.Values) { q.Set("pick", "0") })
			resp, err := noFollow().Get(f.URL() + "/login/oauth/authorize?" + q.Encode())
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			back, _ := url.Parse(resp.Header.Get("Location"))
			code := back.Query().Get("code")
			if resp.StatusCode != http.StatusFound || code == "" {
				t.Fatalf("pick=0 answered %d %q, want a 302 with a code", resp.StatusCode, resp.Header.Get("Location"))
			}
			form := url.Values{"client_id": {cid}, "client_secret": {secret}, "redirect_uri": {redirect}, "code": {code}, "code_verifier": {"verifier"}}
			req, _ := http.NewRequest(http.MethodPost, f.URL()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			tr, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var tok struct {
				AccessToken string `json:"access_token"`
			}
			json.NewDecoder(tr.Body).Decode(&tok)
			tr.Body.Close()
			if tok.AccessToken == "" {
				t.Fatal("no token")
			}
			ureq, _ := http.NewRequest(http.MethodGet, f.URL()+"/user", nil)
			ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
			ur, err := http.DefaultClient.Do(ureq)
			if err != nil {
				t.Fatal(err)
			}
			defer ur.Body.Close()
			var who map[string]any
			if err := json.NewDecoder(ur.Body).Decode(&who); err != nil {
				t.Fatal(err)
			}
			if who["id"] != tc.want || who["login"] != "report-author" {
				t.Fatalf("/user for the picked owner answered id %#v login %#v, want id %#v login report-author", who["id"], who["login"], tc.want)
			}
		})
	}
}
