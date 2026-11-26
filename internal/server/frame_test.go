package server

import (
	"fmt"
	"image"
	"testing"
)

// patch is the mean colour of the 5x5 block of pixels centred on (x, y).
func patch(img image.Image, x, y int) [3]int {
	var sum [3]int
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			r, g, b, _ := img.At(x+dx, y+dy).RGBA()
			sum[0] += int(r >> 8)
			sum[1] += int(g >> 8)
			sum[2] += int(b >> 8)
		}
	}
	return [3]int{sum[0] / 25, sum[1] / 25, sum[2] / 25}
}

func within(a, b [3]int, tol int) bool {
	for i := range a {
		if d := a[i] - b[i]; d > tol || d < -tol {
			return false
		}
	}
	return true
}

func hexOf(c [3]int) string { return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]) }

// TestReportFrameKeepsItsOwnCanvas holds the site to its isolation promise on the
// paint: the site's styling never reaches into a report. A report in the reader's
// frame has to look as it looks opened alone, under each scheme, whatever the
// report says about its own background and colour scheme. The frame is judged on a
// patch in its empty right edge, against the same offset in /raw/<slug> opened on
// its own, so the test needs no knowledge of what Chrome's canvas colours are.
func TestReportFrameKeepsItsOwnCanvas(t *testing.T) {
	h := newHarness(t, false)
	owner := h.mint(testOwnerID, "the-owner")
	reportsByCase := []struct{ slug, why, html string }{
		{"frame-a", "sets neither a background nor a colour scheme",
			"<!doctype html><title>A</title><p>A sets neither a background nor a colour scheme, so it is drawn on the canvas of a plain page.</p>"},
		{"frame-b", "has its own dark-scheme media query that paints the background",
			"<!doctype html><title>B</title><style>@media (prefers-color-scheme: dark) { html { background: #204060; } }</style><p>B paints its own background when the visitor asks for dark.</p>"},
		{"frame-c", "opts into both schemes and paints no background",
			"<!doctype html><title>C</title><style>:root { color-scheme: light dark; }</style><p>C opts into light and dark and paints nothing, so the browser's canvas shows.</p>"},
	}
	for _, r := range reportsByCase {
		h.mustUpload(owner, r.slug, "Report "+r.slug, "published", []byte(r.html))
	}

	b := newLookBrowser(t)
	tab := b.tab
	tab.Viewport(1280, 1000, false)
	var outsideInDark [3]int
	var insideInDark [3]int
	for _, dark := range []bool{false, true} {
		b.setScheme(dark)
		scheme := "light"
		if dark {
			scheme = "dark"
		}
		for _, r := range reportsByCase {
			tab.Navigate("about:blank")
			tab.Navigate(h.srv.URL + "/reports/" + r.slug)
			var rect [4]float64
			tab.Eval(`(() => { const r = document.querySelector('iframe.report-frame').getBoundingClientRect(); return [r.left, r.top, r.right, r.bottom]; })()`, &rect)
			img := shoot(t, tab)
			// The frame's inner edge is 1 px inside its border; the patch sits 12 px from
			// the right inner edge and 120 px below the top inner edge, where no report here
			// has any content.
			innerW := int(rect[2]-rect[0]) - 2
			dx, dy := innerW-12, 120
			inside := patch(img, int(rect[0])+1+dx, int(rect[1])+1+dy)
			outside := patch(img, int(rect[0])-20, int(rect[1])+1+dy)

			tab.Navigate("about:blank")
			tab.Navigate(h.srv.URL + "/raw/" + r.slug)
			alone := patch(shoot(t, tab), dx, dy)

			t.Logf("%s %s (%s): in the frame %s, alone %s, site around the frame %s", r.slug, scheme, r.why, hexOf(inside), hexOf(alone), hexOf(outside))
			if !within(inside, alone, 8) {
				t.Errorf("%s under %s: the frame shows %s where the report opened alone shows %s: the site's styling reaches into the report", r.slug, scheme, hexOf(inside), hexOf(alone))
			}
			if dark && r.slug == "frame-a" {
				outsideInDark, insideInDark = outside, inside
			}
		}
	}
	// The control: report A under dark sits in a dark page, so a patch of the site
	// around the frame must differ from one inside it. Without that the test could not
	// tell a canvas of the report's own from the site's surface showing through.
	if within(outsideInDark, insideInDark, 8) {
		t.Errorf("under dark, the site around the frame is %s and report A inside it is %s: the patches cannot tell the report's canvas from the site's surface", hexOf(outsideInDark), hexOf(insideInDark))
	}
}
