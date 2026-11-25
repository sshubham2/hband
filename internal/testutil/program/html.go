package program

import (
	"html"
	"regexp"
	"strings"
)

// Node is an element of a page, or a run of its text (Tag "#text"); the root is
// "#root". Dom builds the tree the way the finish lines' Python reader did: as a
// forgiving tree, with the end tags HTML leaves out supplied and the text of
// script, style, textarea and title kept as raw text under their element.
type Node struct {
	Tag    string
	Attrs  map[string]string
	Parent *Node
	Kids   []*Node
	Data   string // text nodes only
}

var (
	attrRE   = regexp.MustCompile(`([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+)))?`)
	startRE  = regexp.MustCompile(`^<(/?)([a-zA-Z][^\s/>]*)((?:[^>"']|"[^"]*"|'[^']*')*)>`)
	voidTags = " area base br col embed hr img input link meta source track wbr "
	// a start tag closes an open element of the same kind
	selfClosing = " li p tr td option "
	rawText     = " script style textarea title "
	skipText    = " script style template textarea title "
)

// Dom parses a page.
func Dom(text string) *Node {
	root := &Node{Tag: "#root", Attrs: map[string]string{}}
	cur := root
	add := func(data string) {
		cur.Kids = append(cur.Kids, &Node{Tag: "#text", Data: data, Parent: cur, Attrs: map[string]string{}})
	}
	for i := 0; i < len(text); {
		if text[i] != '<' {
			j := strings.IndexByte(text[i:], '<')
			if j < 0 {
				j = len(text) - i
			}
			add(html.UnescapeString(text[i : i+j]))
			i += j
			continue
		}
		rest := text[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			if e := strings.Index(rest, "-->"); e >= 0 {
				i += e + 3
			} else {
				i = len(text)
			}
			continue
		case strings.HasPrefix(rest, "<!"), strings.HasPrefix(rest, "<?"):
			if e := strings.IndexByte(rest, '>'); e >= 0 {
				i += e + 1
			} else {
				i = len(text)
			}
			continue
		}
		m := startRE.FindStringSubmatch(rest)
		if m == nil {
			add("<")
			i++
			continue
		}
		i += len(m[0])
		name := strings.ToLower(m[2])
		if m[1] == "/" {
			open := false
			for n := cur; n.Parent != nil; n = n.Parent {
				if n.Tag == name {
					open = true
				}
			}
			for open && cur.Parent != nil {
				was := cur.Tag
				cur = cur.Parent
				if was == name {
					break
				}
			}
			continue
		}
		for cur.Parent != nil && cur.Tag == name && strings.Contains(selfClosing, " "+name+" ") {
			cur = cur.Parent
		}
		n := &Node{Tag: name, Attrs: map[string]string{}, Parent: cur}
		for _, a := range attrRE.FindAllStringSubmatch(m[3], -1) {
			n.Attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2] + a[3] + a[4])
		}
		cur.Kids = append(cur.Kids, n)
		switch {
		case strings.HasSuffix(m[3], "/"), strings.Contains(voidTags, " "+name+" "):
		case strings.Contains(rawText, " "+name+" "):
			end := strings.Index(strings.ToLower(text[i:]), "</"+name)
			body := text[i:]
			if end >= 0 {
				body = text[i : i+end]
			}
			if body != "" {
				data := body
				if name == "textarea" || name == "title" {
					data = html.UnescapeString(body)
				}
				n.Kids = append(n.Kids, &Node{Tag: "#text", Data: data, Parent: n, Attrs: map[string]string{}})
			}
			if end < 0 {
				i = len(text)
			} else if e := strings.IndexByte(text[i+end:], '>'); e >= 0 {
				i += end + e + 1
			} else {
				i = len(text)
			}
		default:
			cur = n
		}
	}
	return root
}

// Walk lists every element and text under n, in page order.
func (n *Node) Walk() []*Node {
	var out []*Node
	for _, k := range n.Kids {
		out = append(out, k)
		out = append(out, k.Walk()...)
	}
	return out
}

// Chain is the root-first list of the elements around n, n last.
func (n *Node) Chain() []*Node {
	if n == nil {
		return nil
	}
	return append(n.Parent.Chain(), n)
}

// Text is what n shows a person: its text and its elements' text, less the
// elements skip names (and always script, style, template, textarea and title).
func (n *Node) Text(skip func(*Node) bool) string {
	if n.Tag == "#text" {
		return n.Data
	}
	if strings.Contains(skipText, " "+n.Tag+" ") || (skip != nil && skip(n)) {
		return ""
	}
	parts := make([]string, len(n.Kids))
	for i, k := range n.Kids {
		parts[i] = strings.Join(strings.Fields(k.Text(skip)), " ")
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
