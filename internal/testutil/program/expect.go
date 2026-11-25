package program

import (
	"crypto/rand"
	"fmt"
	"reflect"
	"strings"

	"github.com/sshubham2/hband/internal/testutil/fakegithub"
)

// Expect fails t with the message unless ok.
func Expect(t T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

// ExpectEq fails t, with the message and both values, unless got equals want.
func ExpectEq(t T, got, want any, format string, args ...any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", fmt.Sprintf(format, args...), got, want)
	}
}

// Mark is a word and ten random letters, with no digit, so that no count is ever
// read into it.
func Mark(word string) string {
	const letters = "bcdfghjkmnpqrstvwxz"
	b := make([]byte, 10)
	rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return word + "-" + string(b)
}

// Account is a GitHub account: user ID, login (a random lower-case one when
// login is empty) and avatar address.
func Account(id int64, login string) fakegithub.User {
	if login == "" {
		login = strings.ToLower(Mark("reader"))
	}
	return fakegithub.User{ID: id, Login: login, AvatarURL: fmt.Sprintf("https://avatars.example/u/%d?v=4", id)}
}

// Report is a small report's HTML.
func Report(title string) string {
	return "<!doctype html><html><head><title>" + title + "</title></head><body><h1>" + title + "</h1></body></html>"
}

// Resolve resolves ref against base the way a browser does, keeping a fragment.
func Resolve(base, ref string) string { return resolveRef(base, ref) }
