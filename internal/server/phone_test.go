package server

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// cssBox is the horizontal space one rule of site.css puts around a box, in CSS
// px, by property and side. Each side is set by the last declaration that names it.
type cssBox map[string]float64 // "margin-left", "padding-right", "border-left", ...

var cssLen = regexp.MustCompile(`^(-?[0-9]*\.?[0-9]+)(px|rem|em)?$`)

// cssPx reads one length. 0 is 0, rem and em are 16 px (the root size: no rule of
// site.css changes the font size above a comment), and auto is 0 where there is no
// room to share (a viewport narrower than the box's max-width). Anything else
// fails the test: a percentage, calc() or a unit this reader does not know would
// make the sum below a guess.
func cssPx(t *testing.T, sel, prop, v string) float64 {
	t.Helper()
	if v == "auto" {
		return 0
	}
	m := cssLen.FindStringSubmatch(v)
	if m == nil {
		t.Fatalf("site.css %s { %s: %s }: a length TestThreadFitsPhoneWidth cannot read", sel, prop, v)
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	if m[2] == "rem" || m[2] == "em" {
		n *= 16
	}
	return n
}

// sides expands a 1 to 4 value shorthand into top, right, bottom, left.
func sides(vals []string) (top, right, bottom, left string) {
	switch len(vals) {
	case 1:
		return vals[0], vals[0], vals[0], vals[0]
	case 2:
		return vals[0], vals[1], vals[0], vals[1]
	case 3:
		return vals[0], vals[1], vals[2], vals[1]
	default:
		return vals[0], vals[1], vals[2], vals[3]
	}
}

// boxOf reads every declaration of a rule that can put horizontal space around a
// box, shorthands included, and fails on a property it would have to guess at.
func boxOf(t *testing.T, sel, decls string) cssBox {
	t.Helper()
	box := cssBox{}
	for _, d := range strings.Split(decls, ";") {
		name, value, ok := strings.Cut(d, ":")
		if !ok {
			continue
		}
		name, value = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)
		vals := strings.Fields(value)
		switch {
		case name == "margin" || name == "padding":
			_, r, _, l := sides(vals)
			box[name+"-right"], box[name+"-left"] = cssPx(t, sel, name, r), cssPx(t, sel, name, l)
		case name == "margin-left" || name == "margin-right" || name == "padding-left" || name == "padding-right":
			box[name] = cssPx(t, sel, name, value)
		case name == "margin-inline" || name == "padding-inline":
			base := strings.TrimSuffix(name, "-inline")
			l, r := vals[0], vals[0]
			if len(vals) > 1 {
				r = vals[1]
			}
			box[base+"-left"], box[base+"-right"] = cssPx(t, sel, name, l), cssPx(t, sel, name, r)
		case strings.HasPrefix(name, "margin-inline-") || strings.HasPrefix(name, "padding-inline-"):
			base, side, _ := strings.Cut(name, "-inline-")
			if side == "start" {
				box[base+"-left"] = cssPx(t, sel, name, value)
			} else {
				box[base+"-right"] = cssPx(t, sel, name, value)
			}
		case name == "border" || name == "border-left" || name == "border-right" || name == "border-inline" ||
			name == "border-inline-start" || name == "border-inline-end":
			// A width is the last length in the value, and none or hidden makes it 0. A
			// value with no length and neither of those (medium, say) is an error.
			w, found := 0.0, false
			for _, v := range vals {
				if cssLen.MatchString(v) {
					w, found = cssPx(t, sel, name, v), true
				}
			}
			if !found && !strings.Contains(" "+value+" ", " none ") && !strings.Contains(" "+value+" ", " hidden ") {
				t.Fatalf("site.css %s { %s: %s }: no width to read", sel, name, value)
			}
			if strings.Contains(" "+value+" ", " none ") || strings.Contains(" "+value+" ", " hidden ") {
				w = 0
			}
			switch name {
			case "border":
				box["border-left"], box["border-right"] = w, w
			case "border-left", "border-inline-start":
				box["border-left"] = w
			case "border-right", "border-inline-end":
				box["border-right"] = w
			default:
				box["border-left"], box["border-right"] = w, w
			}
		case name == "border-width" || name == "border-left-width" || name == "border-right-width" ||
			strings.HasPrefix(name, "border-inline") || name == "width" || name == "min-width" ||
			name == "inline-size" || name == "min-inline-size" || name == "position" || name == "float":
			// A width, an inline size or a positioning scheme on a box of the thread
			// changes the width a body gets in a way this reader does not model.
			t.Fatalf("site.css %s { %s: %s }: a property TestThreadFitsPhoneWidth cannot read", sel, name, value)
		}
	}
	return box
}

