package reports

import (
	"testing"
	"time"
)

// TestListPublished: the show-all list holds the published reports and no other,
// newest publish date first, and among reports published in one second the higher
// id first, which is the order Latest takes its row from.
func TestListPublished(t *testing.T) {
	r := newRig(t)
	if got, err := r.lib.ListPublished(bg); err != nil || len(got) != 0 {
		t.Fatalf("ListPublished of an empty library = %v, %v", got, err)
	}

	r.mustSave(goodForm("oldest", "Oldest", "published", htmlOf(100, "a")))
	r.clock.Advance(time.Hour)
	// Two reports published in the same second: only the id tells them apart.
	r.mustSave(goodForm("same-a", "Same A", "published", htmlOf(100, "b")))
	r.mustSave(goodForm("same-b", "Same B", "published", htmlOf(100, "c")))
	r.clock.Advance(time.Hour)
	r.mustSave(goodForm("draft", "A draft", "draft", htmlOf(100, "d")))
	r.mustSave(goodForm("unpublished", "Unpublished later", "published", htmlOf(100, "e")))
	r.mustSave(goodForm("deleted", "Deleted later", "published", htmlOf(100, "f")))
	r.clock.Advance(time.Hour)
	r.mustSave(goodForm("newest", "Newest", "published", htmlOf(100, "g")))
	must(t, r.lib.Unpublish(bg, "unpublished"))
	must(t, r.lib.Delete(bg, "deleted"))

	got, err := r.lib.ListPublished(bg)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, rep := range got {
		slugs = append(slugs, rep.Slug)
		if !rep.Published() {
			t.Errorf("%s is listed with status %q", rep.Slug, rep.Status)
		}
	}
	want := []string{"newest", "same-b", "same-a", "oldest"}
	if len(slugs) != len(want) {
		t.Fatalf("ListPublished = %v, want %v", slugs, want)
	}
	for i := range want {
		if slugs[i] != want[i] {
			t.Fatalf("ListPublished = %v, want %v", slugs, want)
		}
	}
	// The first row is the one Latest answers, and the dates never increase down the list.
	if latest, err := r.lib.Latest(bg); err != nil || latest.ID != got[0].ID {
		t.Fatalf("Latest = %+v %v, but the list starts with %s", latest, err, got[0].Slug)
	}
	for i := 1; i < len(got); i++ {
		if got[i].PublishedAt.After(got[i-1].PublishedAt) {
			t.Errorf("%s (%s) is listed after %s (%s)", got[i].Slug, got[i].PublishedAt, got[i-1].Slug, got[i-1].PublishedAt)
		}
	}
	if !got[1].PublishedAt.Equal(got[2].PublishedAt) || got[1].ID <= got[2].ID {
		t.Errorf("same-b and same-a must share a second and differ by id: %v %d, %v %d", got[1].PublishedAt, got[1].ID, got[2].PublishedAt, got[2].ID)
	}

	// Unpublishing keeps the date, and republishing returns the report to its place.
	must(t, r.lib.Unpublish(bg, "oldest"))
	must(t, r.lib.Publish(bg, "oldest"))
	if got, err = r.lib.ListPublished(bg); err != nil || len(got) != 4 || got[3].Slug != "oldest" {
		t.Fatalf("after unpublishing and republishing the oldest: %v %v", got, err)
	}

	// A read that fails is an error, never an empty list.
	if _, err := r.st.DB().Exec(`ALTER TABLE reports RENAME TO reports_gone`); err != nil {
		t.Fatal(err)
	}
	if got, err := r.lib.ListPublished(bg); err == nil || got != nil {
		t.Fatalf("ListPublished on a missing table = %v, %v; want an error and no list", got, err)
	}
}
