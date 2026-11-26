package server

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file holds the design system to its own rules. site.css is one table of
// tokens (a :root block of light-dark(#light, #dark) colours and the type, space
// and shape scales) and every rule below it reads colour only through var(--x).
// lintStylesheet is the whole of that contract as a function of the stylesheet's
// text, so the corpus (testdata/stylesheet_corpus.json) can plant each fault and
// each look-alike and the real file can be held to the same code.

// nonColourTokens are the :root custom properties that are not colours, by exact
// name. Every other custom property of :root is a colour and has the shape
// light-dark(#rrggbb, #rrggbb): a new type or space token has to be added here on
// purpose, so no colour can hide behind a name that looks like a size.
var nonColourTokens = map[string]bool{
	"--font-sans": true, "--font-mono": true,
	"--text-xs": true, "--text-sm": true, "--text-md": true, "--text-lg": true, "--text-xl": true, "--text-2xl": true,
	"--leading": true, "--leading-tight": true,
	"--space-1": true, "--space-2": true, "--space-3": true, "--space-4": true, "--space-5": true, "--space-6": true, "--space-7": true,
	"--panel-pad": true, "--radius-sm": true, "--radius": true, "--radius-pill": true, "--control-h": true,
}

// decorativeTokens are colour tokens that carry no text and no control edge, so no
// contrast pair names them. Every other colour token a rule uses must appear in a
// pair row.
var decorativeTokens = map[string]bool{"divider": true, "thread": true}

type cssDecl struct{ prop, value string }

type cssRule struct {
	sel   string
	decls []cssDecl
}

var (
	cssCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssRuleRE    = regexp.MustCompile(`([^{}]+)\{([^{}]*)\}`)
)

func parseCSS(css string) []cssRule {
	css = cssCommentRE.ReplaceAllString(css, "")
	var rules []cssRule
	for _, m := range cssRuleRE.FindAllStringSubmatch(css, -1) {
		r := cssRule{sel: strings.TrimSpace(m[1])}
		for _, d := range strings.Split(m[2], ";") {
			name, value, ok := strings.Cut(d, ":")
			if !ok {
				continue
			}
			r.decls = append(r.decls, cssDecl{strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)})
		}
		rules = append(rules, r)
	}
	return rules
}

// The CSS Color 4 named colours, and the system colours (Canvas among them). A
// value that holds one outside :root is a literal that stays put when the scheme
// flips.
var namedColours = setOf(strings.Fields(`aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue
blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan
darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon
darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey
dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey
honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan
lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray
lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid
mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream
mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise
palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown
salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan
teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen`))

var systemColours = setOf(strings.Fields(`activetext buttonborder buttonface buttontext canvas canvastext field fieldtext
graytext highlight highlighttext linktext mark marktext selecteditem selecteditemtext visitedtext accentcolor
accentcolortext activeborder activecaption appworkspace background buttonhighlight buttonshadow captiontext
inactiveborder inactivecaption inactivecaptiontext infobackground infotext menu menutext scrollbar threeddarkshadow
threedface threedhighlight threedlightshadow threedshadow window windowframe windowtext`))

var colourFuncs = setOf(strings.Fields(`rgb rgba hsl hsla hwb lab lch oklab oklch color color-mix light-dark device-cmyk`))

func setOf(words []string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

var (
	quotedRE   = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	varUseRE   = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)\s*\)`)
	varAnyRE   = regexp.MustCompile(`var\(\s*(--[A-Za-z0-9_-]+)`)
	identRE    = regexp.MustCompile(`#[0-9A-Za-z]+|-?[A-Za-z][A-Za-z0-9-]*\(?`)
	frameSelRE = regexp.MustCompile(`(^|[^\w-])iframe\b|\.report-frame\b`)
	lightDark  = regexp.MustCompile(`^light-dark\(\s*(#[0-9a-fA-F]{6})\s*,\s*(#[0-9a-fA-F]{6})\s*\)$`)
	// An edge shorthand: a colour inside it cannot be given through a token without
	// the whole declaration going invalid in an engine that cannot resolve the token.
	edgeShorthandRE = regexp.MustCompile(`^(border(-(top|right|bottom|left|block|inline)(-(start|end))?)?|outline)$`)
)

