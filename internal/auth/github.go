package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"golang.org/x/oauth2"

	"github.com/sshubham2/hband/internal/store"
)

func (a *Auth) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.cfg.ClientID,
		ClientSecret: a.cfg.ClientSecret,
		RedirectURL:  a.cfg.CallbackURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:   a.cfg.GitHub.AuthURL,
			TokenURL:  a.cfg.GitHub.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
		// No scopes: the signed-in account's public profile is all that is read.
	}
}

// exchange trades the authorization code for an access token. The token is
// used once, by the caller, and dropped.
func (a *Auth) exchange(ctx context.Context, code, verifier string) (string, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, a.client)
	tok, err := a.oauthConfig().Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("token exchange failed")
	}
	if tok.AccessToken == "" {
		return "", errors.New("token exchange returned no access token")
	}
	return tok.AccessToken, nil
}

var rawIntegerID = regexp.MustCompile(`^[0-9]{1,19}$`)

// fetchUser asks GitHub who the token belongs to. The id must be a raw JSON
// integer greater than zero (a string, a float, a missing or a zero id is
// refused) and the login must be non-empty.
func (a *Auth) fetchUser(ctx context.Context, accessToken string) (store.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.GitHub.UserURL, nil)
	if err != nil {
		return store.User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hband")
	resp, err := a.client.Do(req)
	if err != nil {
		return store.User{}, errors.New("user request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return store.User{}, fmt.Errorf("user endpoint answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return store.User{}, errors.New("user response unreadable")
	}
	var raw struct {
		ID        json.RawMessage `json:"id"`
		Login     string          `json:"login"`
		AvatarURL string          `json:"avatar_url"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return store.User{}, errors.New("user response is not the expected JSON")
	}
	if !rawIntegerID.Match(raw.ID) {
		return store.User{}, errors.New("user id is not a JSON integer")
	}
	id, err := strconv.ParseInt(string(raw.ID), 10, 64)
	if err != nil || id <= 0 {
		return store.User{}, errors.New("user id is not a positive integer")
	}
	if raw.Login == "" {
		return store.User{}, errors.New("user login is empty")
	}
	return store.User{GitHubID: id, Login: raw.Login, AvatarURL: safeAvatar(raw.AvatarURL)}, nil
}

// safeAvatar keeps only an https avatar URL; anything else is stored empty.
func safeAvatar(s string) string {
	if len(s) > 512 {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return s
}
