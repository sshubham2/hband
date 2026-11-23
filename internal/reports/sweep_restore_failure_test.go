package reports

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// m10: when a restart finds tmp/<id>.old holding the bytes the row describes
// but cannot put it back (here: the file in place is held open, so Windows
// refuses the rename), the sweep must not then delete that set-aside file. It
// is the only copy of the report the row describes.
func TestOpenKeepsTheOnlyCopyWhenRestoreFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("holding the target open makes the rename fail only on Windows")
	}
	r := newRig(t)
	v1, v2 := htmlOf(300, "v1"), htmlOf(400, "v2")
	rep := r.mustSave(goodForm("crash", "Crash", "published", v1))
	// A crash after the new file was renamed in but before the commit: the row
	// still describes v1, v1 is set aside, v2 is in place.
	must(t, os.WriteFile(r.lib.setAsidePath(rep.ID), v1, 0o600))
	must(t, os.WriteFile(r.lib.path(rep.ID), v2, 0o600))

	held, err := os.Open(r.lib.path(rep.ID))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = Open(Options{DB: r.st.DB(), DataDir: r.root, MaxUploadBytes: testCap, Now: r.clock.Now})
	held.Close()

	cur, _ := os.ReadFile(r.lib.path(rep.ID))
	old, _ := os.ReadFile(r.lib.setAsidePath(rep.ID))
	if !bytes.Equal(cur, v1) && !bytes.Equal(old, v1) {
		t.Fatal("the sweep deleted tmp/<id>.old after failing to restore it: the bytes the row describes are gone")
	}
}

// A crash while the canonical path is empty leaves the only copy of the bytes
// the row describes in tmp/<id>.old. If the restart cannot put it back, it must
// not delete it either.
func TestSweepKeepsASetAsideFileItCouldNotRestore(t *testing.T) {
	r := newRig(t)
	v1 := htmlOf(300, "v1")
	rep := r.mustSave(goodForm("crash", "Crash", "published", v1))
	old, cur := r.lib.setAsidePath(rep.ID), r.lib.path(rep.ID)

	// The replace set v1 aside and stopped: the row still says v1.
	if err := os.Rename(cur, old); err != nil {
		t.Fatal(err)
	}
	// Something at the canonical path makes the restoring rename fail.
	if err := os.MkdirAll(filepath.Join(cur, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Restart. Refusing to start is acceptable; deleting the only copy is not.
	_, _ = Open(Options{DB: r.st.DB(), DataDir: r.root, MaxUploadBytes: testCap, Now: r.clock.Now})

	for _, p := range []string{old, cur} {
		if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, v1) {
			return
		}
	}
	t.Fatal("the only copy of the report's bytes was deleted after the restore failed")
}