// literalIn lists the colour literals in one declaration value: a hex colour, a
// colour function, a named or system colour. var(--x) reads, currentColor,
// transparent, the CSS-wide keywords, lengths and every other keyword are not
// literals. Canvas is a literal everywhere but in a frame rule.
func literalIn(value string, frame bool) []string {
	value = quotedRE.ReplaceAllString(value, " ")
	value = varUseRE.ReplaceAllString(value, " ")
	var out []string
	for _, tok := range identRE.FindAllString(value, -1) {
		switch {
		case strings.HasPrefix(tok, "#"):
			out = append(out, tok)
		case strings.HasSuffix(tok, "("):
			if colourFuncs[strings.ToLower(strings.TrimSuffix(tok, "("))] {
				out = append(out, tok)
			}
		default:
			l := strings.ToLower(tok)
			if namedColours[l] || (systemColours[l] && !(l == "canvas" && frame)) {
				out = append(out, tok)
			}
		}
	}
	return out
}

func hexRGB(h string) (r, g, b int) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(h, "#"), 16, 32)
	return int(v >> 16 & 255), int(v >> 8 & 255), int(v & 255)
}

// nearSignal reports whether a colour is within 8 per channel of a pure signal
// colour, which the render assay reads as a script run (lesson #941).
func nearSignal(h string) bool {
	r, g, b := hexRGB(h)
	near := func(a, b, c, x, y, z int) bool {
		return math.Abs(float64(a-x)) <= 8 && math.Abs(float64(b-y)) <= 8 && math.Abs(float64(c-z)) <= 8
	}
	return near(r, g, b, 255, 0, 0) || near(r, g, b, 0, 255, 0)
}

// resets reports whether a border or outline shorthand sets the colour a longhand
// had given earlier in the same rule back to currentColor.
func resets(shorthand, longhand string) bool {
	switch {
	case shorthand == "border":
		return strings.HasPrefix(longhand, "border-") && strings.HasSuffix(longhand, "-color")
	case shorthand == "outline":
		return longhand == "outline-color"
	case strings.HasPrefix(shorthand, "border-"):
		return longhand == "border-color" || longhand == shorthand+"-color"
	}
	return false
}

var frameReachRE = regexp.MustCompile(`^(filter|backdrop-filter|mix-blend-mode|opacity|mask|mask-.*|clip-path)$`)
var vendorRE = regexp.MustCompile(`^-(webkit|moz|ms|o)-`)

func frameAllowed(prop string) bool {
	switch prop {
	case "display", "width", "height", "min-height", "max-width":
		return true
	}
	return strings.HasPrefix(prop, "margin") || strings.HasPrefix(prop, "border") || strings.HasPrefix(prop, "background")
}

