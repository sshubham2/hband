// Package headless drives the locally installed Chromium-family browser
// (Chrome by default; never Microsoft Edge unless HBAND_TEST_BROWSER names it)
// through its own command line, for checks that need
// a real browser: it loads a URL headless, takes the browser's own screenshot
// and returns it as an image. There is no browser-automation dependency and no
// DevTools client; the screenshot is read with image/png.
//
// It is test-only. internal/testutil is kept out of the binary by the rule
// that production code never imports it, and nothing here is read by
// config.Load.
//
// Environment, read only here:
//
//	HBAND_TEST_BROWSER    the browser to run. When set, the check must run: a
//	                      missing browser or a failed launch fails the test, it
//	                      never skips. When unset, the first Chrome install that
//	                      exists is used, and otherwise the test skips. Edge is
//	                      never a default: a new Edge profile signs in to the
//	                      Windows Microsoft account and imports the person's old
//	                      browsing history at its first run.
//	HBAND_TEST_ARTIFACTS  a folder; every screenshot is also copied there as
//	                      <name>.png (any run of characters in name other than
//	                      letters, digits, '.', '_' and '-' becomes one '_'),
//	                      for a person to look at.
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
	"testing"
	"time"
)

// defaultBrowsers are where Chrome installs on Windows. Microsoft Edge is
// deliberately not listed (see the package comment).
var defaultBrowsers = []string{
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
}

// shotTimeout bounds one browser run.
const shotTimeout = 60 * time.Second

// Browser returns the browser to drive. With HBAND_TEST_BROWSER set it is
// that path and never a skip; otherwise it is the first default path that
// exists, or the test skips and says which variable would run it.
func Browser(t testing.TB) string {
	t.Helper()
	if p := os.Getenv("HBAND_TEST_BROWSER"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("HBAND_TEST_BROWSER=%q cannot be used: %v", p, err)
		}
		return p
	}
	for _, p := range defaultBrowsers {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no Chrome found: set HBAND_TEST_BROWSER to a Chromium-family browser (for example chrome.exe) to run this check")
	return ""
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