// TestThreadFitsPhoneWidth ties the depth cap to the indent it costs. The cap and
// the indent live in two files (thread.go, site.css), and a phone is the width
// that decides how many levels a body can still be read at: at 360 px, a comment
// at the cap must still have at least minBody CSS px. The test reads site.css,
// shorthands included, and fails on any horizontal margin, padding or border on
// the boxes that wrap a comment other than the three of .replies (the indent) and
// main's padding (the gutter), so an indent spelled another way, or a new one on
// .comment or article, is counted or refused, never missed. It models the CSS
// layout only; the real-browser look at 360, 400 and 1000 px of a depth-5 branch
// page (a long login, an unbroken body, the Author badge, an open reply form) is
// run by validate.
func TestThreadFitsPhoneWidth(t *testing.T) {
	const (
		viewport = 360.0
		minBody  = 240.0
	)
	data, err := staticFS.ReadFile("static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	css := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(data), "")
	if strings.Contains(css, "@") {
		t.Fatal("site.css has an at-rule (@media, @import ...): this test reads flat rules only, so it would miss what the at-rule changes")
	}
	// The boxes between the viewport and the text of a comment, by what names them.
	wrap := func(sel string) string {
		subject := sel[strings.LastIndexAny(sel, " >+~")+1:]
		subject = regexp.MustCompile(`\[[^\]]*\]|::?[a-z-]+(\([^)]*\))?`).ReplaceAllString(subject, "")
		switch {
		case regexp.MustCompile(`(^|\.)replies$`).MatchString(subject):
			return "replies"
		case regexp.MustCompile(`^(main)(\.[a-z-]+)*$`).MatchString(subject):
			return "main"
		case subject == "*", regexp.MustCompile(`^(html|body|article|section)(\.[a-z-]+)*$`).MatchString(subject),
			regexp.MustCompile(`(^|\.)(comment|comments|body|reader|branch)$`).MatchString(subject):
			return "other"
		}
		return ""
	}
	seen := map[string]cssBox{"replies": {}, "main": {}, "other": {}}
	rules := 0
	for _, m := range regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(css, -1) {
		for _, sel := range strings.Split(m[1], ",") {
			sel = strings.TrimSpace(sel)
			kind := wrap(sel)
			if kind == "" {
				continue
			}
			rules++
			for k, v := range boxOf(t, sel, m[2]) {
				if kind == "other" {
					if v != 0 {
						t.Errorf("site.css %s { %s: %vpx }: horizontal space on a box that wraps a comment; only .replies and main may have any", sel, k, v)
					}
					continue
				}
				seen[kind][k] = v
			}
		}
	}
	if rules < 6 {
		t.Fatalf("matched only %d rules of site.css: the selectors of the thread have moved and this test reads nothing", rules)
	}
	for k, v := range seen["replies"] {
		if v != 0 && k != "margin-left" && k != "padding-left" && k != "border-left" {
			t.Errorf(".replies has %s %vpx: the indent is margin-left, padding-left and border-left", k, v)
		}
	}
	for k, v := range seen["main"] {
		if strings.HasPrefix(k, "margin") && v != 0 || strings.HasPrefix(k, "border") && v != 0 {
			t.Errorf("main has %s %vpx: only its padding is read as the gutter", k, v)
		}
	}
	indent := seen["replies"]["margin-left"] + seen["replies"]["padding-left"] + seen["replies"]["border-left"]
	gutter := seen["main"]["padding-left"] + seen["main"]["padding-right"]
	if indent == 0 || gutter == 0 {
		t.Fatalf("read an indent of %vpx and a gutter of %vpx from site.css: one of them is not being read", indent, gutter)
	}
	body := viewport - gutter - float64(threadDepthCap-1)*indent
	t.Logf("a comment at level %d has %.0f px at %.0f px (indent %.0f px a level, gutter %.0f px)", threadDepthCap, body, viewport, indent, gutter)
	if body < minBody {
		t.Errorf("a comment at level %d (the cap) has %.0f px at %.0f px, want at least %.0f: %d levels of %.0f px indent and a gutter of %.0f px",
			threadDepthCap, body, viewport, minBody, threadDepthCap-1, indent, gutter)
	}
	// What has to fit in that width: a login is one unbreakable word, and a flex
	// item's minimum width is its longest word unless it may break anywhere.
	login := regexp.MustCompile(`(?m)^\.login \{([^}]*)\}`).FindStringSubmatch(css)
	if login == nil || !strings.Contains(login[1], "overflow-wrap: anywhere") {
		t.Error(".login does not break anywhere: a 39-character GitHub login would be wider than the body at the cap")
	}
}