// lintStylesheet returns what is wrong with a stylesheet, one "code: detail" line
// each. The codes: at-rule, url, motion, color-scheme, token-shape, signal-colour,
// token-outside-root, undefined-var, colour-literal, frame-reach, frame-prop,
// edge-var-shorthand, edge-order.
func lintStylesheet(css string) []string {
	var out []string
	add := func(code, format string, a ...any) { out = append(out, code+": "+fmt.Sprintf(format, a...)) }
	if strings.Contains(cssCommentRE.ReplaceAllString(css, ""), "@") {
		add("at-rule", "an at-rule (@media, @import, @font-face ...): TestThreadFitsPhoneWidth reads flat rules only")
	}
	rules := parseCSS(css)
	defined := map[string]bool{}
	for _, r := range rules {
		if r.sel == ":root" {
			for _, d := range r.decls {
				if strings.HasPrefix(d.prop, "--") {
					defined[d.prop] = true
				}
			}
		}
	}
	schemes := 0
	for _, r := range rules {
		root := r.sel == ":root"
		frame := frameSelRE.MatchString(r.sel)
		colourSeen := map[string]bool{} // *-color longhands met so far in this rule
		for _, d := range r.decls {
			prop := d.prop
			base := vendorRE.ReplaceAllString(prop, "")
			for _, m := range varAnyRE.FindAllStringSubmatch(d.value, -1) {
				if !defined[m[1]] {
					add("undefined-var", "%s { %s: %s } names %s, which :root does not define", r.sel, prop, d.value, m[1])
				}
			}
			if prop == "color-scheme" {
				schemes++
				if !root || strings.Join(strings.Fields(d.value), " ") != "light dark" {
					add("color-scheme", "%s { color-scheme: %s }: only :root may set it, to light dark; below :root it changes a report's own scheme", r.sel, d.value)
				}
			}
			if strings.Contains(d.value, "url(") {
				add("url", "%s { %s: %s }: the site's policy allows no data: image and a url() reaches out", r.sel, prop, d.value)
			}
			if strings.HasPrefix(base, "transition") || strings.HasPrefix(base, "animation") {
				add("motion", "%s { %s }: no motion, because reduced motion cannot be honoured without an at-rule", r.sel, prop)
			}
			if frameReachRE.MatchString(base) {
				add("frame-reach", "%s { %s }: this repaints what is below it, and a report's frame is below main", r.sel, prop)
			}
			if frame && !frameAllowed(prop) {
				add("frame-prop", "%s { %s }: a rule that names the report frame may set only its size, margin, border and background", r.sel, prop)
			}
			if root {
				if !strings.HasPrefix(prop, "--") {
					continue
				}
				if nonColourTokens[prop] {
					if len(literalIn(d.value, false)) > 0 {
						add("token-shape", "%s holds a colour literal %v", prop, literalIn(d.value, false))
					}
					continue
				}
				m := lightDark.FindStringSubmatch(d.value)
				switch {
				case m == nil:
					add("token-shape", "%s: %s is not light-dark(#rrggbb, #rrggbb); a name outside the type and space list is a colour", prop, d.value)
				case strings.EqualFold(m[1], m[2]):
					add("token-shape", "%s: %s gives the same colour in both schemes", prop, d.value)
				case nearSignal(m[1]) || nearSignal(m[2]):
					add("signal-colour", "%s: %s is within 8 of a pure signal colour the render assay reads as a script run", prop, d.value)
				}
				continue
			}
			if strings.HasPrefix(prop, "--") {
				add("token-outside-root", "%s { %s } defines a token outside :root", r.sel, prop)
			}
			for _, lit := range literalIn(d.value, frame) {
				add("colour-literal", "%s { %s: %s } holds %q: a colour outside the token table stays fixed when the scheme flips", r.sel, prop, d.value, lit)
			}
			if edgeShorthandRE.MatchString(prop) {
				if strings.Contains(d.value, "var(") {
					add("edge-var-shorthand", "%s { %s: %s }: a token reaches an edge through a *-color longhand, after a literal width and style", r.sel, prop, d.value)
				}
				for longhand := range colourSeen {
					if resets(prop, longhand) {
						add("edge-order", "%s { %s } comes after %s in the same rule and sets its colour back to currentColor", r.sel, prop, longhand)
					}
				}
			}
			if strings.HasSuffix(prop, "-color") && (strings.HasPrefix(prop, "border") || strings.HasPrefix(prop, "outline")) {
				colourSeen[prop] = true
			}
		}
	}
	if schemes != 1 {
		add("color-scheme", "color-scheme is set %d times, want exactly once on :root", schemes)
	}
	return out
}

