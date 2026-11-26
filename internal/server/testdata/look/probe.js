({
  // bare reads the painted look of each kind of element on bare.html, a page with
  // no stylesheet of its own: the browser's default for the scheme in force.
  bare() {
    const get = (id) => document.getElementById(id);
    const paint = (el, pseudo) => {
      const s = getComputedStyle(el, pseudo);
      return { bg: s.backgroundColor, bs: s.borderTopStyle, bw: s.borderTopWidth, bc: s.borderTopColor, r: s.borderTopLeftRadius,
        color: s.color, display: s.display, bbs: s.borderBottomStyle, bbc: s.borderBottomColor };
    };
    return { a: paint(get("a")), button: paint(get("button")), text: paint(get("text")), select: paint(get("select")),
      textarea: paint(get("textarea")), file: paint(get("file"), "::file-selector-button"), summary: paint(get("summary")),
      th: paint(get("th")), td: paint(get("td")) };
  },

  // look measures the page in front of it and returns what is wrong with it, as
  // lines that start with a code (fit, default-link, contrast, ...). args.only is
  // "contrast" for the passes that re-measure a forced hover or focus state.
  look(args) {
    const out = [];
    const seen = {};
    const stats = {};
    const de = document.documentElement;
    const path = (el) => {
      if (!el || el === document) return "document";
      let s = el.tagName.toLowerCase();
      if (el.id) s += "#" + el.id;
      if (typeof el.className === "string" && el.className.trim()) s += "." + el.className.trim().split(/\s+/).join(".");
      const t = (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 24);
      return s + (t ? " \"" + t + "\"" : "");
    };
    const bad = (code, el, msg) => out.push(code + ": " + path(el) + " " + msg);
    const rgb = (s) => {
      const m = /^rgba?\(([^)]+)\)$/.exec(s || "");
      if (!m) return null;
      const p = m[1].split(/[ ,\/]+/).filter(Boolean).map(Number);
      if (p.length < 3 || p.some((n) => Number.isNaN(n))) return null;
      return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 };
    };
    const lin = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
    const lum = (c) => 0.2126 * lin(c.r) + 0.7152 * lin(c.g) + 0.0722 * lin(c.b);
    const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
    const visible = (el) => el.getClientRects().length > 0 && getComputedStyle(el).visibility === "visible";
    // bgOf is the opaque colour behind an element: its own fill, or the nearest
    // ancestor's. null means the backdrop cannot be measured (a translucent fill or
    // an image), which fails the check instead of passing it.
    const bgOf = (el) => {
      for (let n = el; n; n = n.parentElement) {
        const s = getComputedStyle(n);
        if (s.backgroundImage !== "none") return null;
        const c = rgb(s.backgroundColor);
        if (!c) return null;
        if (c.a === 1) return c;
        if (c.a > 0) return null;
      }
      return null;
    };
    const same = (a, b, keys) => keys.every((k) => a[k] === b[k]);

    const forced = args.only === "contrast";

    if (!forced) {
      // (a) one stylesheet, no inline style, the script count the page owes.
      if (document.querySelectorAll("style").length) bad("inline-style", de, "a style element");
      if (document.querySelectorAll("[style]").length) bad("inline-style", de, "a style attribute");
      const sheets = [...document.styleSheets].map((s) => s.href || "inline");
      if (sheets.length !== 1 || !sheets[0].endsWith("/static/site.css")) bad("sheets", de, "style sheets " + JSON.stringify(sheets));
      const scripts = document.querySelectorAll("script").length;
      if (scripts !== args.scripts) bad("script-count", de, scripts + " script tags, want " + args.scripts);

      // (c) no horizontal scroll, against the width asked for. A mobile page that
      // overflows widens its layout viewport, so innerWidth is held to the request too.
      stats.scrollWidth = de.scrollWidth; stats.innerWidth = innerWidth; stats.clientWidth = de.clientWidth;
      if (args.mobile) {
        if (de.scrollWidth > args.width) bad("fit", de, "scrollWidth " + de.scrollWidth + " > " + args.width);
        if (innerWidth !== args.width) bad("fit", de, "innerWidth " + innerWidth + " != " + args.width + ": the layout viewport widened to fit the page");
      } else if (de.scrollWidth > de.clientWidth) {
        bad("fit", de, "scrollWidth " + de.scrollWidth + " > clientWidth " + de.clientWidth);
      }

      // (e) an opaque page background, which the caller compares across schemes.
      const hb = rgb(getComputedStyle(de).backgroundColor);
      stats.htmlBg = getComputedStyle(de).backgroundColor;
      if (!hb || hb.a !== 1) bad("html-bg", de, "html background " + stats.htmlBg + " is not opaque");

      // (b) nothing left at the browser default, judged on what is painted.
      const bare = args.bare;
      for (const el of document.querySelectorAll("a[href]")) {
        if (!visible(el)) continue;
        if (getComputedStyle(el).color === bare.a.color) bad("default-link", el, "has the browser's link colour " + bare.a.color);
      }
      const paint = (el, pseudo) => {
        const s = getComputedStyle(el, pseudo);
        return { bg: s.backgroundColor, bs: s.borderTopStyle, bw: s.borderTopWidth, bc: s.borderTopColor, r: s.borderTopLeftRadius };
      };
      const painted = ["bg", "bs", "bc", "r"];
      for (const el of document.querySelectorAll("button")) {
        if (visible(el) && same(paint(el), bare.button, painted)) bad("default-button", el, "is drawn as the browser draws a button");
      }
      for (const el of document.querySelectorAll("input, select, textarea")) {
        if (!visible(el)) continue;
        const type = el.tagName === "INPUT" ? (el.getAttribute("type") || "text") : "";
        if (el.tagName === "INPUT" && type === "file") {
          if (same(paint(el, "::file-selector-button"), bare.file, painted)) bad("default-file", el, "its choose-file button is the browser's");
          continue;
        }
        if (el.tagName === "INPUT" && type !== "text") continue;
        const key = el.tagName === "INPUT" ? "text" : el.tagName.toLowerCase();
        if (same(paint(el), bare[key], painted)) bad("default-" + key, el, "is drawn as the browser draws it");
      }
      for (const el of document.querySelectorAll("summary")) {
        if (!visible(el)) continue;
        const s = getComputedStyle(el);
        if (s.color === bare.summary.color && s.display === bare.summary.display) bad("default-summary", el, "is a plain disclosure triangle");
      }
      for (const el of document.querySelectorAll("th, td")) {
        if (!visible(el)) continue;
        const s = getComputedStyle(el);
        const b = el.tagName === "TH" ? bare.th : bare.td;
        if (s.color === b.color && s.backgroundColor === b.bg && s.borderBottomStyle === b.bbs && s.borderBottomColor === b.bbc) {
          bad("default-" + el.tagName.toLowerCase(), el, "is drawn as the browser draws a table cell");
        }
      }

      // (g) the role each bound control has to compute, by where it sits.
      const t = args.tokens;
      const roles = [
        ["accent page-head action", ".page-head .primary", "accent", "onAccent", "fill"],
        ["current admin nav item", ".admin-nav a[aria-current=page]", "surface2", "text", "fill"],
        ["danger confirm in .remove", ".remove form button", "danger", "onDanger", "fill"],
        ["danger confirm in .block", ".block form button", "danger", "onDanger", "fill"],
        ["danger delete", "form[action$=\"/delete\"] button", "danger", "onDanger", "fill"],
        ["accent sign in", ".signin", "accent", "onAccent", "fill"],
        ["accent post comment", ".comments > form button", "accent", "onAccent", "fill"],
        ["accent post reply", ".reply form button", "accent", "onAccent", "fill"],
        ["accent send feedback", ".feedback form button", "accent", "onAccent", "fill"],
        ["accent send reply", ".feedback-inbox form button", "accent", "onAccent", "fill"],
        ["accent upload", "form[action=\"/admin/reports\"] button", "accent", "onAccent", "fill"],
        ["danger summary of .remove", ".remove > summary", "", "danger", "text"],
        ["danger summary of .block", ".block > summary", "", "danger", "text"],
      ];
      for (const [name, sel, bgKey, fgKey, kind] of roles) {
        for (const el of document.querySelectorAll(sel)) {
          if (!visible(el)) continue;
          seen[name] = (seen[name] || 0) + 1;
          const s = getComputedStyle(el);
          if (kind === "fill" && s.backgroundColor !== t[bgKey]) bad("role", el, name + ": fill " + s.backgroundColor + ", want " + t[bgKey]);
          if (s.color !== t[fgKey]) bad("role", el, name + ": text " + s.color + ", want " + t[fgKey]);
        }
      }

      // A page-head action that is not the primary one is the neutral button, never the ink or the danger fill.
      for (const el of document.querySelectorAll(".page-head form button:not(.primary)")) {
        if (!visible(el)) continue;
        seen["neutral page-head action"] = (seen["neutral page-head action"] || 0) + 1;
        const s = getComputedStyle(el);
        if (s.backgroundColor === t.accent || s.backgroundColor === t.danger) bad("role", el, "a neutral page-head action has the fill " + s.backgroundColor);
      }

      // The admin sidebar is drawn on the admin pages and on no other page, once, with
      // its current item marked. At phone width it is one row of three, inside the width.
      const navs = document.querySelectorAll("nav.admin-nav");
      const wantNav = args.adminNav ? 1 : 0;
      if (navs.length !== wantNav) bad("admin-nav", de, navs.length + " admin sidebars, want " + wantNav);
      if (wantNav) {
        const current = navs[0].querySelectorAll("a[aria-current=page]").length;
        if (current !== 1) bad("admin-nav", navs[0], current + " items marked as the current page, want 1");
        if (args.mobile) {
          const links = [...navs[0].querySelectorAll("a")];
          const tops = new Set(links.map((a) => Math.round(a.getBoundingClientRect().top)));
          const right = Math.max(...links.map((a) => a.getBoundingClientRect().right));
          if (tops.size !== 1) bad("admin-nav", navs[0], "the sidebar is " + tops.size + " rows at " + args.width + " px, want one");
          if (right > args.width) bad("admin-nav", navs[0], "the sidebar reaches " + right + " px, past the " + args.width + " px viewport");
        }
      }

      // The thread is unboxed: a body at the depth cap keeps the width the model gives it.
      let atCap = 0;
      for (const el of document.querySelectorAll(".comment .body")) {
        if (!visible(el)) continue;
        let depth = 1;
        for (let n = el.parentElement; n; n = n.parentElement) if (n.classList.contains("replies")) depth++;
        if (depth === args.cap) {
          atCap++;
          const w = el.getBoundingClientRect().width;
          if (w < args.minBody) bad("thread-width", el, "a body at depth " + depth + " is " + w + " px wide, want at least " + args.minBody);
        }
      }
      stats.atCap = atCap;
      stats.details = document.querySelectorAll("details").length;
      stats.toggles = document.querySelectorAll("button.toggle").length;
    }

    // (d) contrast: 4.5 for every text, 3 for every filled or bordered control.
    let minText = 99, texts = 0, controls = 0;
    const skipTag = new Set(["SCRIPT", "STYLE", "OPTION", "HEAD", "TITLE", "META", "LINK", "NOSCRIPT"]);
    for (const el of document.body.querySelectorAll("*")) {
      if (skipTag.has(el.tagName) || !visible(el)) continue;
      const isField = ["BUTTON", "SELECT", "TEXTAREA"].includes(el.tagName) || (el.tagName === "INPUT" && el.type !== "hidden" && el.type !== "checkbox" && el.type !== "radio");
      const ownText = [...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
      if (!ownText && !isField) continue;
      const fg = rgb(getComputedStyle(el).color), bg = bgOf(el);
      if (!fg || fg.a !== 1) { bad("contrast", el, "text colour " + getComputedStyle(el).color + " cannot be measured"); continue; }
      if (!bg) { bad("contrast", el, "the colour behind the text cannot be measured"); continue; }
      const r = ratio(fg, bg);
      texts++;
      minText = Math.min(minText, r);
      if (r < 4.5) bad("contrast", el, "text " + r.toFixed(2) + ":1, want 4.5");
    }
    const controlSel = "a[href], button, input, select, textarea";
    for (const el of document.body.querySelectorAll(controlSel)) {
      if (!visible(el) || el.disabled || (el.tagName === "INPUT" && (el.type === "hidden" || el.type === "checkbox" || el.type === "radio"))) continue;
      // The sidebar marks its current page with weight and a tint: that is state, not a
      // control's boundary, and the link's own text is judged for 4.5:1 above.
      if (el.matches(".admin-nav a")) continue;
      const edge = (s) => {
        if (!s || s.borderTopStyle === "none" || parseFloat(s.borderTopWidth) === 0) return null;
        return rgb(s.borderTopColor);
      };
      const isFile = el.tagName === "INPUT" && el.type === "file";
      const s = getComputedStyle(el, isFile ? "::file-selector-button" : undefined);
      const fill = rgb(s.backgroundColor);
      const border = edge(s);
      const hasFill = fill && fill.a > 0, hasBorder = !!border;
      if (!hasFill && !hasBorder) continue;
      if ((hasFill && fill.a !== 1) || (hasBorder && border.a !== 1)) { bad("contrast", el, "a translucent fill or border cannot be measured"); continue; }
      const behind = bgOf(el.parentElement);
      if (!behind) { bad("contrast", el, "the colour behind the control cannot be measured"); continue; }
      // A hover or focus fill is a change of state, not the control's boundary: only a
      // control that had a fill or an edge at rest owes 3:1 in a forced state.
      window.__boundary = window.__boundary || new WeakSet();
      if (forced && !window.__boundary.has(el)) continue;
      if (!forced) window.__boundary.add(el);
      controls++;
      const r = Math.max(hasFill ? ratio(fill, behind) : 0, hasBorder ? ratio(border, behind) : 0);
      if (r < 3) bad("contrast", el, "control edge or fill " + r.toFixed(2) + ":1 against what it sits on, want 3");
    }
    if (args.focus) {
      // A forced :focus-visible ring must exist and be 3:1 against what is behind it.
      for (const el of document.body.querySelectorAll("a[href], button, summary, input, select, textarea")) {
        if (!visible(el) || (el.tagName === "INPUT" && el.type === "hidden")) continue;
        const s = getComputedStyle(el);
        const ring = rgb(s.outlineColor);
        if (s.outlineStyle === "none" || parseFloat(s.outlineWidth) === 0 || !ring || ring.a !== 1) { bad("focus", el, "has no focus ring (outline " + s.outlineStyle + " " + s.outlineWidth + " " + s.outlineColor + ")"); continue; }
        const behind = bgOf(el.parentElement);
        if (!behind) { bad("focus", el, "the colour behind the ring cannot be measured"); continue; }
        const r = ratio(ring, behind);
        if (r < 3) bad("focus", el, "focus ring " + r.toFixed(2) + ":1 against what is behind it, want 3");
      }
    }
    stats.minText = minText; stats.texts = texts; stats.controls = controls;

    if (!forced) {
      // (f) hidden states stay hidden until they apply.
      const note = document.getElementById("feedback-sent");
      if (note) {
        const shown = note.getClientRects().length > 0;
        const want = location.hash === "#feedback-sent";
        stats.note = shown;
        if (shown !== want) bad("hidden-state", note, "drawn=" + shown + ", want " + want);
      }
      for (const b of document.querySelectorAll("button.toggle")) {
        if (args.scripts > 0 && !visible(b)) bad("hidden-state", b, "a toggle is still hidden after the script ran");
        const was = b.hidden;
        b.hidden = true;
        if (getComputedStyle(b).display !== "none") bad("hidden-state", b, "a toggle given the hidden attribute is still drawn");
        b.hidden = was;
      }
    }
    return { violations: out, seen, stats };
  },
})
