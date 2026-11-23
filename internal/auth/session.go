package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"time"

	"github.com/sshubham2/hband/internal/config"
	"github.com/sshubham2/hband/internal/store"
)

const sessionTTL = 30 * 24 * time.Hour

// tokenPattern is the exact shape of a session token: 32 random bytes as 43
// base64url characters. Anything else is anonymous without a database call.
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// Auth holds what the sign-in handlers and the identity middleware share.
type Auth struct {
	cfg    config.Config
	store  *store.Store
	now    func() time.Time
	client *http.Client
}

// New builds the identity block. now is the clock (injected by tests).
func New(cfg config.Config, st *store.Store, now func() time.Time) *Auth {
	return &Auth{
		cfg:   cfg,
		store: st,
		now:   now,
		client: &http.Client{
			Timeout: 10 * time.Second,
			// A redirect from the identity provider is a failure, not a hop.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Viewer is who the current request is, when anybody. IsOwner is computed
// from the pinned ID on every request and never stored.
type Viewer struct {
	GitHubID  int64
	Login     string
	AvatarURL string
	IsOwner   bool
}

type (
	viewerKey      struct{}
	identifyErrKey struct{}
)

// ViewerFrom is the read API for later blocks: ok is false for anonymous
// visitors.
func ViewerFrom(ctx context.Context) (Viewer, bool) {
	v, ok := ctx.Value(viewerKey{}).(Viewer)
	return v, ok
}

func identifyErrFrom(ctx context.Context) error {
	err, _ := ctx.Value(identifyErrKey{}).(error)
	return err
}

// Identify resolves the session cookie on every route. A cookie that does not
// match the token grammar is anonymous with no database call. A store error
// is recorded, not hidden: public pages render as anonymous and the owner
// gate denies.
func (a *Auth) Identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if c, err := r.Cookie(cookieName(a.cfg, sidBase)); err == nil && tokenPattern.MatchString(c.Value) {
			sess, ok, err := a.store.LookupSession(ctx, hashToken(c.Value), a.now())
			switch {
			case err != nil:
				ctx = context.WithValue(ctx, identifyErrKey{}, err)
			case ok:
				ctx = context.WithValue(ctx, viewerKey{}, Viewer{
					GitHubID:  sess.User.GitHubID,
					Login:     sess.User.Login,
					AvatarURL: sess.User.AvatarURL,
					IsOwner:   IsOwner(a.cfg.OwnerGitHubID, sess.User.GitHubID),
				})
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newToken returns 32 crypto/rand bytes as 43 base64url characters.
func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// hashToken is the database key of a session token: hex(sha256(token)).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