func codesOf(findings []string) []string {
	seen := map[string]bool{}
	for _, f := range findings {
		seen[strings.SplitN(f, ":", 2)[0]] = true
	}
	var codes []string
	for c := range seen {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}

// tokenTable reads the colour tokens of :root: name (without --) to light, dark.
func tokenTable(css string) map[string][2]string {
	out := map[string][2]string{}
	for _, r := range parseCSS(css) {
		if r.sel != ":root" {
			continue
		}
		for _, d := range r.decls {
			if m := lightDark.FindStringSubmatch(d.value); m != nil && strings.HasPrefix(d.prop, "--") {
				out[strings.TrimPrefix(d.prop, "--")] = [2]string{strings.ToLower(m[1]), strings.ToLower(m[2])}
			}
		}
	}
	return out
}

func luminance(h string) float64 {
	r, g, b := hexRGB(h)
	lin := func(v int) float64 {
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrast(a, b string) float64 {
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + 0.05) / (math.Min(x, y) + 0.05)
}

// pairRow is one foreground on one background that the site draws, and the ratio it
// owes: 4.5 for text, 3.0 for an edge or a fill a control is found by. The browser
// pass measures the ratios the seeded pages reach; these rows are the states it
// cannot draw (a hover fill, a focus ring inside a tint, the notice tints).
type pairRow struct {
	fg, bg string
	min    float64
}

var pairRows = func() []pairRow {
	var rows []pairRow
	add := func(min float64, fg string, bgs ...string) {
		for _, bg := range bgs {
			rows = append(rows, pairRow{fg, bg, min})
		}
	}
	page := []string{"bg", "surface", "surface-2"}
	// Text, 4.5.
	add(4.5, "text", "bg", "surface", "surface-2", "accent-soft", "danger-soft", "ok-soft", "warning-soft")
	add(4.5, "text-muted", page...)
	add(4.5, "link", page...)
	add(4.5, "accent-hover", page...)
	add(4.5, "danger", "bg", "surface", "surface-2", "danger-soft")
	add(4.5, "ok", "ok-soft")
	add(4.5, "warning", "bg", "surface", "surface-2", "warning-soft")
	add(4.5, "accent", "accent-soft")
	add(4.5, "on-accent", "accent", "accent-hover")
	add(4.5, "on-danger", "danger", "danger-hover")
	// Control edges, fills, notice edges and the focus ring, 3.0.
	add(3.0, "border", page...)
	add(3.0, "accent", page...)
	add(3.0, "accent-hover", page...)
	add(3.0, "danger", "bg", "surface", "surface-2", "danger-soft")
	add(3.0, "danger-hover", "bg", "surface", "surface-2", "danger-soft")
	add(3.0, "ok", "bg", "surface", "surface-2")
	add(3.0, "focus", "bg", "surface", "surface-2", "danger-soft", "ok-soft", "accent-soft", "warning-soft")
	return rows
}()

func TestStylesheetTokens(t *testing.T) {
	raw, err := staticFS.ReadFile("static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	if findings := lintStylesheet(css); len(findings) > 0 {
		t.Errorf("site.css breaks the design-system contract:\n%s", strings.Join(findings, "\n"))
	}
	tokens := tokenTable(css)
	if len(tokens) < 20 {
		t.Fatalf("read %d colour tokens from :root, want the whole table: the reader reads nothing", len(tokens))
	}

	// Every declared pair meets its bar in each scheme, and every ratio is logged.
	used := map[string]bool{}
	for _, row := range pairRows {
		fg, okf := tokens[row.fg]
		bg, okb := tokens[row.bg]
		if !okf || !okb {
			t.Errorf("pair %s on %s names a token :root does not define", row.fg, row.bg)
			continue
		}
		used[row.fg], used[row.bg] = true, true
		for i, scheme := range []string{"light", "dark"} {
			r := contrast(fg[i], bg[i])
			t.Logf("%-5s %-12s %s on %-12s %s  %5.2f:1 (want %.1f)", scheme, row.fg, fg[i], row.bg, bg[i], r, row.min)
			if r < row.min {
				t.Errorf("%s: %s %s on %s %s is %.2f:1, want at least %.1f:1", scheme, row.fg, fg[i], row.bg, bg[i], r, row.min)
			}
		}
	}

	// A token a rule reads has to be in the table, so that a new token cannot skip it.
	for _, r := range parseCSS(css) {
		if r.sel == ":root" {
			continue
		}
		for _, d := range r.decls {
			for _, m := range varAnyRE.FindAllStringSubmatch(d.value, -1) {
				name := strings.TrimPrefix(m[1], "--")
				if _, isColour := tokens[name]; isColour && !used[name] && !decorativeTokens[name] {
					t.Errorf("%s { %s } reads colour token %s, which no row of the pair table judges", r.sel, d.prop, m[1])
				}
			}
		}
	}
}

// corpusRow plants one stylesheet: prelude puts the standard :root in front of it,
// and fires is the exact set of codes lintStylesheet must answer with.
type corpusRow struct {
	ID      string   `json:"id"`
	Class   string   `json:"class"`
	CSS     string   `json:"css"`
	Prelude bool     `json:"prelude"`
	Fires   []string `json:"fires"`
}

const corpusPrelude = `:root { color-scheme: light dark; }
:root { --bg: light-dark(#f7f6f3, #161513); --surface: light-dark(#ffffff, #1f1e1b); --text: light-dark(#1c1b19, #ecebe6); --text-muted: light-dark(#5f5d58, #aeaba3); --border: light-dark(#84817a, #7d7a72); --divider: light-dark(#e0ddd6, #36342f); --thread: light-dark(#cfccc4, #4e4c46); --danger: light-dark(#b42318, #ffa198); --focus: light-dark(#1f4fbf, #90b4ff); --text-sm: .875rem; --space-1: .25rem; }
`

// TestStylesheetCorpus runs lintStylesheet over the classes a stylesheet author can
// produce by accident, each as rows that must fire and look-alikes that must stay
// quiet: colour literals outside the token table (named colours included), rules
// that reach the report frame without naming it, and edges whose colour is a token
// inside a shorthand. Every code the linter has is exercised by some row.
func TestStylesheetCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/stylesheet_corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []corpusRow
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rows); err != nil {
		t.Fatalf("testdata/stylesheet_corpus.json: %v", err)
	}
	fired := map[string]bool{}
	classes := map[string][2]int{} // class: fire rows, quiet rows
	ids := map[string]bool{}
	for _, r := range rows {
		if r.ID == "" || r.CSS == "" || r.Class == "" {
			t.Fatalf("a corpus row has no id, class or css: %+v", r)
		}
		if ids[r.ID] {
			t.Fatalf("corpus id %q is used twice", r.ID)
		}
		ids[r.ID] = true
		c := classes[r.Class]
		if len(r.Fires) == 0 {
			c[1]++
		} else {
			c[0]++
		}
		classes[r.Class] = c
		css := r.CSS
		if r.Prelude {
			css = corpusPrelude + css
		}
		got := codesOf(lintStylesheet(css))
		want := append([]string(nil), r.Fires...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("row %s (%s): %q\n  lint says %v, want %v\n  %s", r.ID, r.Class, r.CSS, got, want, strings.Join(lintStylesheet(css), "\n  "))
		}
		for _, code := range got {
			fired[code] = true
		}
	}
	for _, code := range []string{"at-rule", "url", "motion", "color-scheme", "token-shape", "signal-colour", "token-outside-root",
		"undefined-var", "colour-literal", "frame-reach", "frame-prop", "edge-var-shorthand", "edge-order"} {
		if !fired[code] {
			t.Errorf("no corpus row makes lintStylesheet answer %q", code)
		}
	}
	for _, class := range []string{"colour-literals", "frame-reach", "edge-colour", "token-table"} {
		if c := classes[class]; c[0] < 5 || c[1] < 3 {
			t.Errorf("corpus class %s has %d fire rows and %d quiet rows, want at least 5 and 3", class, c[0], c[1])
		}
	}
}
