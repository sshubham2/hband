package votes

import (
	"strings"
	"testing"
)

// TestPublicLikes: the one read behind the show-all page counts thumbs up and only
// those, keeps a blocked account's like, and for every report says what Public
// says of it.
func TestPublicLikes(t *testing.T) {
	b, st := book(t)
	for _, id := range []int64{7, 8, 9, 10} {
		addUser(t, st, id)
	}
	onlyDislikes := addReport(t, st, "dislikes-only", "published")
	switched := addReport(t, st, "switched", "published")
	blocked := addReport(t, st, "blocked-like", "published")
	plain := addReport(t, st, "plain", "published")
	none := addReport(t, st, "none", "published")
	draft := addReport(t, st, "draft", "draft")

	for _, u := range []int64{7, 8, 9} {
		if err := b.Cast(bg, onlyDislikes, u, Down); err != nil {
			t.Fatal(err)
		}
	}
	// A like that becomes a dislike is no like.
	if err := b.Cast(bg, switched, 7, Up); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, switched, 7, Down); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, switched, 8, Up); err != nil {
		t.Fatal(err)
	}
	// A like by an account that is blocted afterwards stays as it was, and counts.
	if err := b.Cast(bg, blocked, 9, Up); err != nil {
		t.Fatal(err)
	}
	if err := b.Cast(bg, blocked, 10, Down); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO blocked_accounts (github_id, blocked_at) VALUES (9, 5)`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []int64{7, 8, 9} {
		if err := b.Cast(bg, plain, u, Up); err != nil && u != 9 {
			t.Fatal(err)
		}
	}
	if err := b.Cast(bg, draft, 7, Up); err == nil {
		// A draft takes no vote at all: nothing to count for it.
		t.Fatal("a draft took a vote")
	}

	got, err := b.PublicLikes(bg)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		id   int64
		want int
	}{
		"only dislikes":        {onlyDislikes, 0},
		"a like switched":      {switched, 1},
		"a blocked account's":  {blocked, 1},
		"plain (9 is blocked)": {plain, 2},
		"no votes":             {none, 0},
		"a draft":              {draft, 0},
	} {
		if got[c.id] != c.want {
			t.Errorf("%s: %d likes, want %d", name, got[c.id], c.want)
		}
		if pub := public(t, b, c.id, 0); got[c.id] != pub.Likes {
			t.Errorf("%s: PublicLikes %d, Public %d", name, got[c.id], pub.Likes)
		}
	}
	if _, present := got[onlyDislikes]; present {
		t.Error("a report with only dislikes is in the map: a dislike must leave no trace in what the public reads")
	}
	total := 0
	for _, n := range got {
		total += n
	}
	if total != 1+1+2 {
		t.Errorf("%d likes in all, want 4", total)
	}

	t.Run("an error is never a partial map", func(t *testing.T) {
		if _, err := st.DB().Exec(`ALTER TABLE votes RENAME TO votes_gone`); err != nil {
			t.Fatal(err)
		}
		m, err := b.PublicLikes(bg)
		if err == nil || m != nil || !strings.HasPrefix(err.Error(), "votes: likes:") {
			t.Fatalf("PublicLikes on a missing table = %v, %v; want a nil map and a 'votes: likes:' error", m, err)
		}
	})
}
