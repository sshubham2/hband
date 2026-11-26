// Package layout is the one document shell every hband page takes: the head, the
// opening of a page (doctype, html, head, body and the page's header) and its
// closing. It holds templates and nothing else, so internal/server and
// internal/auth, which render pages from separate template sets, can both parse
// it without either one importing the other.
package layout

import "embed"

// FS holds templates/base.html, which defines "head", "page-start" and
// "page-end". A consumer parses FS first and its own templates second; the
// consumer defines "header", which page-start calls, and passes a value with
// .Title and .ThreadScript.
//
//go:embed templates/*.html
var FS embed.FS
