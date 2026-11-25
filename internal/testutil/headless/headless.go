// Package headless drives a locally installed Chromium-family browser (Chrome;
// never Microsoft Edge), for checks that need a real browser, in two ways. Shot
// loads a URL headless through the browser's own command line, takes its screenshot
// and returns it as an image, read with image/png. Open starts a browser of the
// test's own and returns a Tab, which a stdlib-only DevTools client drives with four
// verbs (Call, Viewport, Navigate, Eval): it reaches widths below the command line's
// window floor of about 492 CSS px, and it clicks. There is no browser-automation
// dependency.
//
// It is test-only. internal/testutil is kept out of the binary by the rule
// that production code never imports it, and nothing here is read by
// config.Load.
//
// Environment, read only here:
//
//	CHROME_BIN            the browser to run, and the only place its path comes
//	                      from: no install path is written in this package and
//	                      no other variable stands in for it. When it is unset,
//	                      or names no existing file, the calling test skips and
//	                      says so, so a machine with no browser still passes.
//	                      Edge is refused even when named: a new Edge profile
//	                      signs in to the Windows Microsoft account and imports
//	                      the person's old browsing history at its first run.
//	                      The refusal is by name, on the text as written, on its
//	                      absolute form and on what its symbolic links resolve
//	                      to. A hard link, a renamed copy or a launcher shim that
//	                      starts Edge is out of scope: CHROME_BIN is set only by
//	                      the person, who would have to point it there.
//	HBAND_TEST_ARTIFACTS  a folder; every screenshot is also copied there as
//	                      <name>.png (any run of characters in name other than
//	                      letters, digits, '.', '_' and '-' becomes one '_'),
//	                      for a person to look at. It never picks a browser.
package headless

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sshubham2/hband/internal/testutil/outside"
)

// shotTimeout bounds one browser run.
const shotTimeout = 60 * time.Second

// Browser returns the browser to drive: exactly the path in CHROME_BIN, when it
// names an existing file that is not Microsoft Edge. In every other case it
// skips the calling test with a message that names CHROME_BIN, and returns "".
func Browser(t testing.TB) string {
	t.Helper()
	p := os.Getenv("CHROME_BIN")
	if p == "" {
		t.Skip("CHROME_BIN is not set: set it to the path of a Chrome executable (not Edge) to run this check")
		return ""
	}
	if isEdge(p) {
		t.Skipf("CHROME_BIN=%q is Microsoft Edge, which is never started (its first run signs in to the Windows Microsoft account and imports old browsing history): set CHROME_BIN to a Chrome executable", p)
		return ""
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Skipf("CHROME_BIN=%q names no file (%v): set it to the path of a Chrome executable to run this check", p, err)
		return ""
	}
	if !info.Mode().IsRegular() {
		t.Skipf("CHROME_BIN=%q is not a file: set it to the path of a Chrome executable to run this check", p)
		return ""
	}
	return p
}

// isEdge reports whether the path names Microsoft Edge's executable, as it is
// called on Windows (msedge.exe) and in Linux packages (microsoft-edge). It is
// a decision by name, taken before the path is looked up.
//
// The last text element of the path is not always the file the OS opens: on
// Windows msedge.exe\. , msedge.exe\x\.. , msedge.exe/. and "msedge.exe \."
// all open msedge.exe, and "msedge.exe." and "msedge.exe " lose their trailing
// dot or space. So the name is taken twice, from the text as written and from
// the path resolved the way the OS resolves it (filepath.Abs is
// GetFullPathName on Windows, which CreateFile and CreateProcess apply too),
// and either one naming Edge refuses it. The caller keeps the CHROME_BIN text
// unchanged.
func isEdge(p string) bool {
	if namesEdge(p) {
		return true
	}
	if abs, err := filepath.Abs(p); err == nil && namesEdge(abs) {
		return true
	}
	if r, err := evalSymlinks(p); err == nil && namesEdge(r) {
		return true
	}
	return false
}

// evalSymlinks resolves the symbolic links in a path: a CHROME_BIN called chrome
// that is a link to msedge.exe is Edge. A package variable so a test can stand in
// a resolver on a machine that may not create symbolic links.
var evalSymlinks = filepath.EvalSymlinks

// namesEdge reports whether the last element of p is an Edge executable name.
func namesEdge(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	return strings.Contains(base, "msedge") || strings.Contains(base, "microsoft-edge")
}

// Shot loads pageURL in a fresh headless browser process, with a fresh
// profile, and returns the browser's own screenshot of it. name labels the
// copy written under HBAND_TEST_ARTIFACTS. extraFlags are added before the
// URL. A failed launch, a timeout (the process is killed; only the one this
// call started) or an unreadable PNG fails the test.
func Shot(t testing.TB, browser, name, pageURL string, extraFlags ...string) image.Image {
	t.Helper()
	// Not t.TempDir: its cleanup fails the test when a browser child still
	// holds the profile open, and a leftover profile is only worth a log line.
	profile, err := os.MkdirTemp("", "hband-headless-profile-*")
	if err != nil {
		t.Fatalf("headless: profile folder: %v", err)
	}
	outDir, err := os.MkdirTemp("", "hband-headless-shot-*")
	if err != nil {
		t.Fatalf("headless: screenshot folder: %v", err)
	}
	t.Cleanup(func() {
		removeLoud(t, profile)
		removeLoud(t, outDir)
	})
	outside.MustBeOutside(t, profile)
	outside.MustBeOutside(t, outDir)
	out := filepath.Join(outDir, "shot.png")

	args := []string{
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--hide-scrollbars",
		"--user-data-dir=" + profile,
		"--window-size=1024,1400",
		"--virtual-time-budget=5000",
		"--screenshot=" + out,
	}
	args = append(append(args, extraFlags...), pageURL)

	ctx, cancel := context.WithTimeout(context.Background(), shotTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, args...)
	// After a kill, do not wait forever on a child that still holds a pipe.
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("headless %s: the browser did not finish within %s and was killed: %v", name, shotTimeout, err)
		}
		t.Fatalf("headless %s: the browser failed: %v\n%s", name, err, tail(stderr.String()))
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("headless %s: the browser exited without writing a screenshot: %v\n%s", name, err, tail(stderr.String()))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("headless %s: the screenshot is not a PNG: %v", name, err)
	}
	if dir := os.Getenv("HBAND_TEST_ARTIFACTS"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			err = os.WriteFile(filepath.Join(dir, safeName(name)+".png"), data, 0o644)
		}
		if err != nil {
			t.Logf("headless %s: could not keep the screenshot under HBAND_TEST_ARTIFACTS: %v", name, err)
		}
	}
	return img
}

// Count returns how many pixels of img are within tol of c on every channel.
func Count(img image.Image, c color.RGBA, tol uint8) int {
	near := func(got uint32, want uint8) bool {
		g := int(got >> 8)
		d := g - int(want)
		if d < 0 {
			d = -d
		}
		return d <= int(tol)
	}
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if near(r, c.R) && near(g, c.G) && near(bl, c.B) {
				n++
			}
		}
	}
	return n
}

// removeLoud deletes a folder with a few retries (a browser child can hold a
// file for a moment after the process is gone) and logs, never fails, when it
// cannot.
func removeLoud(t testing.TB, dir string) {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("headless: left %s behind: %v", dir, err)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string { return unsafeName.ReplaceAllString(s, "_") }

// tail keeps the end of a long stderr.
func tail(s string) string {
	const max = 2000
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}
