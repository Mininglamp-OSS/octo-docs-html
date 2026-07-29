package core

import (
	"reflect"
	"strings"
	"testing"
)

// StampAids is a byte-exact port; these tests pin its observable behavior — which
// tags get a data-odoc-aid, the exact aid strings (a function of the frozen
// Cyrb53 over stripped content), idempotence, and the parse traps (attribute
// values containing '>', raw-text tags, void elements, already-stamped input).

func TestStampStampsArtifactTags(t *testing.T) {
	in := `<body><section><p>hi</p><img src="a.png"></section></body>`
	want := `<body><section data-odoc-aid="1l6mnuqtjhy"><p>hi</p><img src="a.png" data-odoc-aid="1etotygyt3m"></section></body>`
	res := StampAids(in)
	if res.HTML != want {
		t.Errorf("HTML:\n got %q\nwant %q", res.HTML, want)
	}
	if len(res.AIDs) != 2 {
		t.Fatalf("aids = %d, want 2", len(res.AIDs))
	}
	tags := map[string]bool{res.AIDs[0].Tag: true, res.AIDs[1].Tag: true}
	if !tags["section"] || !tags["img"] {
		t.Errorf("tags = %q/%q, want the set {section, img}", res.AIDs[0].Tag, res.AIDs[1].Tag)
	}
}

func TestStampNoArtifactsIsPassthrough(t *testing.T) {
	in := `<body><p>plain text no artifacts</p></body>`
	res := StampAids(in)
	if res.HTML != in {
		t.Errorf("passthrough changed HTML: %q", res.HTML)
	}
	if len(res.AIDs) != 0 {
		t.Errorf("aids = %d, want 0", len(res.AIDs))
	}
}

func TestStampIsIdempotent(t *testing.T) {
	in := `<body><section><p>x</p></section></body>`
	once := StampAids(in)
	twice := StampAids(once.HTML)
	if once.HTML != twice.HTML {
		t.Errorf("not idempotent:\n once %q\ntwice %q", once.HTML, twice.HTML)
	}
	// A pre-stamped element keeps its existing aid rather than getting a new one.
	pre := `<body><section data-odoc-aid="14m9wlpaboz"><p>x</p></section></body>`
	if got := StampAids(pre); got.HTML != pre {
		t.Errorf("re-stamped an already-stamped doc: %q", got.HTML)
	}
}

// The tag scanner must not treat a '>' inside a quoted attribute value as the end
// of the tag — a classic HTML-parse trap.
func TestStampAttributeValueWithGreaterThan(t *testing.T) {
	in := `<body><img alt="a > b" src="x.png"></body>`
	want := `<body><img alt="a > b" src="x.png" data-odoc-aid="2b8fykuv7qz"></body>`
	if got := StampAids(in); got.HTML != want {
		t.Errorf("attr-with-> :\n got %q\nwant %q", got.HTML, want)
	}
}

// Raw-text tags (script/style) are never stamped, and content inside them must not
// be mis-scanned for artifact tags.
func TestStampSkipsRawTextTags(t *testing.T) {
	in := `<body><script>var x=1</script><section><p>y</p></section></body>`
	res := StampAids(in)
	if len(res.AIDs) != 1 || res.AIDs[0].Tag != "section" {
		t.Fatalf("aids = %+v, want a single section", res.AIDs)
	}
	want := `<body><script>var x=1</script><section data-odoc-aid="1ywg46qkab5"><p>y</p></section></body>`
	if res.HTML != want {
		t.Errorf("script-skip:\n got %q\nwant %q", res.HTML, want)
	}
}

func TestStampVoidAndSvg(t *testing.T) {
	// Void element (img) gets the attribute inside the self-terminating tag.
	if got := StampAids(`<body><img src="a.png"></body>`).HTML; got != `<body><img src="a.png" data-odoc-aid="1etotygyt3m"></body>` {
		t.Errorf("void img: %q", got)
	}
	// SVG is stampable; viewBox is preserved verbatim (case-sensitive attr).
	svg := `<body><svg viewBox="0 0 24 24"><path d="M3 8"/></svg></body>`
	want := `<body><svg viewBox="0 0 24 24" data-odoc-aid="28osv6m0k8m"><path d="M3 8"/></svg></body>`
	if got := StampAids(svg).HTML; got != want {
		t.Errorf("svg:\n got %q\nwant %q", got, want)
	}
}

// The same content stamped twice yields the same aid (content-addressed); changing
// the content changes the aid.
func TestStampAidIsContentAddressed(t *testing.T) {
	a := StampAids(`<body><section><p>alpha</p></section></body>`).AIDs
	b := StampAids(`<body><section><p>alpha</p></section></body>`).AIDs
	c := StampAids(`<body><section><p>beta</p></section></body>`).AIDs
	if len(a) != 1 || len(b) != 1 || len(c) != 1 {
		t.Fatalf("expected one aid each: %d/%d/%d", len(a), len(b), len(c))
	}
	if a[0].AID != b[0].AID {
		t.Errorf("same content gave different aids: %s vs %s", a[0].AID, b[0].AID)
	}
	if a[0].AID == c[0].AID {
		t.Errorf("different content gave the same aid: %s", a[0].AID)
	}
}

// ElementByAID must locate an artifact in an already-stamped doc by the aid the
// stamper wrote, returning its full outer HTML (open tag through close tag) and
// tag name, and miss cleanly on an unknown aid.
func TestElementByAIDHitAndMiss(t *testing.T) {
	in := `<body><section><p>hi</p><img src="a.png"></section></body>`
	stamped := StampAids(in)
	// Find the section's aid from the stamp index.
	var sectionAID, imgAID string
	for _, a := range stamped.AIDs {
		switch a.Tag {
		case "section":
			sectionAID = a.AID
		case "img":
			imgAID = a.AID
		}
	}
	if sectionAID == "" || imgAID == "" {
		t.Fatalf("expected section+img aids, got %+v", stamped.AIDs)
	}

	outer, tag, ok := ElementByAID(stamped.HTML, sectionAID)
	if !ok {
		t.Fatal("section aid not found")
	}
	if tag != "section" {
		t.Errorf("tag = %q, want section", tag)
	}
	// Outer fragment must include the open and close tags and the inner <img>.
	if !strings.HasPrefix(outer, "<section") || !strings.HasSuffix(outer, "</section>") {
		t.Errorf("outer not a full section fragment: %q", outer)
	}
	if !strings.Contains(outer, `data-odoc-aid="`+sectionAID+`"`) {
		t.Errorf("outer missing the section aid: %q", outer)
	}

	// A void element (img) returns just its self-terminating tag.
	outerImg, tagImg, okImg := ElementByAID(stamped.HTML, imgAID)
	if !okImg || tagImg != "img" {
		t.Fatalf("img aid lookup = %q,%v", tagImg, okImg)
	}
	if !strings.HasPrefix(outerImg, "<img") || strings.Contains(outerImg, "</") {
		t.Errorf("img outer should be a single void tag: %q", outerImg)
	}

	if _, _, ok := ElementByAID(stamped.HTML, "no-such-aid"); ok {
		t.Error("unknown aid unexpectedly matched")
	}
	if _, _, ok := ElementByAID(stamped.HTML, ""); ok {
		t.Error("empty aid unexpectedly matched")
	}
}

// ReplaceElementByAID must swap exactly the located element's outer HTML, leaving
// the rest of the document byte-identical, and miss cleanly on an unknown aid.
func TestReplaceElementByAID(t *testing.T) {
	in := `<body><section><p>old</p></section><figure>keep</figure></body>`
	stamped := StampAids(in)
	var sectionAID string
	for _, a := range stamped.AIDs {
		if a.Tag == "section" {
			sectionAID = a.AID
		}
	}
	if sectionAID == "" {
		t.Fatalf("no section aid in %+v", stamped.AIDs)
	}
	out, ok := ReplaceElementByAID(stamped.HTML, sectionAID, `<section><p>new</p></section>`)
	if !ok {
		t.Fatal("replace missed a present aid")
	}
	if strings.Contains(out, "old") {
		t.Errorf("old content still present: %q", out)
	}
	if !strings.Contains(out, "new") {
		t.Errorf("new content missing: %q", out)
	}
	// The untouched sibling (and its stamped aid) must survive verbatim.
	if !strings.Contains(out, "<figure") || !strings.Contains(out, "keep") {
		t.Errorf("sibling figure clobbered: %q", out)
	}

	if _, ok := ReplaceElementByAID(stamped.HTML, "nope", `<section></section>`); ok {
		t.Error("replace matched an unknown aid")
	}
}

// SingleTopLevelTag gates aid replacements: exactly one top-level element passes;
// multi-element fragments, non-elements, and raw-text/script fragments are
// rejected so a replace can't smuggle extra nodes or scripts past the boundary.
func TestSingleTopLevelTag(t *testing.T) {
	pass := []string{
		`<section><p>x</p></section>`,
		`  <figure>only</figure>  `,
		`<img src="a.png">`,
		`<img src="a.png"/>`,
		`<svg/>`,
		`<svg />`,
		`<math/>`,
		`<div>a <span>nested</span> b</div>`,
	}
	for _, s := range pass {
		if _, ok := SingleTopLevelTag(s); !ok {
			t.Errorf("expected single-element accept: %q", s)
		}
	}
	fail := []string{
		``,
		`   `,
		`plain text`,
		`<section></section><section></section>`, // two top-level elements
		`<section></section> trailing`,           // trailing non-whitespace
		`<svg/ >`,                                // slash is not immediately before >
		`<script>alert(1)</script>`,              // raw-text/script fragment
		`<style>.a{}</style>`,
		`text <section></section>`, // leading non-element
	}
	for _, s := range fail {
		if _, ok := SingleTopLevelTag(s); ok {
			t.Errorf("expected reject: %q", s)
		}
	}
}

// Fix C: a NON-void tag written self-closed (<section/>) must not be treated as
// void — the browser would swallow following siblings, so it needs an explicit
// close tag. Only true void tags (img/hr/br/...) may skip the close, with or
// without the trailing slash. iframe is NON-void (P1): it may hold fallback
// content, so a self-closed or unclosed iframe is rejected and only a fully
// closed <iframe>...</iframe> is accepted.
func TestSingleTopLevelTagNonVoidSelfCloseRejected(t *testing.T) {
	reject := []string{
		`<section/>`,         // non-void self-closed, no close tag
		`<div/>`,             // same
		`<section/><p>x</p>`, // self-closed then a sibling
		`<iframe/>`,          // iframe is non-void: self-close is not a close
		`<iframe src="x">`,   // iframe is non-void: needs an explicit close
	}
	for _, s := range reject {
		if _, ok := SingleTopLevelTag(s); ok {
			t.Errorf("non-void self-close must be rejected: %q", s)
		}
	}
	accept := []string{
		`<section></section>`,       // explicit close is fine
		`<iframe src="x"></iframe>`, // iframe closed through its real </iframe>
		`<img/>`,                    // true void with slash
		`<br>`,                      // true void without slash
	}
	for _, s := range accept {
		if _, ok := SingleTopLevelTag(s); !ok {
			t.Errorf("expected accept: %q", s)
		}
	}
}

// Fix B: SafeReplacementFragment layers injection scanning on top of the
// structural single-element check. Raw-text tags, event handlers, and
// javascript: URLs at ANY nesting depth are rejected even when the fragment is a
// single top-level element.
func TestSafeReplacementFragment(t *testing.T) {
	pass := []string{
		`<section><p>hello</p></section>`,
		`<img src="a.png">`,
		`<svg/>`,
		`<svg />`,
		`<math/>`,
		`<div><a href="https://example.com">ok</a></div>`,
		`<section data-note="onload=alert(1)">onerror= is text</section>`,
		`<section title='before /onload = after'>safe</section>`,
		`<a href="https://example.com/a">safe</a>`,
		`<a href="/relative">safe</a>`,
		`<img src="data:image/png;base64,AAAA">`,
		`<object data="/relative"></object>`,
		`<img src="data:image/png;base64,PHN2Zz4=">`,
		`<svg><animate attributeName="opacity" values="javascript:prose;1"/></svg>`,
		`<svg><set attributeName="fill" to="data:image/svg+xml,prose"/></svg>`,
		`<svg><animate values="javascript:prose"/></svg>`,
		`<div><set attributeName="href" to="javascript:prose"></set></div>`,
		`<svg><set attributeName="href" from="https://safe" from="javascript:alert(1)"/></svg>`,
		`<svg><set attributeName="href" to="https://safe" to="javascript:alert(1)"/></svg>`,
		`<svg><animate attributeName="href" values="https://safe" values="javascript:alert(1)"/></svg>`,
	}
	for _, s := range pass {
		if _, ok := SafeReplacementFragment(s); !ok {
			t.Errorf("expected safe accept: %q", s)
		}
	}
	fail := []string{
		`<img src=x onerror=alert(1)>`,                   // event handler on a void tag
		`<img/onerror=alert(1)>`,                         // slash-separated event handler
		`<section onload="x()"><p>y</p></section>`,       // event handler on top-level tag
		`<section/onload=alert(1)></section>`,            // slash-separated root handler
		`<section/OnLoAd = alert(1)></section>`,          // mixed case and spacing
		`<section><img / ONERROR = alert(1)></section>`,  // nested slash and spacing
		`<section><img/onerror></section>`,               // valueless event handler
		`<div><script>alert(1)</script></div>`,           // nested raw-text (inner script)
		`<div><style>.a{}</style></div>`,                 // nested style
		`<div><a href="javascript:alert(1)">x</a></div>`, // javascript: URL
		`<a href="JavaScript:evil()">x</a>`,              // case-insensitive scheme
		`<button onClick="go()">x</button>`,              // mixed-case handler
		`<iframe srcdoc="<p>unsafe</p>"></iframe>`,
		`<iframe srcdoc="&lt;p&gt;unsafe&lt;/p&gt;"></iframe>`,
		`<a href="&#106;avascript:alert(1)">x</a>`,
		"<a href=\"java\tscript:alert(1)\">x</a>",
		`<a href="vb&#115;cript:alert(1)">x</a>`,
		`<iframe src="DATA:TEXT/HTML;base64,PHNjcmlwdD4="></iframe>`,
		`<iframe src="data:text&#x2f;html,<p>x</p>"></iframe>`,
		`<object data="javascript:alert(1)"></object>`,
		`<object data="&#106;avascript:alert(1)"></object>`,
		"<object data=\"java\tscript:alert(1)\"></object>",
		`<object data="data:text/html,<script>x</script>"></object>`,
		`<svg><a><animate attributeName="href" values="https://safe; javascript:alert(1)"/></a></svg>`,
		`<svg><a><animate attributeName="xlink:href" from="&#106;avascript:alert(1)"/></a></svg>`,
		"<svg><a><set attributeName=\"href\" to=\"java\tscript:alert(1)\"/></a></svg>",
		`<svg><a><set attributeName="HREF" to="VBScript:alert(1)"/></a></svg>`,
		`<svg><set attributeName="href" from="javascript:alert(1)" from="https://safe"/></svg>`,
		`<svg><set attributeName="href" to="javascript:alert(1)" to="https://safe"/></svg>`,
		`<svg><animate attributeName="href" values="javascript:alert(1)" values="https://safe"/></svg>`,
		`<iframe src="data:text/xml,<svg/>"></iframe>`,
		`<iframe src="DATA:APPLICATION/XML;charset=utf-8;base64,PHN2Zz4="></iframe>`,
		`<object data="data:image/svg+xml;base64,PHN2Zz4="></object>`,
		`<a href="data:application&#x2f;xhtml+xml,<p>x</p>">x</a>`,
	}
	for _, s := range fail {
		if _, ok := SafeReplacementFragment(s); ok {
			t.Errorf("expected injection reject: %q", s)
		}
	}
}

func TestUnsafeReplacementURLRegisteredXMLMediaTypes(t *testing.T) {
	unsafe := []string{
		`data:application/atom+xml,<feed/>`,
		`data:application/rss+xml,<rss/>`,
		`data:application/atom+xml;base64,PGZlZWQvPg==`,
		`data:application/rss+xml;base64,PHJzcy8+`,
		`DATA:APPLICATION/ATOM+XML;CHARSET=UTF-8,<feed/>`,
		`DATA:APPLICATION/RSS+XML;CHARSET=UTF-8;BASE64,PHJzcy8+`,
		`data:application&#x2f;atom+xml,<feed/>`,
		`data:application&sol;rss+xml,<rss/>`,
		"data:application/at\tom+xml,<feed/>",
		"data:application/rs\ts+xml,<rss/>",
	}
	for _, value := range unsafe {
		if !unsafeReplacementURL(value) {
			t.Errorf("expected registered XML media type reject: %q", value)
		}
	}

	safe := []string{
		`data:text/plain,application/atom+xml`,
		`data:image/png;name=application/rss+xml,AAAA`,
		`data:application/json,{"type":"application/atom+xml"}`,
		`data:image/png;base64,AAAA`,
		`https://example.com/application/atom+xml`,
	}
	for _, value := range safe {
		if unsafeReplacementURL(value) {
			t.Errorf("expected non-XML media type accept: %q", value)
		}
	}
}

func TestSafeReplacementFragmentDuplicateSinkFirstWins(t *testing.T) {
	tests := []struct {
		name, safeFirst, unsafeFirst string
	}{
		{"href", `<a href="https://safe" href="javascript:alert(1)">x</a>`, `<a href="javascript:alert(1)" href="https://safe">x</a>`},
		{"src", `<img src="safe.png" src="javascript:alert(1)">`, `<img src="javascript:alert(1)" src="safe.png">`},
		{"xlink href", `<svg><a xlink:href="https://safe" xlink:href="javascript:alert(1)">x</a></svg>`, `<svg><a xlink:href="javascript:alert(1)" xlink:href="https://safe">x</a></svg>`},
		{"action", `<form action="/safe" action="javascript:alert(1)"></form>`, `<form action="javascript:alert(1)" action="/safe"></form>`},
		{"formaction", `<button formaction="/safe" formaction="javascript:alert(1)">x</button>`, `<button formaction="javascript:alert(1)" formaction="/safe">x</button>`},
		{"object data", `<object data="/safe" data="javascript:alert(1)"></object>`, `<object data="javascript:alert(1)" data="/safe"></object>`},
		{"srcdoc", `<iframe srcdoc="" srcdoc="&lt;script&gt;alert(1)&lt;/script&gt;"></iframe>`, `<iframe srcdoc="&lt;script&gt;alert(1)&lt;/script&gt;" srcdoc=""></iframe>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := SafeReplacementFragment(tt.safeFirst); !ok {
				t.Errorf("safe first duplicate rejected: %q", tt.safeFirst)
			}
			if _, ok := SafeReplacementFragment(tt.unsafeFirst); ok {
				t.Errorf("unsafe first duplicate accepted: %q", tt.unsafeFirst)
			}
		})
	}
}

func TestSafeReplacementFragmentForeignBreakoutNamespace(t *testing.T) {
	pass := `<svg><p><set attributeName="href" to="javascript:prose"></set></p></svg>`
	if _, ok := SafeReplacementFragment(pass); !ok {
		t.Fatalf("HTML set after SVG breakout rejected: %q", pass)
	}
	for _, fragment := range []string{
		`<svg><set attributeName="href" to="javascript:alert(1)"></set></svg>`,
		`<svg><g><set attributeName="href" to="javascript:alert(1)"></set></g></svg>`,
		`<svg><foreignObject><svg><set attributeName="href" to="javascript:alert(1)"></set></svg></foreignObject></svg>`,
	} {
		if _, ok := SafeReplacementFragment(fragment); ok {
			t.Errorf("true SVG assignment accepted: %q", fragment)
		}
	}

	opens := scanOpenTags(`<svg><g><p></p><set attributeName="href" to="javascript:prose"></set></g></svg>`)
	for _, open := range opens {
		if open.tag == "p" || open.tag == "set" {
			if open.namespace != namespaceHTML {
				t.Errorf("%s namespace = %v, want HTML after breakout", open.tag, open.namespace)
			}
		}
	}
}

func TestSafeReplacementFragmentForeignEndBreakoutNamespace(t *testing.T) {
	for _, endTag := range []string{"p", "br"} {
		t.Run(endTag, func(t *testing.T) {
			pass := []string{
				`<svg></` + endTag + `><set attributeName="href" to="javascript:prose"></set></svg>`,
				`<svg><g></` + endTag + `><set attributeName="href" to="javascript:prose"></set></g></svg>`,
				`<math><mtext><svg><g></` + endTag + `><set attributeName="href" to="javascript:prose"></set></g></svg></mtext></math>`,
				`<div><svg></` + endTag + `><set attributeName="href" to="javascript:prose"></set></svg><set attributeName="href" to="javascript:prose"></set></div>`,
			}
			for _, fragment := range pass {
				if _, ok := SafeReplacementFragment(fragment); !ok {
					t.Errorf("HTML set after foreign end-tag breakout rejected: %q", fragment)
				}
			}

			for _, fragment := range []string{
				`<svg><set attributeName="href" to="javascript:alert(1)"></set></` + endTag + `></svg>`,
				`<math><mtext><svg><set attributeName="href" to="javascript:alert(1)"></set></svg></mtext></` + endTag + `></math>`,
				`<div><svg></` + endTag + `><set attributeName="href" to="javascript:prose"></set></svg><svg><set attributeName="href" to="javascript:alert(1)"></set></svg></div>`,
			} {
				if _, ok := SafeReplacementFragment(fragment); ok {
					t.Errorf("true SVG assignment accepted around end-tag breakout: %q", fragment)
				}
			}

			opens := scanOpenTags(`<svg><g></` + endTag + `><set></set></g></svg><svg><set></set></svg>`)
			var sets []parsedOpenTag
			for _, open := range opens {
				if open.tag == "set" {
					sets = append(sets, open)
				}
			}
			if len(sets) != 2 || sets[0].namespace != namespaceHTML || sets[1].namespace != namespaceSVG {
				t.Fatalf("set namespaces after breakout and close = %#v", sets)
			}
		})
	}
}

func TestSafeReplacementFragmentForeignBreakoutReentry(t *testing.T) {
	breakouts := []struct {
		name, prefix string
	}{
		{"start", `<svg><p></p>`},
		{"end", `<svg></p>`},
	}
	for _, tt := range breakouts {
		t.Run(tt.name, func(t *testing.T) {
			unsafe := tt.prefix + `<svg><set attributeName="href" to="javascript:alert(1)"/></svg></svg>`
			if _, ok := SafeReplacementFragment(unsafe); ok {
				t.Fatalf("executable assignment accepted after SVG re-entry: %q", unsafe)
			}

			for _, fragment := range []string{
				tt.prefix + `<svg><set attributeName="href" to="https://safe"/></svg></svg>`,
				tt.prefix + `<math><mtext>safe</mtext></math></svg>`,
				tt.prefix + `<svg></svg><set attributeName="href" to="javascript:alert(1)"></set></svg>`,
				tt.prefix + `<svg><set attributeName="href" to="https://safe"/></svg><set attributeName="href" to="javascript:alert(1)"></set></svg>`,
			} {
				if _, ok := SafeReplacementFragment(fragment); !ok {
					t.Errorf("safe breakout/re-entry control rejected: %q", fragment)
				}
			}

			nested := tt.prefix + `<svg></svg><math></math><set></set></svg><svg><set></set></svg>`
			opens := scanOpenTags(nested)
			var svgNamespaces, mathNamespaces, setNamespaces []contentNamespace
			for _, open := range opens {
				switch open.tag {
				case "svg":
					svgNamespaces = append(svgNamespaces, open.namespace)
				case "math":
					mathNamespaces = append(mathNamespaces, open.namespace)
				case "set":
					setNamespaces = append(setNamespaces, open.namespace)
				}
			}
			if len(svgNamespaces) != 3 || svgNamespaces[1] != namespaceSVG || svgNamespaces[2] != namespaceSVG {
				t.Fatalf("SVG namespaces after breakout = %v", svgNamespaces)
			}
			if len(mathNamespaces) != 1 || mathNamespaces[0] != namespaceMathML {
				t.Fatalf("MathML namespace after breakout = %v", mathNamespaces)
			}
			if len(setNamespaces) != 2 || setNamespaces[0] != namespaceHTML || setNamespaces[1] != namespaceSVG {
				t.Fatalf("set namespaces after nested close/root transition = %v", setNamespaces)
			}
		})
	}
}

func TestSafeReplacementFragmentForeignBreakoutMathSVGReentry(t *testing.T) {
	for _, fragment := range []string{
		`<svg><p></p><math><mtext><svg><set attributeName="href" to="javascript:alert(1)"/></svg></mtext></math></svg>`,
		`<svg></p><math><mtext><svg><set attributeName="href" to="javascript:alert(1)"/></svg></mtext></math></svg>`,
	} {
		if _, ok := SafeReplacementFragment(fragment); ok {
			t.Errorf("executable assignment accepted through MathML re-entry: %q", fragment)
		}
	}

	// Tag spelling alone must not turn an ordinary HTML unknown element into SVG.
	fragment := `<div><set attributeName="href" to="javascript:alert(1)"></set></div>`
	if _, ok := SafeReplacementFragment(fragment); !ok {
		t.Fatalf("HTML unknown set rejected: %q", fragment)
	}
}

func TestStampAidsMathMLAnnotationXMLDirectSVG(t *testing.T) {
	in := `<body><math><annotation-xml><svg><foreignObject><section/><figure>inside</figure></section><aside>html-sibling</aside></foreignObject><section/><figure>svg-sibling</figure></svg><figure>math-sibling</figure></annotation-xml></math><figure>after</figure></body>`
	res := StampAids(in)
	assertUniqueAIDs(t, res)

	var sections []StampedArtifact
	for _, artifact := range res.AIDs {
		if artifact.Tag == "section" {
			sections = append(sections, artifact)
		}
	}
	if len(sections) != 2 {
		t.Fatalf("want HTML and SVG sections, got %#v", res.AIDs)
	}

	htmlSection, tag, ok := ElementByAID(res.HTML, sections[0].AID)
	if !ok || tag != "section" || !strings.Contains(htmlSection, `inside</figure></section>`) || strings.Contains(htmlSection, `html-sibling`) {
		t.Fatalf("foreignObject child did not use HTML semantics: %q tag=%q ok=%v", htmlSection, tag, ok)
	}
	svgSection, _, ok := ElementByAID(res.HTML, sections[1].AID)
	if !ok || svgSection != `<section data-odoc-aid="`+sections[1].AID+`"/>` {
		t.Fatalf("direct annotation-xml svg child did not enter SVG namespace: %q ok=%v", svgSection, ok)
	}
	replaced, boundary, ok := ReplaceElementByAIDAt(res.HTML, sections[0].AID, `<section>new</section>`)
	if !ok || boundary != strings.Index(res.HTML, `<section`) || !strings.Contains(replaced, `<section>new</section><aside`) || strings.Contains(replaced, `inside</figure>`) || !strings.Contains(replaced, `svg-sibling`) || !strings.Contains(replaced, `math-sibling`) || !strings.Contains(replaced, `>after</figure>`) {
		t.Fatalf("HTML section replacement boundary wrong: boundary=%d result=%q ok=%v", boundary, replaced, ok)
	}
	if again := StampAids(res.HTML); again.HTML != res.HTML {
		t.Fatalf("re-stamp not idempotent:\n first %q\nsecond %q", res.HTML, again.HTML)
	}
}

func TestMathMLAnnotationXMLNamespaceControls(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		wantChild contentNamespace
		wantGrand contentNamespace
	}{
		{
			name:      "unencoded non-svg stays MathML",
			html:      `<math><annotation-xml><section><figure></figure></section></annotation-xml></math>`,
			wantChild: namespaceMathML,
			wantGrand: namespaceMathML,
		},
		{
			name:      "encoded content enters HTML",
			html:      `<math><annotation-xml encoding="application/xhtml+xml"><section><figure></figure></section></annotation-xml></math>`,
			wantChild: namespaceHTML,
			wantGrand: namespaceHTML,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opens := scanOpenTags(tt.html)
			var child, grand contentNamespace
			for _, open := range opens {
				switch open.tag {
				case "section":
					child = open.namespace
				case "figure":
					grand = open.namespace
				}
			}
			if child != tt.wantChild || grand != tt.wantGrand {
				t.Fatalf("namespaces child=%v grandchild=%v, want %v/%v", child, grand, tt.wantChild, tt.wantGrand)
			}
		})
	}
}

func TestSafeReplacementFragmentAnnotationXMLEncodingEntities(t *testing.T) {
	tests := []struct {
		name string
		html string
		want bool
	}{
		{
			name: "hex text html exposes nested SVG set",
			html: `<math><annotation-xml encoding="text&#x2f;html"><x-foo><svg><set attributeName="href" to="javascript:alert(1)"/></svg></x-foo></annotation-xml></math>`,
		},
		{
			name: "hex XHTML exposes nested SVG animate",
			html: `<math><annotation-xml encoding="application&#x2f;xhtml+xml"><x-foo><svg><animate attributeName="href" values="javascript:alert(1)"/></svg></x-foo></annotation-xml></math>`,
		},
		{
			name: "named slash exposes nested SVG set",
			html: `<math><annotation-xml encoding="text&sol;html"><x-foo><svg><set attributeName="href" to="javascript:alert(1)"/></svg></x-foo></annotation-xml></math>`,
		},
		{
			name: "decimal slash exposes nested SVG animate",
			html: `<math><annotation-xml encoding="application&#47;xhtml+xml"><x-foo><svg><animate attributeName="href" values="javascript:alert(1)"/></svg></x-foo></annotation-xml></math>`,
		},
		{
			name: "ordinary encoding remains MathML",
			html: `<math><annotation-xml encoding="application/xml"><x-foo><svg><set attributeName="href" to="javascript:alert(1)"/></svg></x-foo></annotation-xml></math>`,
			want: true,
		},
		{
			name: "benign assignment remains safe",
			html: `<math><annotation-xml encoding="text&#x2f;html"><x-foo><svg><set attributeName="href" to="https://safe"/></svg></x-foo></annotation-xml></math>`,
			want: true,
		},
		{
			name: "first integration encoding wins",
			html: `<math><annotation-xml encoding="text&#x2f;html" encoding="application/xml"><x-foo><svg><set attributeName="href" to="javascript:alert(1)"/></svg></x-foo></annotation-xml></math>`,
		},
		{
			name: "first ordinary encoding wins",
			html: `<math><annotation-xml encoding="application/xml" encoding="text&#x2f;html"><x-foo><svg><set attributeName="href" to="javascript:alert(1)"/></svg></x-foo></annotation-xml></math>`,
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := SafeReplacementFragment(tt.html); ok != tt.want {
				t.Fatalf("SafeReplacementFragment() ok = %v, want %v", ok, tt.want)
			}
		})
	}
}

// Fix D: hand-written replacements must not carry stamper-owned data-odoc-*
// attributes (Publish re-stamps only stampable open tags; residue ⇒ ambiguous
// selector).
func TestHasDataOdocAttr(t *testing.T) {
	has := []string{
		`<section data-odoc-aid="abc"><p>x</p></section>`,
		`<div data-odoc-artifact="1">y</div>`,
		`<img data-odoc-aid = "z">`,
		`<section data-odoc-aid2="forged"></section>`,
		`<section data-odoc-_private="forged"></section>`,
	}
	for _, s := range has {
		if !HasDataOdocAttr(s) {
			t.Errorf("expected data-odoc detected: %q", s)
		}
	}
	clean := []string{
		`<section><p>x</p></section>`,
		`<div data-foo="1">y</div>`,
	}
	for _, s := range clean {
		if HasDataOdocAttr(s) {
			t.Errorf("false positive data-odoc: %q", s)
		}
	}
}

// issue-21: the element/replace path injects the target's OLD aid onto the
// replacement root, then re-stamps with StampAidsPinned so exactly that root —
// and thus the comment anchored to it — keeps the aid even when the tag/content
// changed. InjectRootAID must stamp only the outermost element (block and void
// forms).
func TestInjectRootAIDStampsOuterElementOnly(t *testing.T) {
	// Block element: aid lands on the root open tag, not the nested <figure>.
	if got := InjectRootAID(`<section><figure>x</figure></section>`, "old1"); got != `<section data-odoc-aid="old1"><figure>x</figure></section>` {
		t.Errorf("block inject:\n got %q", got)
	}
	// Void tag without slash.
	if got := InjectRootAID(`<img src="a.png">`, "old2"); got != `<img src="a.png" data-odoc-aid="old2">` {
		t.Errorf("void inject:\n got %q", got)
	}
	// Self-terminating void tag: aid goes before the trailing slash.
	if got := InjectRootAID(`<img src="a.png"/>`, "old3"); got != `<img src="a.png" data-odoc-aid="old3"/>` {
		t.Errorf("self-close inject:\n got %q", got)
	}
	// Empty aid is a no-op.
	if got := InjectRootAID(`<section></section>`, ""); got != `<section></section>` {
		t.Errorf("empty aid must be a no-op: %q", got)
	}
}

// StampAidsPinned keeps the pinned root's aid while every OTHER element rehashes
// normally. Here the replacement changes tag (section -> figure) and content;
// after the pinned re-stamp the root still carries the OLD aid and the nested
// <img> gets a fresh content-addressed aid.
func TestStampAidsPinnedKeepsPinnedRootOnly(t *testing.T) {
	const prefix = `<body>`
	injected := InjectRootAID(`<figure><p>new</p><img src="a.png"></figure>`, "oldaid")
	doc := prefix + injected + `</body>`
	res := StampAidsPinned(doc, "oldaid", len(prefix))

	var rootAID, imgAID string
	for _, a := range res.AIDs {
		switch a.Tag {
		case "figure":
			rootAID = a.AID
		case "img":
			imgAID = a.AID
		}
	}
	if rootAID != "oldaid" {
		t.Fatalf("root aid = %q; want the pinned oldaid", rootAID)
	}
	if imgAID == "" || imgAID == "oldaid" {
		t.Fatalf("nested img aid = %q; want a fresh content-addressed aid", imgAID)
	}
	if !strings.Contains(res.HTML, `<figure data-odoc-aid="oldaid">`) {
		t.Errorf("stamped HTML lost the pinned root aid: %q", res.HTML)
	}
	// Plain StampAids recomputes the root from content, so it must NOT reproduce
	// the injected aid — proving the pin is opt-in and scoped to StampAidsPinned.
	if got := StampAids(doc); strings.Contains(got.HTML, `<figure data-odoc-aid="oldaid">`) {
		t.Errorf("StampAids should recompute, not keep, the injected aid: %q", got.HTML)
	}
}

// P1(1): pinning is scoped to the replacement root ONLY. Editing an element
// nested inside a stampable ancestor changes the ancestor's content, so the
// ancestor must rehash rather than keep its stale aid. Simulates the post-replace
// document: stale ancestor aid + pinned inner root aid.
func TestStampAidsPinnedRehashesChangedAncestor(t *testing.T) {
	// First stamp a v1 with a section wrapping a figure to learn the section's aid.
	v1 := StampAids(`<body><section><figure>original</figure></section></body>`)
	var staleSectionAID, oldFigureAID string
	for _, a := range v1.AIDs {
		switch a.Tag {
		case "section":
			staleSectionAID = a.AID
		case "figure":
			oldFigureAID = a.AID
		}
	}
	if staleSectionAID == "" || oldFigureAID == "" {
		t.Fatalf("v1 aids incomplete: %+v", v1.AIDs)
	}
	// Replace only the inner <figure> (content changed), injecting its old aid; the
	// ancestor <section> keeps the stale aid v1 stamped onto it. This is exactly
	// the string ReplaceElementByAIDAt would hand to StampAidsPinned.
	inner := InjectRootAID(`<figure>edited</figure>`, oldFigureAID)
	pre := `<body><section data-odoc-aid="` + staleSectionAID + `">`
	doc := pre + inner + `</section></body>`
	res := StampAidsPinned(doc, oldFigureAID, len(pre))

	var newSectionAID, figureAID string
	for _, a := range res.AIDs {
		switch a.Tag {
		case "section":
			newSectionAID = a.AID
		case "figure":
			figureAID = a.AID
		}
	}
	if figureAID != oldFigureAID {
		t.Errorf("pinned figure aid = %q; want preserved %q", figureAID, oldFigureAID)
	}
	// The ancestor's content changed, so it must NOT keep the stale aid.
	if newSectionAID == staleSectionAID {
		t.Errorf("ancestor kept stale aid %q; it must rehash on content change", staleSectionAID)
	}
	if strings.Contains(res.HTML, `data-odoc-aid="`+staleSectionAID+`"`) {
		t.Errorf("stale ancestor aid survived in HTML: %q", res.HTML)
	}
}

// A non-stampable tag can still be pinned for a SINGLE stamp at the core level
// (the service rejects such a root as a replacement since the aid would not
// survive a later plain re-stamp). Verify the pin + index with a plain
// data-odoc-aid and no persistent marker.
func TestStampAidsPinnedIndexesNonStampableRoot(t *testing.T) {
	for _, tag := range []string{"div", "p"} {
		const prefix = `<body>`
		injected := InjectRootAID(`<`+tag+`>hello</`+tag+`>`, "pinaid")
		doc := prefix + injected + `</body>`
		res := StampAidsPinned(doc, "pinaid", len(prefix))

		var found bool
		for _, a := range res.AIDs {
			if a.AID == "pinaid" {
				found = true
				if a.Tag != tag {
					t.Errorf("pinned %s indexed with tag %q", tag, a.Tag)
				}
			}
		}
		if !found {
			t.Errorf("non-stampable <%s> root not indexed: %+v", tag, res.AIDs)
		}
		if !strings.Contains(res.HTML, `<`+tag+` data-odoc-aid="pinaid">`) {
			t.Errorf("non-stampable <%s> root missing pinned aid in HTML: %q", tag, res.HTML)
		}
		// No persistent marker is minted.
		if strings.Contains(res.HTML, "data-odoc-keep") {
			t.Errorf("<%s> unexpectedly carries a data-odoc-keep marker: %q", tag, res.HTML)
		}
	}
}

// P1(1): a SafeReplacementFragment may carry leading whitespace, so the root '<'
// is not at the fragment start. InjectRootAIDAt reports the root offset; adding it
// to the boundary must point StampAidsPinned at the true root. Covers a stampable
// (figure) and non-stampable (div) root.
func TestStampAidsPinnedWhitespacePaddedRoot(t *testing.T) {
	for _, tag := range []string{"figure", "div"} {
		const boundary = len(`<body>`)
		injected, localRoot := InjectRootAIDAt("  \n\t<"+tag+">padded</"+tag+">", "padaid")
		if localRoot <= 0 {
			t.Fatalf("<%s>: localRoot = %d; want > 0 for whitespace-padded fragment", tag, localRoot)
		}
		doc := `<body>` + injected + `</body>`
		res := StampAidsPinned(doc, "padaid", boundary+localRoot)

		var found bool
		for _, a := range res.AIDs {
			if a.AID == "padaid" {
				found = true
				if a.Tag != tag {
					t.Errorf("<%s>: pinned aid indexed with tag %q", tag, a.Tag)
				}
			}
		}
		if !found {
			t.Errorf("<%s>: whitespace-padded root not pinned/indexed: %+v", tag, res.AIDs)
		}
		// Both roots carry only the plain pinned data-odoc-aid (no data-odoc-keep).
		wantSub := `<` + tag + ` data-odoc-aid="padaid">`
		if !strings.Contains(res.HTML, wantSub) {
			t.Errorf("<%s>: whitespace-padded root missing pinned aid in HTML: %q", tag, res.HTML)
		}
	}
}

// P1(2): a self-closing stampable void root must be reconstructed with exactly one
// closing slash and data-odoc-aid before it — never a malformed
// "<img .../ data-odoc-aid/...>". Covers the final StampAidsPinned output.
func TestStampAidsPinnedSelfClosingVoidRoot(t *testing.T) {
	const boundary = len(`<body>`)
	injected, localRoot := InjectRootAIDAt(`<img src="a.png" alt="x"/>`, "imgaid")
	doc := `<body>` + injected + `</body>`
	res := StampAidsPinned(doc, "imgaid", boundary+localRoot)

	if want := `<img src="a.png" alt="x" data-odoc-aid="imgaid"/>`; !strings.Contains(res.HTML, want) {
		t.Errorf("self-closing void root malformed:\n got %q\nwant substring %q", res.HTML, want)
	}
	// Guard against the P1(2) regression: no double slash / slash-before-aid.
	if strings.Contains(res.HTML, `/ data-odoc-aid`) || strings.Contains(res.HTML, `"/data-odoc-aid`) {
		t.Errorf("self-closing void root retained a stray slash: %q", res.HTML)
	}
}

// Plain StampAids must reconstruct a self-closing void with exactly one slash too.
func TestStampAidsSelfClosingVoidSingleSlash(t *testing.T) {
	res := StampAids(`<body><img src="a.png"/></body>`)
	if strings.Contains(res.HTML, `/ data-odoc-aid`) {
		t.Errorf("stray slash before aid: %q", res.HTML)
	}
	if !imgSelfCloseOk(res.HTML) {
		t.Errorf("self-closing img not normalized to a single trailing slash: %q", res.HTML)
	}
}

func imgSelfCloseOk(html string) bool {
	i := strings.Index(html, "<img")
	if i < 0 {
		return false
	}
	end := strings.IndexByte(html[i:], '>')
	if end < 0 {
		return false
	}
	tag := html[i : i+end+1]
	return strings.Count(tag, "/") == 1 && strings.HasSuffix(tag, `"/>`) &&
		strings.Contains(tag, `data-odoc-aid="`)
}

// Fix 3: when an ordinary element's content hash collides with the immovable
// pinned aid, the pin wins verbatim and only the collider is salted away.
func TestStampAidsPinnedResolvesAidCollision(t *testing.T) {
	// The nested element that will collide: a <figure> with fixed content.
	const nested = `<figure>collide-me</figure>`
	nestedHash := StampAids(`<body>` + nested + `</body>`).AIDs[0].AID
	if nestedHash == "" {
		t.Fatal("could not learn nested figure hash")
	}

	// Pin the OUTER <section> root to the nested figure's hash, forcing a collision.
	const prefix = `<body>`
	injected := InjectRootAID(`<section>`+nested+`</section>`, nestedHash)
	doc := prefix + injected + `</body>`
	res := StampAidsPinned(doc, nestedHash, len(prefix))

	var rootAID, figAID string
	for _, a := range res.AIDs {
		switch a.Tag {
		case "section":
			rootAID = a.AID
		case "figure":
			figAID = a.AID
		}
	}
	// Pinned identity is preserved verbatim.
	if rootAID != nestedHash {
		t.Fatalf("pinned root aid = %q; want the preserved %q", rootAID, nestedHash)
	}
	// The colliding nested element was re-salted to a DIFFERENT, unique aid.
	if figAID == "" {
		t.Fatal("nested figure was not indexed")
	}
	if figAID == rootAID {
		t.Fatalf("aid collision not resolved: root and figure both %q", figAID)
	}
	// Document-wide uniqueness: no two stamped aids match.
	assertUniqueAIDs(t, res)
	// The re-salted aid must still be a \w-safe base36 token (reconcile selector).
	for _, r := range figAID {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') {
			t.Fatalf("salted aid %q not base36/\\w-safe", figAID)
		}
	}
	// Determinism: same input stamps to the same salted aid.
	res2 := StampAidsPinned(doc, nestedHash, len(prefix))
	if res2.HTML != res.HTML {
		t.Errorf("collision resolution not deterministic:\n a %q\n b %q", res.HTML, res2.HTML)
	}

	// Server/browser-consistent target: ElementByAID(rootAID) must resolve to the
	// pinned <section> (the FIRST element in document order carrying the aid), the
	// same node the browser's querySelector would pick — not the nested figure.
	outer, tag, ok := ElementByAID(res.HTML, rootAID)
	if !ok || tag != "section" {
		t.Fatalf("ElementByAID(root) = tag %q ok %v; want the pinned section", tag, ok)
	}
	if !strings.HasPrefix(outer, "<section") {
		t.Errorf("resolved element is not the pinned root: %q", outer)
	}

	// Second replace must edit the intended pinned root, not the nested collider:
	// ReplaceElementByAID(rootAID, ...) swaps the <section>.
	replaced, ok := ReplaceElementByAID(res.HTML, rootAID, `<section data-marker="x"><figure>replaced</figure></section>`)
	if !ok {
		t.Fatal("second replace by rootAID missed")
	}
	if !strings.Contains(replaced, `data-marker="x"`) || strings.Contains(replaced, "collide-me") && !strings.Contains(replaced, "replaced") {
		t.Errorf("second replace edited the wrong element: %q", replaced)
	}
}

func TestSaltAwayFromPinnedRetryIsBounded(t *testing.T) {
	calls := 0
	got := saltAwayFromPinnedWithHasher("base", "pinned", 3, func(string, uint32) string {
		calls++
		return "pinned"
	})
	if calls != 3 || got != "pinned0" {
		t.Fatalf("bounded salt retry: calls=%d aid=%q, want 3 and pinned0", calls, got)
	}
}

// assertUniqueAIDs fails the test if any two stamped artifacts share an aid.
func assertUniqueAIDs(t *testing.T, res StampResult) {
	t.Helper()
	seen := map[string]string{}
	for _, a := range res.AIDs {
		if prevTag, dup := seen[a.AID]; dup {
			t.Fatalf("duplicate aid %q on <%s> and <%s>", a.AID, prevTag, a.Tag)
		}
		seen[a.AID] = a.Tag
	}
}

// Fix 3 (no over-uniquification): with NO pin, two identical ordinary artifacts
// legitimately share the same content-addressed aid and the exact bytes are
// unchanged. Salting must NOT globally uniquify ordinary hashes.
func TestStampAidsIdenticalArtifactsShareHash(t *testing.T) {
	const one = `<body><section>same</section></body>`
	const two = `<body><section>same</section><section>same</section></body>`

	solo := StampAids(one)
	dup := StampAids(two)
	if len(solo.AIDs) != 1 || len(dup.AIDs) != 2 {
		t.Fatalf("aid counts: solo=%d dup=%d", len(solo.AIDs), len(dup.AIDs))
	}
	// Both identical sections keep the SAME historical hash as the lone section.
	if dup.AIDs[0].AID != solo.AIDs[0].AID || dup.AIDs[1].AID != solo.AIDs[0].AID {
		t.Fatalf("identical sections diverged: solo=%q dup=%q,%q",
			solo.AIDs[0].AID, dup.AIDs[0].AID, dup.AIDs[1].AID)
	}
	// Exact-bytes stability: re-stamping the already-stamped output is idempotent.
	if again := StampAids(dup.HTML); again.HTML != dup.HTML {
		t.Errorf("re-stamp not idempotent:\n a %q\n b %q", dup.HTML, again.HTML)
	}
}

// A VALUELESS data-odoc-* attribute (no ="...") must still be rejected by
// HasDataOdocAttr — the opt-in harvest honors it, so a caller must not smuggle it in.
func TestHasDataOdocAttrValueless(t *testing.T) {
	has := []string{
		`<div data-odoc-artifact>y</div>`,
		`<section data-odoc-aid>x</section>`,
		`<img data-odoc-artifact/>`,
		`<div data-odoc-artifact >y</div>`,
	}
	for _, s := range has {
		if !HasDataOdocAttr(s) {
			t.Errorf("valueless data-odoc-* not detected: %q", s)
		}
	}
	// A tag whose name merely starts with data-foo must not false-positive.
	if HasDataOdocAttr(`<div data-foo>y</div>`) {
		t.Error("false positive on data-foo")
	}
}

// InjectRootAIDAt must find a self-closing slash QUOTE-AWARE — a '/' inside a
// quoted value (src="a/b.png") is not the self-close, so the aid goes before the
// real trailing slash and the tag keeps a single trailing slash.
func TestInjectRootAIDQuoteAwareSelfClose(t *testing.T) {
	got := InjectRootAID(`<img src="a/b.png"/>`, "qaid")
	want := `<img src="a/b.png" data-odoc-aid="qaid"/>`
	if got != want {
		t.Errorf("quote-aware self-close:\n got %q\nwant %q", got, want)
	}
	// Non-self-closing void with a slash in the value: aid goes at the end, tag
	// stays intact (no stray slash injected).
	got = InjectRootAID(`<img src="a/b.png">`, "qaid2")
	want = `<img src="a/b.png" data-odoc-aid="qaid2">`
	if got != want {
		t.Errorf("quote-aware non-self-close:\n got %q\nwant %q", got, want)
	}
}

// Empty aid is a no-op AND reports localRootOffset -1 (no injection point), so a
// caller adding it to a boundary detects the no-op instead of shifting by '<'.
func TestInjectRootAIDAtEmptyAidOffsetContract(t *testing.T) {
	out, off := InjectRootAIDAt(`  <section></section>`, "")
	if out != `  <section></section>` {
		t.Errorf("empty aid mutated fragment: %q", out)
	}
	if off != -1 {
		t.Errorf("empty aid localRootOffset = %d; want -1 (no injection point)", off)
	}
}

// Fix 2 (root-scope opt-in): the class "odoc-artifact" opt-in is honored ONLY on
// the single root open tag. A nested child carrying it, or the token in text or
// another attribute's value, must NOT make a bare root harvestable. A stampable
// root is always harvestable.
func TestIsHarvestableReplacementRootScope(t *testing.T) {
	harvestable := []string{
		`<section><p>x</p></section>`,            // stampable tag
		`<div class="odoc-artifact">x</div>`,     // root opt-in
		`<div class="a odoc-artifact b">x</div>`, // token among others
		`<div class='odoc-artifact'>x</div>`,     // single-quoted
	}
	for _, s := range harvestable {
		if !IsHarvestableReplacementRoot(s) {
			t.Errorf("want harvestable: %q", s)
		}
	}
	notHarvestable := []string{
		`<div>bare</div>`, // bare non-stampable
		`<p>bare</p>`,     // bare non-stampable
		`<div><span class="odoc-artifact">nested</span></div>`,    // opt-in on a CHILD only
		`<div><p>x</p><span class="odoc-artifact">y</span></div>`, // nested opt-in, bare root
		`<div data-x="odoc-artifact">v</div>`,                     // token only in a value
		`<div title="class=odoc-artifact">v</div>`,                // literal in a value
		`<div>odoc-artifact</div>`,                                // token only in text
	}
	for _, s := range notHarvestable {
		if IsHarvestableReplacementRoot(s) {
			t.Errorf("want NOT harvestable (root-scope): %q", s)
		}
	}
}

// Fix 3 (attribute-name inspection): HasDataOdocAttr must match a real data-odoc-*
// open-tag attribute NAME in every form, and must NOT false-positive on the
// literal string in text or in an attribute's value.
func TestHasDataOdocAttrNameForms(t *testing.T) {
	has := []string{
		`<div data-odoc-artifact>y</div>`,             // valueless
		`<div data-odoc-artifact >y</div>`,            // valueless + trailing space
		`<img data-odoc-artifact/>`,                   // valueless self-close
		`<div DATA-ODOC-AID="x">y</div>`,              // mixed/upper case
		`<div data-odoc-aid2="x">y</div>`,             // suffix
		`<div data-odoc-_private="x">y</div>`,         // underscore
		`<div data-odoc-aid='x'>y</div>`,              // single-quoted value
		`<div data-odoc-aid=x>y</div>`,                // unquoted value
		`<div class="c" data-odoc-keep="1">y</div>`,   // second attribute
		`<div><span data-odoc-aid="x">n</span></div>`, // on a nested child
	}
	for _, s := range has {
		if !HasDataOdocAttr(s) {
			t.Errorf("want data-odoc-* detected: %q", s)
		}
	}
	clean := []string{
		`<div>x</div>`,
		`<div data-foo="1">y</div>`,
		`<div class="data-odoc-aid">y</div>`,           // literal only in a VALUE
		`<div title="data-odoc-artifact here">y</div>`, // literal in a value
		`<div>text data-odoc-aid="x" text</div>`,       // literal only in TEXT node
		`<div data-odoc="x">y</div>`,                   // no trailing hyphen ⇒ not a match
		`<div data-odocx="x">y</div>`,                  // prefix without hyphen
	}
	for _, s := range clean {
		if HasDataOdocAttr(s) {
			t.Errorf("false positive (not a real data-odoc-* attr): %q", s)
		}
	}
}

// Fix 4 (invalid pin degrades cleanly): StampAidsPinned with an offset that
// resolves to NO element must behave byte-for-byte like plain StampAids — no
// element keeps pinnedAID and no collider is salted, even when a real element's
// content hash equals pinnedAID.
func TestStampAidsPinnedInvalidOffsetDegradesToPlain(t *testing.T) {
	const doc = `<body><section>a</section><figure>b</figure></body>`
	plain := StampAids(doc)

	// An offset that lands on no open-tag start (mid-text) must degrade to plain.
	if got := StampAidsPinned(doc, plain.AIDs[0].AID, 999); got.HTML != plain.HTML {
		t.Errorf("invalid offset did not degrade:\n got %q\nwant %q", got.HTML, plain.HTML)
	}
	// A negative offset (missing pin) must degrade to plain.
	if got := StampAidsPinned(doc, plain.AIDs[0].AID, -1); got.HTML != plain.HTML {
		t.Errorf("negative offset did not degrade:\n got %q\nwant %q", got.HTML, plain.HTML)
	}

	// Even when pinnedAID EQUALS a real element's content hash, an invalid offset
	// must NOT salt that colliding element: no reservation without an active pin.
	collidingAID := plain.AIDs[1].AID // the <figure>'s real hash
	got := StampAidsPinned(doc, collidingAID, 12345)
	if got.HTML != plain.HTML {
		t.Errorf("invalid offset salted a collider (must not):\n got %q\nwant %q", got.HTML, plain.HTML)
	}
	for i, a := range got.AIDs {
		if a.AID != plain.AIDs[i].AID {
			t.Errorf("aid[%d] changed under invalid pin: got %q want %q", i, a.AID, plain.AIDs[i].AID)
		}
	}
}

// Fix (opt-in harvest consistency): a class="odoc-artifact" opt-in root accepted
// by IsHarvestableReplacementRoot must ALSO be harvested by the plain re-stamp
// path in every valid quote form (double, single, unquoted), so the element stays
// addressable even though later identity is content-derived rather than pinned.
func TestOptInClassRootPersistsAcrossPlainRestamp(t *testing.T) {
	forms := []struct {
		name string
		root string
	}{
		{"double", `<div class="odoc-artifact"><p>edited</p></div>`},
		{"single", `<div class='odoc-artifact'><p>edited</p></div>`},
		{"unquoted", `<div class=odoc-artifact><p>edited</p></div>`},
		{"among-others-single", `<div class='a odoc-artifact b'><p>edited</p></div>`},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			// Acceptance and harvesting must agree.
			if !IsHarvestableReplacementRoot(f.root) {
				t.Fatalf("root not accepted as harvestable: %q", f.root)
			}
			// v1 document with an addressable <section> we will replace.
			v1 := StampAids(`<body><section><p>orig</p></section></body>`)
			var oldAID string
			for _, a := range v1.AIDs {
				if a.Tag == "section" {
					oldAID = a.AID
				}
			}
			if oldAID == "" {
				t.Fatal("v1 section aid missing")
			}
			// Replace the section with the opt-in root, learning the insertion boundary.
			replaced, boundary, ok := ReplaceElementByAIDAt(v1.HTML, oldAID, f.root)
			if !ok {
				t.Fatalf("replace by aid missed for %q", f.root)
			}
			// Inject the OLD aid onto the replacement root and pin it at its true offset:
			// the pinned re-stamp keeps the OLD aid verbatim so the anchor carries over.
			injected, localRoot := InjectRootAIDAt(f.root, oldAID)
			doc := replaced[:boundary] + injected + replaced[boundary+len(f.root):]
			pinned := StampAidsPinned(doc, oldAID, boundary+localRoot)
			outer, tag, ok := ElementByAID(pinned.HTML, oldAID)
			if !ok {
				t.Fatalf("pinned re-stamp did not carry old aid for %q", f.root)
			}
			if tag != "div" || !strings.Contains(outer, "odoc-artifact") {
				t.Errorf("pinned element resolved wrong: tag=%q outer=%q", tag, outer)
			}
			// Plain re-stamp (what Publish runs on every later version) must still HARVEST
			// the opt-in root regardless of quote style so it keeps an addressable aid. It
			// recomputes the hash (the pinned value is not kept); the element must remain
			// in the aid index so the comment can re-anchor.
			restamped := StampAids(pinned.HTML)
			if len(restamped.AIDs) != 1 || restamped.AIDs[0].Tag != "div" {
				t.Fatalf("opt-in root not harvested on plain re-stamp for %q\nAIDs=%#v\nHTML=%q",
					f.root, restamped.AIDs, restamped.HTML)
			}
			newAID := restamped.AIDs[0].AID
			if newAID == "" {
				t.Fatalf("opt-in root harvested but got empty aid for %q", f.root)
			}
			// The re-stamped aid must be addressable back to the same opt-in <div>.
			outer2, tag2, ok := ElementByAID(restamped.HTML, newAID)
			if !ok || tag2 != "div" || !strings.Contains(outer2, "odoc-artifact") {
				t.Errorf("re-stamped aid not addressable to opt-in root for %q: ok=%v tag=%q outer=%q",
					f.root, ok, tag2, outer2)
			}
			// Idempotent: harvest stays stable on a second plain re-stamp.
			again := StampAids(restamped.HTML)
			if len(again.AIDs) != 1 || again.AIDs[0].AID != newAID {
				t.Errorf("opt-in harvest not stable on second re-stamp for %q: %#v", f.root, again.AIDs)
			}
		})
	}
}

// Fix (opt-in harvest scope): harvestOptInMarkers must not be fooled by the token
// buried in another attribute's value or a nested-only opt-in — the plain path
// must match IsHarvestableReplacementRoot's root-scope contract.
func TestPlainRestampIgnoresNonRootOptIn(t *testing.T) {
	// class token only inside another attribute's VALUE — not a real class token.
	doc := `<body><div data-x="odoc-artifact"><p>x</p></div></body>`
	res := StampAids(doc)
	if len(res.AIDs) != 0 {
		t.Errorf("value-only 'odoc-artifact' wrongly harvested: %#v", res.AIDs)
	}
	// Nested-only opt-in: the bare <div> root must not be harvested; only the
	// <span> carrying the real class token is.
	doc2 := `<body><div><span class="odoc-artifact">n</span></div></body>`
	res2 := StampAids(doc2)
	if len(res2.AIDs) != 1 || res2.AIDs[0].Tag != "span" {
		t.Errorf("nested opt-in harvest wrong: %#v", res2.AIDs)
	}
}

// Fix (comment skipping): HasDataOdocAttr walks open-tag attribute names and
// skips the entire <!-- ... --> comment range, so fake data-odoc-* markup in
// comment text is ignored while a real attribute before/after a comment is still
// detected. Unterminated comments consume the rest.
func TestHasDataOdocAttrSkipsComments(t *testing.T) {
	clean := []string{
		`<!-- <div data-odoc-aid="x"> --><p>y</p>`,            // fake attr inside comment
		`<div>a</div><!-- data-odoc-artifact --><div>b</div>`, // fake valueless in comment
		`<!-- <img data-odoc-artifact/> -->`,                  // fake self-close in comment
		`<p>text</p><!-- <span data-odoc-keep="1"> -->`,       // fake, unterminated after real close
		`<!-- multi\nline <div data-odoc-aid="x"> -->`,        // multiline comment
	}
	for _, s := range clean {
		if HasDataOdocAttr(s) {
			t.Errorf("false positive: fake data-odoc-* inside comment not ignored: %q", s)
		}
	}
	has := []string{
		`<!-- comment --><div data-odoc-aid="x">y</div>`,                         // real attr AFTER a comment
		`<div data-odoc-aid="x">y</div><!-- comment -->`,                         // real attr BEFORE a comment
		`<!-- fake data-odoc-artifact --><section data-odoc-aid="y">z</section>`, // fake then real
		`<div data-odoc-artifact></div><!-- <p data-odoc-aid="fake"> -->`,        // real before, fake in comment
	}
	for _, s := range has {
		if !HasDataOdocAttr(s) {
			t.Errorf("real data-odoc-* around a comment not detected: %q", s)
		}
	}
	// Unterminated comment consuming a REAL-looking attr must not match: browsers
	// treat everything after "<!--" with no "-->" as comment text.
	if HasDataOdocAttr(`<!-- <div data-odoc-aid="x">`) {
		t.Error("unterminated comment: fake attr wrongly detected")
	}
}

// Fix (raw-text/RCDATA body skipping): HasDataOdocAttr skips the entire body of a
// raw-text/RCDATA element (script/style/textarea/title) through its matching
// close tag, so fake data-odoc-* markup in that text is ignored while a real
// attribute on the raw-text open tag or after it closes is still detected.
// Unterminated bodies consume the rest.
func TestHasDataOdocAttrSkipsRawTextBodies(t *testing.T) {
	clean := []string{
		`<script>"<div data-odoc-aid=x>"</script>`,             // fake attr in script text
		`<style>/* <p data-odoc-artifact> */</style>`,          // fake in style text
		`<textarea><span data-odoc-aid="y"></textarea>`,        // fake in textarea text
		`<title><b data-odoc-keep="1"></title>`,                // fake in title text
		`<SCRIPT><div data-odoc-aid=x></SCRIPT>`,               // mixed case tags
		`<p>a</p><script><i data-odoc-aid=z></script><p>b</p>`, // fake between real content
	}
	for _, s := range clean {
		if HasDataOdocAttr(s) {
			t.Errorf("false positive: fake data-odoc-* inside raw-text body not ignored: %q", s)
		}
	}
	has := []string{
		`<script data-odoc-aid="x">"<div>"</script>`,             // real attr ON the script open tag
		`<style data-odoc-artifact></style>`,                     // real valueless on style open tag
		`<script>ignored</script><div data-odoc-aid="y">z</div>`, // real attr AFTER raw-text closes
		`<title>t</title><section data-odoc-aid="w">q</section>`, // real after title
		`<TEXTAREA DATA-ODOC-AID="m"></TEXTAREA>`,                // real attr, mixed case
	}
	for _, s := range has {
		if !HasDataOdocAttr(s) {
			t.Errorf("real data-odoc-* on/after raw-text not detected: %q", s)
		}
	}
	// Unterminated raw-text bodies consume the rest, so fake attrs inside them
	// must not match, and no panic/overrun may occur.
	unterminatedClean := []string{
		`<script>"<div data-odoc-aid=x>"`,     // never closed
		`<style><p data-odoc-artifact>`,       // never closed
		`<textarea><b data-odoc-aid="y">more`, // never closed
	}
	for _, s := range unterminatedClean {
		if HasDataOdocAttr(s) {
			t.Errorf("unterminated raw-text: fake attr wrongly detected: %q", s)
		}
	}
	// A real attr on the unterminated raw-text OPEN tag is still detected.
	if !HasDataOdocAttr(`<script data-odoc-aid="x">"<div>"`) {
		t.Error("real attr on unterminated raw-text open tag not detected")
	}
}

// P1(#1): a trailing slash on a raw-text/RCDATA open tag (<script/>) is NOT a
// self-close; HTML runs the body to the matching close tag. So fake data-odoc
// markup inside <script/>...</script> must be ignored, and a real attribute on a
// tag AFTER the close must still be seen.
func TestRawTextSlashIsNotSelfClose(t *testing.T) {
	// Fake markup inside a slash-opened script body is body text, not a tag.
	if HasDataOdocAttr(`<script/><div data-odoc-aid="x"></script>`) {
		t.Error("<script/> slash treated as self-close: fake body attr wrongly detected")
	}
	// A real attr after the close is found (the body ended at </script>).
	if !HasDataOdocAttr(`<script/>ignored</script><div data-odoc-aid="y">z</div>`) {
		t.Error("real attr after </script> not detected")
	}
	// Same for other raw-text tags.
	if HasDataOdocAttr(`<style/><p data-odoc-artifact></style>`) {
		t.Error("<style/> slash treated as self-close")
	}
}

// P1(#2): harvestOptInMarkers shares the structural walker, so fake opt-in markup
// inside comments and raw-text/RCDATA bodies must never be harvested (no phantom
// aid, no mutation), while a real opt-in on a raw-text OPEN tag or after the body
// closes is still harvested. Includes <script/> (slash is not a self-close).
func TestHarvestOptInMarkersSkipsCommentsAndRawText(t *testing.T) {
	// Fake opt-in class/attr buried in comment or raw-text bodies: no aid, unchanged.
	noHarvest := []string{
		`<body><!-- <div class="odoc-artifact">fake</div> --><p>x</p></body>`,
		`<body><script>"<div class=odoc-artifact>"</script><p>x</p></body>`,
		`<body><style>/* <p data-odoc-artifact> */</style><p>x</p></body>`,
		`<body><textarea><span class="odoc-artifact"></textarea><p>x</p></body>`,
		`<body><title><b data-odoc-artifact></title><p>x</p></body>`,
		`<body><script/><div class="odoc-artifact">fake</div></script><p>x</p></body>`, // slash not self-close
	}
	for _, in := range noHarvest {
		res := StampAids(in)
		if len(res.AIDs) != 0 {
			t.Errorf("fake opt-in harvested: %q -> %#v", in, res.AIDs)
		}
		if res.HTML != in {
			t.Errorf("fake opt-in mutated HTML:\n in  %q\n out %q", in, res.HTML)
		}
	}

	// A real opt-in on a raw-text OPEN tag is harvested (the open tag itself is a tag).
	if got := StampAids(`<body><script class="odoc-artifact">v</script></body>`); len(got.AIDs) != 1 || got.AIDs[0].Tag != "script" {
		t.Errorf("real opt-in on script open tag not harvested: %#v", got.AIDs)
	}
	// A real opt-in AFTER the raw-text body closes is harvested.
	after := StampAids(`<body><script>ignored</script><div class="odoc-artifact">real</div></body>`)
	if len(after.AIDs) != 1 || after.AIDs[0].Tag != "div" {
		t.Errorf("real opt-in after </script> not harvested: %#v", after.AIDs)
	}
	// A real opt-in AFTER a comment is harvested.
	afterC := StampAids(`<body><!-- <div class="odoc-artifact"> --><div class="odoc-artifact">real</div></body>`)
	if len(afterC.AIDs) != 1 || afterC.AIDs[0].Tag != "div" {
		t.Errorf("real opt-in after comment not harvested: %#v", afterC.AIDs)
	}
}

// P1(comment): the structural walker must tokenize the malformed abrupt comment
// terminators the browser treats as an EMPTY comment, so a real tag right after
// them is scanned (not swallowed). The reproduced case is `<!-->` closing an
// empty comment before a real div; `<!--->` (comment-start-dash '>') behaves the
// same. Normal and unterminated comments still behave correctly.
func TestForEachOpenTagMalformedCommentTerminator(t *testing.T) {
	// Exact reproduced case: the browser makes `<!-->` an empty comment, so the
	// following <div data-odoc-aid="forged"> is a REAL element and must be seen.
	if !HasDataOdocAttr(`<section><!--><div data-odoc-aid="forged">x</div></section>`) {
		t.Error(`P1: <!--> not treated as empty comment; real following attr missed`)
	}
	// Closely-related minimal form: `<!--->` also abrupt-closes an empty comment.
	if !HasDataOdocAttr(`<section><!---><div data-odoc-aid="forged">x</div></section>`) {
		t.Error(`P1: <!---> not treated as empty comment; real following attr missed`)
	}
	// A stampable tag right after `<!-->` is a real element and IS harvested.
	res := StampAids(`<body><!--><section>real</section></body>`)
	if len(res.AIDs) != 1 || res.AIDs[0].Tag != "section" {
		t.Errorf("real <section> after <!--> not harvested: %#v", res.AIDs)
	}

	// Normal comment: fake markup inside a well-terminated comment is still text.
	if HasDataOdocAttr(`<!-- <div data-odoc-aid="x"> --><p>y</p>`) {
		t.Error("normal comment content wrongly treated as markup")
	}
	// A short but genuine comment body must NOT be truncated by the abrupt-close
	// rule: content that merely CONTAINS '-' or '>' still runs to the real `-->`.
	if HasDataOdocAttr(`<!--a <div data-odoc-aid="x"> b--><p>y</p>`) {
		t.Error("genuine comment content over-truncated by abrupt-close handling")
	}
	if HasDataOdocAttr(`<!-- a > b <div data-odoc-aid="x"> --><p>y</p>`) {
		t.Error("comment containing '>' over-truncated")
	}
	// Unterminated comment consumes the rest: a real-looking attr after `<!--`
	// with no `-->` is comment text, not a match.
	if HasDataOdocAttr(`<!-- <div data-odoc-aid="x">`) {
		t.Error("unterminated comment: fake attr wrongly detected")
	}
	// The empty comment does not consume the following real content.
	after := HasDataOdocAttr(`<!--><div data-odoc-aid="y">z</div>`)
	if !after {
		t.Error("real attr after empty comment not detected")
	}
}

// P1(harvest): harvestStampableTags shares the structural walker, so a stampable
// tag that appears only inside a comment or a raw-text/RCDATA body is TEXT, not
// an element: it is neither indexed (no phantom artifact) nor mutated. Real
// stampable tags after the comment/body closes are still harvested. Covers the
// reviewer's exact <script> and comment cases and every raw-text/RCDATA tag,
// including <script/> (trailing slash is not a self-close).
func TestHarvestStampableTagsSharedWalker(t *testing.T) {
	// Reviewer's exact cases: no mutation, no phantom artifact.
	noHarvest := []string{
		`<script>const x='<img src=x>';</script>`, // stampable in script text
		`<!-- <img src=x> -->`,                    // stampable in comment
		`<script>const s='<section>y</section>';</script>`,
		`<style>/* <figure>f</figure> */</style>`,
		`<textarea><table>t</table></textarea>`,
		`<title><svg></svg></title>`,
		`<!-- <section>y</section> <figure>z</figure> -->`,
		`<script/><img src=x></script>`, // slash is NOT a self-close
		`<SCRIPT><img src=x></SCRIPT>`,  // mixed case
	}
	for _, in := range noHarvest {
		res := StampAids(in)
		if len(res.AIDs) != 0 {
			t.Errorf("phantom stampable harvested: %q -> %#v", in, res.AIDs)
		}
		if res.HTML != in {
			t.Errorf("stampable harvest mutated text:\n in  %q\n out %q", in, res.HTML)
		}
	}

	// Real stampable tags AFTER a raw-text body closes / after a comment are harvested.
	afterScript := StampAids(`<body><script>ignored '<img>'</script><figure>real</figure></body>`)
	if len(afterScript.AIDs) != 1 || afterScript.AIDs[0].Tag != "figure" {
		t.Errorf("real stampable after </script> not harvested: %#v", afterScript.AIDs)
	}
	afterSlash := StampAids(`<body><script/>ignored</script><section>real</section></body>`)
	if len(afterSlash.AIDs) != 1 || afterSlash.AIDs[0].Tag != "section" {
		t.Errorf("real stampable after <script/> close not harvested: %#v", afterSlash.AIDs)
	}
	afterComment := StampAids(`<body><!-- <section>fake</section> --><section>real</section></body>`)
	if len(afterComment.AIDs) != 1 || afterComment.AIDs[0].Tag != "section" {
		t.Errorf("real stampable after comment not harvested: %#v", afterComment.AIDs)
	}
	// A genuine top-level stampable element is still harvested and stamped.
	plain := StampAids(`<body><section>real</section></body>`)
	if len(plain.AIDs) != 1 || plain.AIDs[0].Tag != "section" {
		t.Fatalf("plain stampable not harvested: %#v", plain.AIDs)
	}
	if !strings.Contains(plain.HTML, `data-odoc-aid="`) {
		t.Errorf("plain stampable not stamped: %q", plain.HTML)
	}
	// Nested stampable inside a stampable is harvested (both indexed), unchanged
	// by the walker refactor.
	nested := StampAids(`<body><section><figure>f</figure></section></body>`)
	if len(nested.AIDs) != 2 {
		t.Errorf("nested stampable count changed: %#v", nested.AIDs)
	}
}

// P2(raw-text close): browser-aligned close recognition. The scanner must close
// the raw-text/RCDATA body on `</tag>`, `</tag >`, `</tag/>`, and `</tag x>` and
// then continue to detect/harvest real following tags — but must NOT treat
// `</tagx>` as a close. Covers script/style/textarea/title, mixed case,
// slash/attribute-like close tails, malformed non-match, and EOF.
func TestRawTextBrowserAlignedClose(t *testing.T) {
	// For each raw-text tag, every valid close tail closes the body so a real
	// data-odoc-* attr after it is detected; fake markup inside stays hidden.
	for _, tag := range []string{"script", "style", "textarea", "title"} {
		valid := []string{
			`<` + tag + `><i data-odoc-aid=z></` + tag + `><div data-odoc-aid="y">z</div>`,
			`<` + tag + `><i data-odoc-aid=z></` + tag + ` ><div data-odoc-aid="y">z</div>`,
			`<` + tag + `><i data-odoc-aid=z></` + tag + `/><div data-odoc-aid="y">z</div>`,
			`<` + tag + `><i data-odoc-aid=z></` + tag + ` x><div data-odoc-aid="y">z</div>`,
		}
		for _, s := range valid {
			if !HasDataOdocAttr(s) {
				t.Errorf("[%s] valid close tail did not close body; real attr missed: %q", tag, s)
			}
		}
		// Mixed-case close.
		mixed := `<` + tag + `><i data-odoc-aid=z></` + strings.ToUpper(tag) + `><div data-odoc-aid="y">z</div>`
		if !HasDataOdocAttr(mixed) {
			t.Errorf("[%s] mixed-case close not recognized: %q", tag, mixed)
		}
		// `</tagx>` is NOT a close: the body (with fake attr) runs on; when the body
		// is never really closed, the trailing real-looking attr is comment/body text.
		nonClose := `<` + tag + `><i data-odoc-aid=z></` + tag + `x><div data-odoc-aid="y">z`
		if HasDataOdocAttr(nonClose) {
			t.Errorf("[%s] </%sx> wrongly treated as close: %q", tag, tag, nonClose)
		}
		// EOF with no close consumes the rest: fake body attr not detected.
		eof := `<` + tag + `><i data-odoc-aid=z>`
		if HasDataOdocAttr(eof) {
			t.Errorf("[%s] unterminated body: fake attr wrongly detected: %q", tag, eof)
		}
	}

	// The close-continue behavior also lets a real stampable tag after the close
	// be harvested (not just detected as an attr).
	got := StampAids(`<body><script>x='<img>'</script/><section>real</section></body>`)
	if len(got.AIDs) != 1 || got.AIDs[0].Tag != "section" {
		t.Errorf("stampable after </script/> close not harvested: %#v", got.AIDs)
	}
}

// P1(findCloseEnd comment-aware): a same-tag open/close that appears only inside
// an HTML comment must not move nesting, so the real close is found and the
// following sibling stays intact. Covers normal and malformed/unterminated
// comments, plus a fake same-tag OPEN in a comment (which used to corrupt depth).
func TestFindCloseEndSkipsCommentFakeTags(t *testing.T) {
	// Reviewer's exact case: the fake </section> in the comment must be ignored;
	// the section spans through the REAL </section>, and <figure>after</figure>
	// remains a separate stamped sibling.
	in := `<body><section><!-- </section> --><p>real</p></section><figure>after</figure></body>`
	res := StampAids(in)
	if len(res.AIDs) != 2 {
		t.Fatalf("want section+figure, got %#v", res.AIDs)
	}
	sec, _, ok := ElementByAID(res.HTML, aidOf(res, "section"))
	if !ok {
		t.Fatal("section not addressable")
	}
	// The section's outer must contain the real inner <p> and end at the REAL close,
	// not the comment's fake one, so "real" is inside and "after" is NOT.
	if !strings.Contains(sec, "<p>real</p>") || strings.Contains(sec, "after") {
		t.Errorf("section boundary wrong (comment fake-close leaked): %q", sec)
	}
	// A fake same-tag OPEN inside a comment must not inflate depth and swallow the
	// real close: the section still ends at its real </section>.
	in2 := `<body><section><!-- <section> --><p>x</p></section><figure>after</figure></body>`
	res2 := StampAids(in2)
	if len(res2.AIDs) != 2 {
		t.Fatalf("fake open in comment corrupted nesting: %#v", res2.AIDs)
	}
	sec2, _, _ := ElementByAID(res2.HTML, aidOf(res2, "section"))
	if strings.Contains(sec2, "after") {
		t.Errorf("fake comment open inflated depth, swallowed sibling: %q", sec2)
	}
	// Malformed/unterminated comment holding a fake close: still ignored.
	in3 := `<body><section><p>y</p></section><!-- </section>`
	res3 := StampAids(in3)
	if len(res3.AIDs) != 1 {
		t.Errorf("unterminated comment fake-close changed harvest: %#v", res3.AIDs)
	}
}

// P1(findCloseEnd raw-text-aware): a same-tag close inside a nested raw-text body
// (script/style/textarea/title) is text, so nesting is unaffected and the real
// close bounds the element.
func TestFindCloseEndSkipsRawTextFakeClose(t *testing.T) {
	in := `<body><section><script>var s='</section>';</script><p>real</p></section><figure>after</figure></body>`
	res := StampAids(in)
	if len(res.AIDs) != 2 {
		t.Fatalf("want section+figure, got %#v", res.AIDs)
	}
	sec, _, ok := ElementByAID(res.HTML, aidOf(res, "section"))
	if !ok {
		t.Fatal("section not addressable")
	}
	if !strings.Contains(sec, "<p>real</p>") || strings.Contains(sec, "after") {
		t.Errorf("raw-text fake close leaked into section boundary: %q", sec)
	}
}

// P1(findCloseEnd nested same-name): a genuinely nested same-name element must be
// balanced so the OUTER close bounds the outer element (inner stays inside).
func TestFindCloseEndNestedSameName(t *testing.T) {
	in := `<body><section><section><p>inner</p></section><p>outer</p></section><figure>after</figure></body>`
	res := StampAids(in)
	// Two sections + figure.
	var sections, figures int
	for _, a := range res.AIDs {
		switch a.Tag {
		case "section":
			sections++
		case "figure":
			figures++
		}
	}
	if sections != 2 || figures != 1 {
		t.Fatalf("nested same-name harvest wrong: %#v", res.AIDs)
	}
	// The FIRST (outer) section in document order must contain the inner one and
	// the "outer" paragraph, and must NOT swallow the following <figure>.
	sec, _, ok := ElementByAID(res.HTML, aidOf(res, "section"))
	if !ok {
		t.Fatal("outer section not addressable")
	}
	if !strings.Contains(sec, "<p>inner</p>") || !strings.Contains(sec, "<p>outer</p>") || strings.Contains(sec, "after") {
		t.Errorf("outer section boundary wrong for nested same-name: %q", sec)
	}
	// The trailing <figure> sibling is a separate, addressable element (not
	// swallowed by the balanced sections).
	fig, ftag, fok := ElementByAID(res.HTML, aidOf(res, "figure"))
	if !fok || ftag != "figure" || !strings.Contains(fig, "after") {
		t.Errorf("figure sibling not independently addressable: %q %q %v", fig, ftag, fok)
	}
}

// P2(findCloseEnd browser-aligned close tails): the normal element close must be
// recognized for </section>, </section >, </section/>, </section x>, and mixed
// case, and innerHTML/outer must be computed from the ACTUAL close start (never
// by subtracting len("</section>")). </sectionx> must NOT close a section.
func TestFindCloseEndBrowserAlignedTails(t *testing.T) {
	tails := []string{`</section>`, `</section >`, `</section/>`, `</section x>`, `</SECTION>`, `</Section  y>`}
	for _, tail := range tails {
		in := `<body><section><p>real</p>` + tail + `<figure>after</figure></body>`
		res := StampAids(in)
		if len(res.AIDs) != 2 {
			t.Fatalf("tail %q: want section+figure, got %#v", tail, res.AIDs)
		}
		sec, tag, ok := ElementByAID(res.HTML, aidOf(res, "section"))
		if !ok || tag != "section" {
			t.Fatalf("tail %q: section not addressable", tail)
		}
		// innerHTML must be exactly the real inner, so "real" is in and "after" out.
		if !strings.Contains(sec, "<p>real</p>") || strings.Contains(sec, "after") {
			t.Errorf("tail %q: boundary wrong (bad close-start): %q", tail, sec)
		}
	}
	// </sectionx> is not a section close: the section runs unbounded, so it never
	// finds a close and the figure is not swallowed into a bogus inner range. With
	// no real close, the section is harvested with empty inner (openEnd==closeStart)
	// but the figure sibling is still independently harvested.
	in := `<body><section><p>x</p></sectionx><figure>after</figure></body>`
	res := StampAids(in)
	var figures int
	for _, a := range res.AIDs {
		if a.Tag == "figure" {
			figures++
		}
	}
	if figures != 1 {
		t.Errorf("</sectionx> non-close broke figure harvest: %#v", res.AIDs)
	}
}

// P2(ElementByAID/Replace with tail close): ElementByAID returns the full exact
// outer (open through the real close tail), and ReplaceElementByAIDAt removes
// exactly the whole target, leaving the after-sibling intact.
func TestReplaceByAIDWithBrowserAlignedTail(t *testing.T) {
	// Stamp so the section and figure get aids, using a slash-tail close.
	in := `<body><section><p>old</p></section/><figure>keep</figure></body>`
	stamped := StampAids(in)
	sectionAID := aidOf(stamped, "section")
	if sectionAID == "" {
		t.Fatalf("no section aid: %#v", stamped.AIDs)
	}
	// ElementByAID returns the exact outer including the "/>" close tail.
	outer, tag, ok := ElementByAID(stamped.HTML, sectionAID)
	if !ok || tag != "section" {
		t.Fatalf("section lookup failed: %q %v", tag, ok)
	}
	if !strings.HasPrefix(outer, "<section") || !strings.HasSuffix(outer, "</section/>") {
		t.Errorf("outer not the exact full element with tail close: %q", outer)
	}
	if strings.Contains(outer, "keep") {
		t.Errorf("outer over-ran into the sibling: %q", outer)
	}
	// Replace removes exactly the whole target; the after-sibling survives verbatim.
	out, boundary, ok := ReplaceElementByAIDAt(stamped.HTML, sectionAID, `<section><p>new</p></section>`)
	if !ok {
		t.Fatal("replace by aid missed")
	}
	if strings.Contains(out, "old") || !strings.Contains(out, "new") {
		t.Errorf("replace did not swap the target exactly: %q", out)
	}
	// The figure sibling and its content are intact and after the boundary.
	if !strings.Contains(out, "<figure") || !strings.Contains(out, "keep") {
		t.Errorf("after-sibling clobbered by replace: %q", out)
	}
	if boundary < 0 || !strings.HasPrefix(out[boundary:], "<section") {
		t.Errorf("boundary %d does not point at the replacement root: %q", boundary, out)
	}
}

// P2(innerHTML hash stability across tails): the aid is a hash of innerHTML, so
// two documents whose only difference is the close-tag TAIL form must produce the
// SAME section aid (innerHTML is identical; the tail is not part of it).
func TestFindCloseEndInnerHashStableAcrossTails(t *testing.T) {
	base := StampAids(`<body><section><p>same</p></section></body>`)
	baseAID := aidOf(base, "section")
	for _, tail := range []string{`</section >`, `</section/>`, `</section x>`, `</SECTION>`} {
		got := StampAids(`<body><section><p>same</p>` + tail + `</body>`)
		gotAID := aidOf(got, "section")
		if gotAID != baseAID {
			t.Errorf("tail %q changed innerHTML hash: got %q want %q", tail, gotAID, baseAID)
		}
	}
}

// aidOf returns the aid of the FIRST element with the given tag, matching the
// document-order resolution ElementByAID uses.
func aidOf(res StampResult, tag string) string {
	for _, a := range res.AIDs {
		if a.Tag == tag {
			return a.AID
		}
	}
	return ""
}

// P1(#1 harvest self-close): a trailing slash on a NON-void stampable tag is NOT
// a self-close in HTML. <section/><figure>inside</figure></section> keeps the
// section open through its REAL </section>, so the nested figure is inside it and
// the section's boundary (ElementByAID outer) spans to the real close, never
// stopping at the phantom "self-close" slash. harvest, ElementByAID, and
// SingleTopLevelTag must all agree.
func TestHarvestNonVoidTrailingSlashNotSelfClose(t *testing.T) {
	in := `<body><section/><figure>inside</figure></section></body>`
	res := StampAids(in)
	// The section is non-void: it runs to </section>, wrapping the figure. Both
	// the section and the nested figure are harvested (2 aids).
	if len(res.AIDs) != 2 {
		t.Fatalf("want section+figure harvested (non-void slash), got %#v", res.AIDs)
	}
	sec, tag, ok := ElementByAID(res.HTML, aidOf(res, "section"))
	if !ok || tag != "section" {
		t.Fatalf("section not addressable: tag=%q ok=%v", tag, ok)
	}
	// The section boundary must run THROUGH the real close, so the nested figure
	// content is inside it and the outer ends at </section>.
	if !strings.Contains(sec, "inside") {
		t.Errorf("section boundary stopped at phantom self-close (figure not inside): %q", sec)
	}
	if !strings.HasSuffix(sec, "</section>") {
		t.Errorf("section outer did not end at the real </section>: %q", sec)
	}
	// The whole thing IS a single top-level <section> (wrapping the figure through
	// its real close) — not two siblings — so SingleTopLevelTag accepts it as a
	// section, proving the slash was not read as a self-close boundary.
	if tag, ok := SingleTopLevelTag(`<section/><figure>inside</figure></section>`); !ok || tag != "section" {
		t.Errorf("phantom self-closed section not treated as one section element: tag=%q ok=%v", tag, ok)
	}
	// But a self-closed non-void with NO real close and a trailing sibling is not a
	// single top-level element (the browser would swallow the sibling; we reject).
	if _, ok := SingleTopLevelTag(`<section/><p>x</p>`); ok {
		t.Error("self-closed section + bare sibling (no close) wrongly accepted")
	}
}

// P1(#1 replace self-close): ReplaceElementByAIDAt must swap the WHOLE non-void
// element (through its real close), not just the phantom self-closed open tag.
func TestReplaceNonVoidTrailingSlashSpansRealClose(t *testing.T) {
	in := `<body><section/><p>keep-inside</p></section><figure>after</figure></body>`
	stamped := StampAids(in)
	secAID := aidOf(stamped, "section")
	if secAID == "" {
		t.Fatalf("no section aid: %#v", stamped.AIDs)
	}
	out, boundary, ok := ReplaceElementByAIDAt(stamped.HTML, secAID, `<section><p>new</p></section>`)
	if !ok {
		t.Fatal("replace missed the section")
	}
	// The whole section (through </section>) was replaced: old inner is gone, the
	// after-sibling figure survives verbatim.
	if strings.Contains(out, "keep-inside") {
		t.Errorf("replace stopped at phantom self-close; inner survived: %q", out)
	}
	if !strings.Contains(out, "<figure") || !strings.Contains(out, "after") {
		t.Errorf("after-sibling clobbered: %q", out)
	}
	if boundary < 0 || !strings.HasPrefix(out[boundary:], "<section") {
		t.Errorf("boundary %d not at replacement root: %q", boundary, out)
	}
}

func TestNestedSameTagNonVoidSlashSpansMatchingClose(t *testing.T) {
	const input = `<body><section/><section>two</section></section><figure>after</figure></body>`
	stamped := StampAids(input)
	var sections []StampedArtifact
	for _, artifact := range stamped.AIDs {
		if artifact.Tag == "section" {
			sections = append(sections, artifact)
		}
	}
	if len(sections) != 2 {
		t.Fatalf("sections = %#v, want outer and inner", sections)
	}

	outer, tag, ok := ElementByAID(stamped.HTML, sections[0].AID)
	if !ok || tag != "section" || !strings.Contains(outer, `><section data-odoc-aid="`+sections[1].AID+`">two</section></section>`) {
		t.Fatalf("outer section boundary = %q tag=%q ok=%v", outer, tag, ok)
	}
	inner, tag, ok := ElementByAID(stamped.HTML, sections[1].AID)
	wantInner := `<section data-odoc-aid="` + sections[1].AID + `">two</section>`
	if !ok || tag != "section" || inner != wantInner {
		t.Fatalf("inner section boundary = %q tag=%q ok=%v; want %q", inner, tag, ok, wantInner)
	}

	figureAID := aidOf(stamped, "figure")
	figure, tag, ok := ElementByAID(stamped.HTML, figureAID)
	if !ok || tag != "figure" || figure != `<figure data-odoc-aid="`+figureAID+`">after</figure>` {
		t.Fatalf("following sibling = %q tag=%q ok=%v", figure, tag, ok)
	}

	replaced, boundary, ok := ReplaceElementByAIDAt(stamped.HTML, sections[0].AID, `<section>new</section>`)
	if !ok || boundary != len(`<body>`) || replaced != `<body><section>new</section>`+figure+`</body>` {
		t.Fatalf("outer replace = %q boundary=%d ok=%v", replaced, boundary, ok)
	}
	innerReplaced, _, ok := ReplaceElementByAIDAt(stamped.HTML, sections[1].AID, `<section>inner-new</section>`)
	wantInnerReplaced := strings.Replace(stamped.HTML, inner, `<section>inner-new</section>`, 1)
	if !ok || innerReplaced != wantInnerReplaced {
		t.Fatalf("inner replace crossed boundary: %q ok=%v", innerReplaced, ok)
	}
	if again := StampAids(stamped.HTML); again.HTML != stamped.HTML {
		t.Fatalf("re-stamp not idempotent:\n first %q\nsecond %q", stamped.HTML, again.HTML)
	}
}

// P1(#2 iframe non-void): iframe is a NORMAL element that may hold fallback
// content, so it is NOT void. Its boundary runs through the real </iframe> and
// ElementByAID/replace include the full element with its fallback children.
func TestIframeIsNonVoid(t *testing.T) {
	in := `<body><iframe src="x"><p>fallback</p></iframe></body>`
	res := StampAids(in)
	if len(res.AIDs) != 1 || res.AIDs[0].Tag != "iframe" {
		t.Fatalf("iframe not harvested as a single element: %#v", res.AIDs)
	}
	outer, tag, ok := ElementByAID(res.HTML, aidOf(res, "iframe"))
	if !ok || tag != "iframe" {
		t.Fatalf("iframe not addressable: tag=%q ok=%v", tag, ok)
	}
	// The outer must be the FULL iframe: through </iframe>, including the fallback.
	if !strings.HasPrefix(outer, "<iframe") || !strings.HasSuffix(outer, "</iframe>") {
		t.Errorf("iframe outer not a full non-void element: %q", outer)
	}
	if !strings.Contains(outer, "<p>fallback</p>") {
		t.Errorf("iframe fallback content not inside the element boundary: %q", outer)
	}
	// The aid is stamped on the OPEN tag (not self-terminating), and there is a
	// real close tag in the output.
	if !strings.Contains(res.HTML, `<iframe src="x" data-odoc-aid="`) {
		t.Errorf("iframe open tag not stamped as a normal element: %q", res.HTML)
	}
	if !strings.Contains(res.HTML, "</iframe>") {
		t.Errorf("iframe close tag missing (treated as void?): %q", res.HTML)
	}
	// A self-terminating slash on iframe is NOT a self-close: the body runs to the
	// real close, so following siblings are not swallowed.
	in2 := `<body><iframe src="x"/><section>sib</section></iframe></body>`
	res2 := StampAids(in2)
	iframeOuter, _, ok := ElementByAID(res2.HTML, aidOf(res2, "iframe"))
	if !ok {
		t.Fatalf("iframe (slash) not addressable: %#v", res2.AIDs)
	}
	if !strings.Contains(iframeOuter, "sib") {
		t.Errorf("iframe slash treated as self-close, sibling not inside real boundary: %q", iframeOuter)
	}
}

// P1(#3 probe tag-name tokenization): a tag-name PREFIX confusion form like
// <section:x> must NOT be parsed as <section> (the ':' is not an HTML name
// terminator), so no phantom stamp/AID is minted and the document is unchanged.
// Normal tags, hyphenated custom names, and mixed case still parse.
func TestProbeTagNamePrefixConfusion(t *testing.T) {
	// Prefix-confusion forms: none of these are a real <section>/<figure>/... so
	// they are neither harvested nor mutated.
	noHarvest := []string{
		`<body><section:x>content</section:x></body>`,
		`<body><figure:y>c</figure:y></body>`,
		`<body><svg:z></svg:z></body>`,
		`<body><section.x>c</section.x></body>`, // '.' also not a terminator
	}
	for _, in := range noHarvest {
		res := StampAids(in)
		if len(res.AIDs) != 0 {
			t.Errorf("prefix-confusion form phantom-harvested: %q -> %#v", in, res.AIDs)
		}
		if res.HTML != in {
			t.Errorf("prefix-confusion form mutated HTML:\n in  %q\n out %q", in, res.HTML)
		}
	}
	// HasDataOdocAttr must not mint a match off a prefix-confusion open tag.
	if HasDataOdocAttr(`<section:x data-odoc-aid="forged">c</section:x>`) {
		t.Error("prefix-confusion tag wrongly parsed; forged data-odoc detected")
	}
	// Normal stampable tags still work.
	ok := StampAids(`<body><section>real</section></body>`)
	if len(ok.AIDs) != 1 || ok.AIDs[0].Tag != "section" {
		t.Fatalf("normal section broke under tightened tokenization: %#v", ok.AIDs)
	}
	// Mixed case still parses.
	mixed := StampAids(`<body><SECTION>real</SECTION></body>`)
	if len(mixed.AIDs) != 1 || mixed.AIDs[0].Tag != "section" {
		t.Errorf("mixed-case section regressed: %#v", mixed.AIDs)
	}
	// Hyphenated custom-element name with the opt-in class is still harvestable
	// (name terminates at whitespace before class, and at '>' when bare).
	if !IsHarvestableReplacementRoot(`<my-widget class="odoc-artifact">x</my-widget>`) {
		t.Error("hyphenated custom name with opt-in wrongly rejected")
	}
	custom := StampAids(`<body><my-widget class="odoc-artifact">x</my-widget></body>`)
	if len(custom.AIDs) != 1 || custom.AIDs[0].Tag != "my-widget" {
		t.Errorf("hyphenated custom opt-in not harvested: %#v", custom.AIDs)
	}
	// SingleTopLevelTag also rejects the prefix-confusion form.
	if _, ok := SingleTopLevelTag(`<section:x>c</section:x>`); ok {
		t.Error("SingleTopLevelTag accepted a prefix-confusion tag")
	}
}

// P2(#4 comment --!> terminator): the browser closes an HTML comment on either
// "-->" or "--!>". commentEnd must recognize "--!>", choose the EARLIEST valid
// terminator, and keep a following real element visible. The abrupt <!--> /
// <!---> forms and unterminated behavior are retained.
func TestCommentEndBangTerminator(t *testing.T) {
	// "--!>" closes the comment, so the following <div data-odoc-aid> is a REAL
	// element and its attribute IS detected.
	if !HasDataOdocAttr(`<!-- c --!><div data-odoc-aid="real">x</div>`) {
		t.Error(`"--!>" not recognized as a comment close; following real attr missed`)
	}
	// A stampable tag after "--!>" is harvested as a real element.
	res := StampAids(`<body><!-- c --!><section>real</section></body>`)
	if len(res.AIDs) != 1 || res.AIDs[0].Tag != "section" {
		t.Errorf("real <section> after --!> not harvested: %#v", res.AIDs)
	}
	// Fake markup INSIDE a --!>-terminated comment stays comment text.
	if HasDataOdocAttr(`<!-- <div data-odoc-aid="fake"> --!><p>y</p>`) {
		t.Error("fake attr inside a --!>-closed comment wrongly detected")
	}
	// Earliest-terminator: "-->" appears before "--!>", so the comment ends at the
	// "-->" and the text after it (a real element) is visible.
	if !HasDataOdocAttr(`<!-- a --> <div data-odoc-aid="real"> --!>`) {
		t.Error("earliest terminator not chosen when --> precedes --!>")
	}
	// Earliest-terminator the other way: "--!>" appears before "-->", so the
	// comment ends at "--!>" and the following real element is visible.
	if !HasDataOdocAttr(`<!-- a --!><div data-odoc-aid="real"> -->`) {
		t.Error("earliest terminator not chosen when --!> precedes -->")
	}
	// Following real element is not swallowed: it is independently addressable.
	res2 := StampAids(`<body><section>one</section><!-- x --!><figure>two</figure></body>`)
	if len(res2.AIDs) != 2 {
		t.Fatalf("real elements around a --!> comment not both harvested: %#v", res2.AIDs)
	}
	fig, ftag, fok := ElementByAID(res2.HTML, aidOf(res2, "figure"))
	if !fok || ftag != "figure" || !strings.Contains(fig, "two") {
		t.Errorf("figure after --!> comment not independently addressable: %q %q %v", fig, ftag, fok)
	}
	// Retained: abrupt <!--> and <!---> still close an EMPTY comment (not affected
	// by the bang handling), so following real content is visible.
	if !HasDataOdocAttr(`<!--><div data-odoc-aid="y">z</div>`) {
		t.Error("abrupt <!--> regressed after adding --!> handling")
	}
	if !HasDataOdocAttr(`<!---><div data-odoc-aid="y">z</div>`) {
		t.Error("abrupt <!---> regressed after adding --!> handling")
	}
	// Retained: an unterminated comment (no --> and no --!>) consumes the rest, so
	// a real-looking attr after "<!--" is comment text, not a match.
	if HasDataOdocAttr(`<!-- <div data-odoc-aid="x">`) {
		t.Error("unterminated comment behavior regressed")
	}
	// A comment terminated ONLY by "--!>" with no "-->" anywhere is closed there.
	res3 := StampAids(`<body><!-- only bang --!><section>real</section></body>`)
	if len(res3.AIDs) != 1 || res3.AIDs[0].Tag != "section" {
		t.Errorf("comment closed only by --!> did not release following element: %#v", res3.AIDs)
	}
}

func TestTerminalSlashAttributeParsing(t *testing.T) {
	t.Run("void values and true markers", func(t *testing.T) {
		tests := []struct {
			in        string
			wantSrc   string
			wantClose bool
		}{
			{`<body><img src=http://example/x/></body>`, `src=http://example/x/`, false},
			{`<body><img src="http://example/x/"></body>`, `src="http://example/x/"`, false},
			{`<body><img src=http://example/x/ /></body>`, `src=http://example/x/`, true},
			{`<body><img src="http://example/x/"/></body>`, `src="http://example/x/"`, true},
			{`<body><img src="http://example/x/"/ ></body>`, `src="http://example/x/"`, false},
		}
		for _, tt := range tests {
			res := StampAids(tt.in)
			if !strings.Contains(res.HTML, tt.wantSrc) {
				t.Fatalf("trailing value slash changed:\n in %q\nout %q", tt.in, res.HTML)
			}
			img, tag, ok := ElementByAID(res.HTML, aidOf(res, "img"))
			if !ok || tag != "img" || !strings.Contains(img, tt.wantSrc) {
				t.Fatalf("img lookup lost exact value: %q tag=%q ok=%v", img, tag, ok)
			}
			if got := strings.HasSuffix(img, `/>`); got != tt.wantClose {
				t.Fatalf("self-close classification = %v, want %v: %q", got, tt.wantClose, img)
			}
			if again := StampAids(res.HTML); again.HTML != res.HTML {
				t.Fatalf("re-stamp not idempotent:\n first %q\nsecond %q", res.HTML, again.HTML)
			}
		}
	})

	t.Run("foreign unquoted slash spans real close", func(t *testing.T) {
		res := StampAids(`<body><svg data=x/><figure>after</figure></svg><aside>outside</aside></body>`)
		svgAID := aidOf(res, "svg")
		svg, tag, ok := ElementByAID(res.HTML, svgAID)
		if !ok || tag != "svg" || !strings.Contains(svg, `data=x/ data-odoc-aid="`+svgAID+`">`) || !strings.HasSuffix(svg, `</svg>`) || !strings.Contains(svg, `>after</figure>`) {
			t.Fatalf("foreign value slash closed early: %q tag=%q ok=%v", svg, tag, ok)
		}
		replaced, _, replacedOK := ReplaceElementByAIDAt(res.HTML, svgAID, `<svg data=y></svg>`)
		if !replacedOK || strings.Contains(replaced, `>after</figure>`) || !strings.Contains(replaced, `<aside data-odoc-aid=`) {
			t.Fatalf("replace did not use real svg boundary: %q ok=%v", replaced, replacedOK)
		}
		if again := StampAids(res.HTML); again.HTML != res.HTML {
			t.Fatalf("re-stamp not idempotent:\n first %q\nsecond %q", res.HTML, again.HTML)
		}
	})

	t.Run("opt-in acceptance agrees with harvesting", func(t *testing.T) {
		const fragment = `<div class=odoc-artifact/></div>`
		if IsHarvestableReplacementRoot(fragment) {
			t.Fatal("unquoted trailing slash was stripped from class token during acceptance")
		}
		res := StampAids(fragment)
		if len(res.AIDs) != 0 || res.HTML != fragment {
			t.Fatalf("harvesting disagreed with acceptance: %#v %q", res.AIDs, res.HTML)
		}
	})
}

// P1(foreign self-close): StampAids on a genuinely self-closing foreign element
// (<svg/>) must insert the aid BEFORE the terminal slash, keeping the slash final
// so self-closing SVG semantics survive and the following sibling is NOT absorbed.
func TestStampAidsForeignSelfCloseSVG(t *testing.T) {
	res := StampAids(`<body><svg/><section>after</section></body>`)
	// Exact reconstruction: aid before the slash, slash final.
	if want := `<body><svg data-odoc-aid="` + aidOf(res, "svg") + `"/><section data-odoc-aid="` + aidOf(res, "section") + `">after</section></body>`; res.HTML != want {
		t.Fatalf("svg self-close reconstruction:\n got %q\nwant %q", res.HTML, want)
	}
	// No stray "/ " slash-before-aid regression.
	if strings.Contains(res.HTML, `/ data-odoc-aid`) {
		t.Errorf("stray slash before aid: %q", res.HTML)
	}
	// The following section sibling is intact and not swallowed by the svg.
	if len(res.AIDs) != 2 {
		t.Fatalf("want svg+section harvested (sibling not absorbed), got %#v", res.AIDs)
	}
	sec, stag, sok := ElementByAID(res.HTML, aidOf(res, "section"))
	if !sok || stag != "section" || sec != `<section data-odoc-aid="`+aidOf(res, "section")+`">after</section>` {
		t.Errorf("following section not intact: %q tag=%q ok=%v", sec, stag, sok)
	}
	// ElementByAID on the svg returns exactly the self-closing open tag (empty inner).
	svg, vtag, vok := ElementByAID(res.HTML, aidOf(res, "svg"))
	if !vok || vtag != "svg" || svg != `<svg data-odoc-aid="`+aidOf(res, "svg")+`"/>` {
		t.Errorf("svg outer not exact self-close: %q tag=%q ok=%v", svg, vtag, vok)
	}
	// Re-stamping already-stamped output is idempotent (byte-identical).
	if res2 := StampAids(res.HTML); res2.HTML != res.HTML {
		t.Errorf("re-stamp not idempotent:\n first %q\nsecond %q", res.HTML, res2.HTML)
	}
	// ReplaceElementByAIDAt swaps only the svg; the section sibling stays intact.
	out, _, ok := ReplaceElementByAIDAt(res.HTML, aidOf(res, "svg"), `<svg width="9"/>`)
	if !ok || out != `<body><svg width="9"/><section data-odoc-aid="`+aidOf(res, "section")+`">after</section></body>` {
		t.Errorf("replace svg left sibling changed: %q ok=%v", out, ok)
	}
}

// Regression guard: an HTML non-void tag written self-closed (<section/>) is NOT a
// self-close — the aid must NOT be moved before the slash. It spans to the real
// </section>, so it stays reconstructed as an ordinary open tag (slash retained
// in place, aid after), never converted to "<section .../>".
func TestStampAidsHTMLSectionSlashStaysNonVoid(t *testing.T) {
	res := StampAids(`<body><section/><figure>inside</figure></section></body>`)
	// The section reconstruction keeps the phantom slash where it was, aid after it,
	// and does NOT end the open tag with "/>".
	if !strings.Contains(res.HTML, `<section/ data-odoc-aid="`) {
		t.Errorf("HTML <section/> reconstruction changed (should keep non-void form): %q", res.HTML)
	}
	if strings.Contains(res.HTML, `<section data-odoc-aid="`+aidOf(res, "section")+`"/>`) {
		t.Errorf("HTML <section/> wrongly reconstructed as self-closing: %q", res.HTML)
	}
	// Boundary still runs through the real </section>, wrapping the figure.
	sec, tag, ok := ElementByAID(res.HTML, aidOf(res, "section"))
	if !ok || tag != "section" || !strings.HasSuffix(sec, "</section>") || !strings.Contains(sec, "inside") {
		t.Errorf("section boundary not through real close: %q tag=%q ok=%v", sec, tag, ok)
	}
}

func TestStampAidsForeignIntegrationPoints(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{
			name: "svg foreignObject HTML descendant",
			html: `<body><svg><foreignObject><section/><figure>inside</figure></section><aside>svg-sibling</aside></foreignObject></svg><figure>after</figure></body>`,
		},
		{
			name: "MathML text integration point",
			html: `<body><math><mtext><section/><figure>inside</figure></section><aside>math-sibling</aside></mtext></math><figure>after</figure></body>`,
		},
		{
			name: "MathML annotation XML integration point",
			html: `<body><math><annotation-xml encoding="text/html"><section/><figure>inside</figure></section></annotation-xml></math><figure>after</figure></body>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := StampAids(tt.html)
			assertUniqueAIDs(t, res)
			sectionAID := aidOf(res, "section")
			section, tag, ok := ElementByAID(res.HTML, sectionAID)
			if !ok || tag != "section" || !strings.Contains(section, `<figure`) || !strings.Contains(section, `inside</figure></section>`) {
				t.Fatalf("HTML integration descendant treated as foreign self-close: %q tag=%q ok=%v", section, tag, ok)
			}
			if strings.Contains(section, `<section data-odoc-aid="`+sectionAID+`"/>`) {
				t.Fatalf("section reconstructed as foreign self-close: %q", section)
			}
			lastFigureAID := res.AIDs[len(res.AIDs)-1].AID
			after, afterTag, afterOK := ElementByAID(res.HTML, lastFigureAID)
			if !afterOK || afterTag != "figure" || !strings.Contains(after, ">after</figure>") {
				t.Fatalf("following sibling boundary lost: %q tag=%q ok=%v", after, afterTag, afterOK)
			}
			replaced, _, replaceOK := ReplaceElementByAIDAt(res.HTML, sectionAID, `<section>new</section>`)
			if !replaceOK || !strings.Contains(replaced, `<section>new</section>`) || !strings.Contains(replaced, `>after</figure>`) || strings.Contains(replaced, `inside</figure>`) {
				t.Fatalf("replacement boundary not exact: %q ok=%v", replaced, replaceOK)
			}
			if again := StampAids(res.HTML); again.HTML != res.HTML {
				t.Fatalf("re-stamp not idempotent:\n first %q\nsecond %q", res.HTML, again.HTML)
			}
		})
	}
}

func TestStampAidsForeignIntegrationNestedSVGReentry(t *testing.T) {
	in := `<body><svg><foreignObject><section><svg><section/><figure>outside-inner-svg</figure></svg><figure>html-sibling</figure></section></foreignObject></svg><aside>after</aside></body>`
	res := StampAids(in)
	assertUniqueAIDs(t, res)

	var sections []StampedArtifact
	for _, artifact := range res.AIDs {
		if artifact.Tag == "section" {
			sections = append(sections, artifact)
		}
	}
	if len(sections) != 2 {
		t.Fatalf("want outer HTML and inner SVG sections, got %#v", res.AIDs)
	}
	outer, _, ok := ElementByAID(res.HTML, sections[0].AID)
	if !ok || !strings.Contains(outer, `html-sibling</figure></section>`) {
		t.Fatalf("outer HTML section boundary wrong: %q ok=%v", outer, ok)
	}
	inner, _, ok := ElementByAID(res.HTML, sections[1].AID)
	if !ok || inner != `<section data-odoc-aid="`+sections[1].AID+`"/>` {
		t.Fatalf("nested SVG did not re-enter foreign context: %q ok=%v", inner, ok)
	}
	var innerSVGFigureAID string
	for _, artifact := range res.AIDs {
		if artifact.Tag == "figure" && strings.Contains(artifact.Head, "outside-inner-svg") {
			innerSVGFigureAID = artifact.AID
		}
	}
	innerSVGFigure, figureTag, figureOK := ElementByAID(res.HTML, innerSVGFigureAID)
	if !figureOK || figureTag != "figure" || !strings.Contains(innerSVGFigure, `>outside-inner-svg</figure>`) {
		t.Fatalf("foreign self-close absorbed following inner-SVG sibling: %q tag=%q ok=%v", innerSVGFigure, figureTag, figureOK)
	}
	if again := StampAids(res.HTML); again.HTML != res.HTML {
		t.Fatalf("re-stamp not idempotent:\n first %q\nsecond %q", res.HTML, again.HTML)
	}
}

func TestTerminalSelfCloseRequiresImmediateGreaterThan(t *testing.T) {
	t.Run("svg", func(t *testing.T) {
		malformed := StampAids(`<body><svg/ ><figure>inside</figure></svg><aside>after</aside></body>`)
		svgAID := aidOf(malformed, "svg")
		figureAID := aidOf(malformed, "figure")
		asideAID := aidOf(malformed, "aside")
		want := `<body><svg/ data-odoc-aid="` + svgAID + `" ><figure data-odoc-aid="` + figureAID + `">inside</figure></svg><aside data-odoc-aid="` + asideAID + `">after</aside></body>`
		if malformed.HTML != want {
			t.Fatalf("malformed svg reconstruction:\n got %q\nwant %q", malformed.HTML, want)
		}
		outer, tag, ok := ElementByAID(malformed.HTML, svgAID)
		if !ok || tag != "svg" || outer != strings.TrimPrefix(strings.TrimSuffix(want, `<aside data-odoc-aid="`+asideAID+`">after</aside></body>`), `<body>`) {
			t.Fatalf("malformed svg lookup = %q tag=%q ok=%v", outer, tag, ok)
		}
		replaced, ok := ReplaceElementByAID(malformed.HTML, svgAID, `<svg/>`)
		if !ok || replaced != `<body><svg/><aside data-odoc-aid="`+asideAID+`">after</aside></body>` {
			t.Fatalf("malformed svg replace = %q ok=%v", replaced, ok)
		}
		if again := StampAids(malformed.HTML); again.HTML != malformed.HTML {
			t.Fatalf("malformed svg restamp changed bytes:\n first %q\nsecond %q", malformed.HTML, again.HTML)
		}

		for _, input := range []string{`<body><svg/><aside>after</aside></body>`, `<body><svg /><aside>after</aside></body>`} {
			res := StampAids(input)
			aid := aidOf(res, "svg")
			aside := aidOf(res, "aside")
			want := `<body><svg data-odoc-aid="` + aid + `"`
			if strings.Contains(input, "svg /") {
				want += ` />`
			} else {
				want += `/>`
			}
			want += `<aside data-odoc-aid="` + aside + `">after</aside></body>`
			if res.HTML != want {
				t.Fatalf("valid svg reconstruction:\n got %q\nwant %q", res.HTML, want)
			}
			outer, tag, ok := ElementByAID(res.HTML, aid)
			if !ok || tag != "svg" || outer != strings.TrimSuffix(strings.TrimPrefix(want, `<body>`), `<aside data-odoc-aid="`+aside+`">after</aside></body>`) {
				t.Fatalf("valid svg lookup = %q tag=%q ok=%v", outer, tag, ok)
			}
			if again := StampAids(res.HTML); again.HTML != res.HTML {
				t.Fatalf("valid svg restamp changed bytes:\n first %q\nsecond %q", res.HTML, again.HTML)
			}
		}
	})

	t.Run("img", func(t *testing.T) {
		tests := []struct {
			input, open string
		}{
			{`<body><img/ ><aside>after</aside></body>`, `<img/ data-odoc-aid="%s" >`},
			{`<body><img/><aside>after</aside></body>`, `<img data-odoc-aid="%s"/>`},
			{`<body><img /><aside>after</aside></body>`, `<img data-odoc-aid="%s" />`},
		}
		for _, tt := range tests {
			res := StampAids(tt.input)
			aid := aidOf(res, "img")
			wantOpen := strings.Replace(tt.open, "%s", aid, 1)
			outer, tag, ok := ElementByAID(res.HTML, aid)
			if !ok || tag != "img" || outer != wantOpen {
				t.Fatalf("img lookup = %q tag=%q ok=%v; want %q", outer, tag, ok, wantOpen)
			}
			replaced, ok := ReplaceElementByAID(res.HTML, aid, `<img src=x>`)
			if !ok || !strings.HasPrefix(replaced, `<body><img src=x><aside`) {
				t.Fatalf("img replace = %q ok=%v", replaced, ok)
			}
			if again := StampAids(res.HTML); again.HTML != res.HTML {
				t.Fatalf("img restamp changed bytes:\n first %q\nsecond %q", res.HTML, again.HTML)
			}
		}
	})
}

func TestStampAidsScansStructureOnce(t *testing.T) {
	var doc strings.Builder
	doc.WriteString(`<body>`)
	for i := 0; i < 4000; i++ {
		doc.WriteString(`<section><figure>item</figure></section>`)
	}
	doc.WriteString(`</body>`)

	stats := &scanStats{}
	opens := scanOpenTagsWithStats(doc.String(), stats)
	if len(opens) != 8001 {
		t.Fatalf("open tags = %d, want 8001", len(opens))
	}
	if stats.stackOps > 20000 {
		t.Fatalf("scanOpenTags stack operations = %d, want <= 20000", stats.stackOps)
	}
}

func TestScanOpenTagsStopsAtUnbalancedAttributeQuote(t *testing.T) {
	in := `<body><div title="BEFORE><figure>AFTER</figure><table><tr><td>AFTER</td></tr></table></body>`
	res := StampAids(in)
	if len(res.AIDs) != 0 {
		t.Fatalf("phantom artifacts = %#v, want none", res.AIDs)
	}
	if res.HTML != in {
		t.Fatalf("unterminated attribute tail was mutated:\n got %q\nwant %q", res.HTML, in)
	}
	if strings.Contains(res.HTML, `data-odoc-aid`) {
		t.Fatalf("aid injected inside browser attribute value: %q", res.HTML)
	}
}

func TestAnnotationXMLParsesEncodingOnce(t *testing.T) {
	var doc strings.Builder
	doc.Grow(480 << 10)
	doc.WriteString(`<math><annotation-xml data-padding="`)
	doc.WriteString(strings.Repeat("x", 440<<10))
	doc.WriteString(`" encoding="text/html">`)
	for i := 0; i < 2500; i++ {
		doc.WriteString(`<section><span>x</span></section>`)
	}
	doc.WriteString(`</annotation-xml></math>`)

	stats := &scanStats{}
	opens := scanOpenTagsWithStats(doc.String(), stats)
	if stats.annotationAttrParses != 1 {
		t.Fatalf("annotation encoding parses = %d, want 1", stats.annotationAttrParses)
	}
	if len(opens) != 5002 {
		t.Fatalf("open tags = %d, want 5002", len(opens))
	}
}

func TestIframeLegacyAIDIsReconciliationOnly(t *testing.T) {
	cases := []struct {
		html      string
		legacyAID string
	}{
		{`<body><iframe src="x"><p>fallback</p></iframe></body>`, "1xf1ckmazcw"},
		{`<body><iframe src="x"></iframe></body>`, "1xf1ckmazcw"},
		{`<body><iframe title="a" src="x">legacy fallback</iframe></body>`, "1pib9z82p75"},
	}
	for _, tc := range cases {
		res := StampAids(tc.html)
		canonicalAID := aidOf(res, "iframe")
		artifact := res.AIDs[0]
		if canonicalAID != tc.legacyAID && (len(artifact.LegacyAIDs) != 1 || artifact.LegacyAIDs[0] != tc.legacyAID) {
			t.Errorf("iframe metadata = %+v, want legacy alias %q for %q", artifact, tc.legacyAID, tc.html)
		}
		if canonicalAID != tc.legacyAID && strings.Contains(res.HTML, `data-odoc-aid="`+tc.legacyAID+`"`) {
			t.Errorf("legacy alias was emitted into HTML: %q", res.HTML)
		}
		outer, _, ok := ElementByAID(res.HTML, canonicalAID)
		if !ok || !strings.HasSuffix(outer, `</iframe>`) {
			t.Errorf("iframe boundary = %q ok=%v", outer, ok)
		}
	}
}

func TestStampAidsStripsAllAIDAttributeForms(t *testing.T) {
	forms := []string{
		`data-odoc-aid="forged"`,
		`data-odoc-aid='forged'`,
		`data-odoc-aid=forged`,
		`DATA-ODOC-AID="forged"`,
		`data-odoc-aid`,
		`data-odoc-aid class=x`,
		`data-odoc-aid="first" data-odoc-aid='second'`,
	}
	for _, attrs := range forms {
		t.Run(attrs, func(t *testing.T) {
			first := StampAids(`<body><section ` + attrs + `>x</section></body>`)
			if len(first.AIDs) != 1 {
				t.Fatalf("aid count = %d: %q", len(first.AIDs), first.HTML)
			}
			want := `data-odoc-aid="` + first.AIDs[0].AID + `"`
			if strings.Count(strings.ToLower(first.HTML), "data-odoc-aid") != 1 || !strings.Contains(first.HTML, want) || strings.Contains(first.HTML, "forged") {
				t.Fatalf("forged aid survived: %q", first.HTML)
			}
			second := StampAids(first.HTML)
			if second.HTML != first.HTML || second.AIDs[0].AID != first.AIDs[0].AID {
				t.Fatalf("restamp changed canonical output:\n first %q\nsecond %q", first.HTML, second.HTML)
			}
		})
	}

	nested := StampAids(`<body><section><div data-odoc-aid='forged' class=x>x</div></section></body>`)
	if strings.Contains(strings.ToLower(nested.HTML), "forged") || !strings.Contains(nested.HTML, `<div class=x>`) {
		t.Fatalf("nested non-artifact aid was not safely stripped: %q", nested.HTML)
	}
	again := StampAids(nested.HTML)
	if again.HTML != nested.HTML || len(again.AIDs) != 1 || again.AIDs[0].AID != nested.AIDs[0].AID {
		t.Fatalf("nested strip was not idempotent: first=%q second=%q", nested.HTML, again.HTML)
	}
}

func TestStampAidsNestedForgedAIDsDoNotAffectAncestorHash(t *testing.T) {
	tests := []struct {
		name  string
		dirty string
		clean string
	}{
		{
			name:  "stampable single quoted",
			dirty: `<body><section><figure data-odoc-aid='forged'>x</figure></section></body>`,
			clean: `<body><section><figure>x</figure></section></body>`,
		},
		{
			name:  "stampable double quoted",
			dirty: `<body><section><figure data-odoc-aid="forged">x</figure></section></body>`,
			clean: `<body><section><figure>x</figure></section></body>`,
		},
		{
			name:  "stampable unquoted",
			dirty: `<body><section><figure data-odoc-aid=forged>x</figure></section></body>`,
			clean: `<body><section><figure>x</figure></section></body>`,
		},
		{
			name:  "stampable valueless",
			dirty: `<body><section><figure data-odoc-aid>x</figure></section></body>`,
			clean: `<body><section><figure>x</figure></section></body>`,
		},
		{
			name:  "stampable mixed case duplicate",
			dirty: `<body><section><figure DATA-ODOC-AID='first' data-odoc-aid=second title=x>x</figure></section></body>`,
			clean: `<body><section><figure title=x>x</figure></section></body>`,
		},
		{
			name:  "opt in single quoted",
			dirty: `<body><section><div class="odoc-artifact" data-odoc-aid='forged'>x</div></section></body>`,
			clean: `<body><section><div class="odoc-artifact">x</div></section></body>`,
		},
		{
			name:  "opt in double quoted",
			dirty: `<body><section><div class="odoc-artifact" data-odoc-aid="forged">x</div></section></body>`,
			clean: `<body><section><div class="odoc-artifact">x</div></section></body>`,
		},
		{
			name:  "opt in unquoted",
			dirty: `<body><section><div class="odoc-artifact" data-odoc-aid=forged>x</div></section></body>`,
			clean: `<body><section><div class="odoc-artifact">x</div></section></body>`,
		},
		{
			name:  "opt in valueless",
			dirty: `<body><section><div class="odoc-artifact" data-odoc-aid>x</div></section></body>`,
			clean: `<body><section><div class="odoc-artifact">x</div></section></body>`,
		},
		{
			name:  "opt in mixed case duplicate",
			dirty: `<body><section><div DATA-ODOC-AID="first" class="odoc-artifact" data-odoc-aid='second'>x</div></section></body>`,
			clean: `<body><section><div class="odoc-artifact">x</div></section></body>`,
		},
		{
			name:  "non void slash belongs to unquoted aid value",
			dirty: `<body><section><figure data-odoc-aid=forged/>x</figure></section></body>`,
			clean: `<body><section><figure>x</figure></section></body>`,
		},
		{
			name:  "void slash belongs to unquoted aid value",
			dirty: `<body><section><img data-odoc-aid=forged/></section></body>`,
			clean: `<body><section><img></section></body>`,
		},
		{
			name:  "foreign slash belongs to unquoted aid value",
			dirty: `<body><section><svg><path data-odoc-aid=forged/></svg></section></body>`,
			clean: `<body><section><svg><path></svg></section></body>`,
		},
		{
			name:  "true explicit foreign self close after unquoted aid",
			dirty: `<body><section><svg><path data-odoc-aid=forged /></svg></section></body>`,
			clean: `<body><section><svg><path /></svg></section></body>`,
		},
		{
			name:  "foreign self closing descendant",
			dirty: `<body><section><svg><path data-odoc-aid='forged'/></svg></section></body>`,
			clean: `<body><section><svg><path/></svg></section></body>`,
		},
		{
			name: "only real attribute names are stripped",
			dirty: `<body><section title="data-odoc-aid='literal'">` +
				`<!-- <figure data-odoc-aid='comment'>fake</figure> -->` +
				`<script>"<figure data-odoc-aid=raw>fake</figure>"</script>` +
				`<textarea><figure data-odoc-aid=rawtext>fake</figure></textarea>` +
				`<figure data-note="data-odoc-aid=literal" data-odoc-aid='real'>x</figure>` +
				`</section></body>`,
			clean: `<body><section title="data-odoc-aid='literal'">` +
				`<!-- <figure data-odoc-aid='comment'>fake</figure> -->` +
				`<script>"<figure data-odoc-aid=raw>fake</figure>"</script>` +
				`<textarea><figure data-odoc-aid=rawtext>fake</figure></textarea>` +
				`<figure data-note="data-odoc-aid=literal">x</figure>` +
				`</section></body>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := StampAids(tt.clean)
			first := StampAids(tt.dirty)
			if first.HTML != want.HTML {
				t.Fatalf("caller AID changed canonical parent/child output:\n got  %q\n want %q", first.HTML, want.HTML)
			}
			if len(first.AIDs) != 2 || len(want.AIDs) != 2 {
				t.Fatalf("artifacts = %#v, want canonical parent and child", first.AIDs)
			}
			for i := range first.AIDs {
				if first.AIDs[i].Tag != want.AIDs[i].Tag || first.AIDs[i].AID != want.AIDs[i].AID {
					t.Fatalf("artifact %d = %+v, want %+v", i, first.AIDs[i], want.AIDs[i])
				}
			}
			second := StampAids(first.HTML)
			if second.HTML != first.HTML {
				t.Fatalf("nested stamp was not idempotent:\n first  %q\n second %q", first.HTML, second.HTML)
			}
		})
	}
}

func TestStampAidsCleanSelfCloseSpacingCompatibility(t *testing.T) {
	tests := []struct {
		name  string
		clean string
		dirty string
		tag   string
	}{
		{
			name:  "foreign opt in unquoted class",
			clean: `<body><section><svg><path class=odoc-artifact /></svg></section></body>`,
			dirty: `<body><section><svg><path class=odoc-artifact data-odoc-aid=forged /></svg></section></body>`,
			tag:   "path",
		},
		{
			name:  "void unquoted src",
			clean: `<body><section><img src=x /></section></body>`,
			dirty: `<body><section><img src=x data-odoc-aid=forged /></section></body>`,
			tag:   "img",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean := StampAids(tt.clean)
			dirty := StampAids(tt.dirty)
			if aidOf(clean, tt.tag) == "" || aidOf(dirty, tt.tag) == "" {
				t.Fatalf("%s ceased to be harvested: clean=%#v dirty=%#v", tt.tag, clean.AIDs, dirty.AIDs)
			}
			if dirty.HTML != clean.HTML {
				t.Fatalf("dirty AID changed clean compatibility output:\n dirty %q\n clean %q", dirty.HTML, clean.HTML)
			}
			if again := StampAids(clean.HTML); again.HTML != clean.HTML {
				t.Fatalf("clean self-close changed on restamp:\n first  %q\n second %q", clean.HTML, again.HTML)
			}
		})
	}
}

func TestStampAidsSelfCloseCanonicalOutputCompatibility(t *testing.T) {
	tests := []struct {
		name  string
		input string
		tag   string
	}{
		{"void unquoted attribute", `<body><img src=x /></body>`, "img"},
		{"foreign unquoted attribute", `<body><svg class=foo /></body>`, "svg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := StampAids(tt.input)
			aid := aidOf(res, tt.tag)
			want := `<body><` + tt.tag + ` `
			if tt.tag == "img" {
				want += `src=x`
			} else {
				want += `class=foo`
			}
			want += ` data-odoc-aid="` + aid + `" /></body>`
			if res.HTML != want {
				t.Fatalf("canonical output changed:\n got  %q\n want %q", res.HTML, want)
			}
			if again := StampAids(res.HTML); again.HTML != res.HTML {
				t.Fatalf("canonical output not idempotent:\n first  %q\n second %q", res.HTML, again.HTML)
			}
		})
	}
}

func TestStampAidsTrueForeignSelfCloseParentIsIdempotent(t *testing.T) {
	dirty := `<body><section><svg data-odoc-aid=x /><path class="odoc-artifact"/></section></body>`
	clean := `<body><section><svg/><path class="odoc-artifact"/></section></body>`

	first := StampAids(dirty)
	want := StampAids(clean)
	if !strings.Contains(first.HTML, `<svg data-odoc-aid="`) || !strings.Contains(want.HTML, `<svg data-odoc-aid="`) {
		t.Fatalf("foreign roots were not stamped: dirty=%q clean=%q", first.HTML, want.HTML)
	}
	if aidOf(first, "section") == "" || aidOf(first, "svg") == "" || aidOf(first, "path") == "" {
		t.Fatalf("missing parent/foreign/opt-in artifacts: %#v", first.AIDs)
	}
	second := StampAids(first.HTML)
	if second.HTML != first.HTML || aidOf(second, "section") != aidOf(first, "section") {
		t.Fatalf("foreign self-close parent changed on restamp:\n first  %q\n second %q", first.HTML, second.HTML)
	}
}

func TestStampAidsFixedPointCorpus(t *testing.T) {
	seeds := []string{
		`<body><section><figure >x</figure></section></body>`,
		"<body><section><figure\n id=\"a\"\n >x</figure></section></body>",
		`<body><SECTION><FIGURE >x</FIGURE></SECTION></body>`,
		`<body><div class="odoc-artifact" ><img  ></div></body>`,
		`<body><iframe src="safe" >fallback</iframe></body>`,
		`<body><svg viewBox="0 0 1 1" ><path class="odoc-artifact" /></svg></body>`,
		`<body><math><annotation-xml encoding="text/html"><figure >x</figure></annotation-xml></math></body>`,
		`<body><svg  /><section>after</section></body>`,
		`<body><section/ ><figure >x</figure></section></body>`,
		`<body><section DATA-ODOC-AID='forged'><figure data-odoc-aid=x >x</figure></section></body>`,
	}
	for _, seed := range seeds {
		first := StampAids(seed)
		second := StampAids(first.HTML)
		if second.HTML != first.HTML {
			t.Fatalf("not byte fixed point for %q:\nfirst  %q\nsecond %q", seed, first.HTML, second.HTML)
		}
		if !reflect.DeepEqual(second.AIDs, first.AIDs) {
			t.Fatalf("not AID fixed point for %q:\nfirst  %#v\nsecond %#v", seed, first.AIDs, second.AIDs)
		}
	}
}

func TestStampAidsForgedAIDWhitespaceMatchesClean(t *testing.T) {
	clean := StampAids(`<body><section><figure>x</figure></section></body>`)
	for _, separator := range []string{"  ", "\t", " \t ", "\n  "} {
		dirty := StampAids(`<body><section><figure` + separator + `data-odoc-aid=forged>x</figure></section></body>`)
		if dirty.HTML != clean.HTML || !reflect.DeepEqual(dirty.AIDs, clean.AIDs) {
			t.Fatalf("separator %q affected canonical output:\ndirty %q\nclean %q", separator, dirty.HTML, clean.HTML)
		}
		if again := StampAids(dirty.HTML); again.HTML != dirty.HTML || !reflect.DeepEqual(again.AIDs, dirty.AIDs) {
			t.Fatalf("separator %q was not a fixed point: first=%q second=%q", separator, dirty.HTML, again.HTML)
		}
	}
}

func TestStampAidsPreservesSlashValueTrailingWhitespace(t *testing.T) {
	in := `<body><section title="/"  >x</section></body>`
	first := StampAids(in)
	if !strings.Contains(first.HTML, `title="/" data-odoc-aid="`) || !strings.Contains(first.HTML, `"  >x`) {
		t.Fatalf("attribute whitespace was not preserved: %q", first.HTML)
	}
	if again := StampAids(first.HTML); again.HTML != first.HTML || !reflect.DeepEqual(again.AIDs, first.AIDs) {
		t.Fatalf("slash-value stamp was not fixed: first=%q second=%q", first.HTML, again.HTML)
	}
}

func TestStampAidsPinnedStripsNestedForgedAID(t *testing.T) {
	const prefix = `<body><section>`
	dirty := prefix + `<figure data-odoc-aid='forged'><img data-odoc-aid=child src=x/></figure></section></body>`
	clean := prefix + `<figure><img src=x/></figure></section></body>`

	got := StampAidsPinned(dirty, "serverpin", len(prefix))
	want := StampAidsPinned(clean, "serverpin", len(prefix))
	if got.HTML != want.HTML {
		t.Fatalf("nested caller AIDs changed pinned output:\n got  %q\n want %q", got.HTML, want.HTML)
	}
	if !strings.Contains(got.HTML, `<figure data-odoc-aid="serverpin">`) {
		t.Fatalf("server pin was not preserved: %q", got.HTML)
	}
	if strings.Contains(got.HTML, "forged") || strings.Contains(got.HTML, "child") {
		t.Fatalf("caller AID survived pinned stamp: %q", got.HTML)
	}
	if again := StampAidsPinned(got.HTML, "serverpin", len(prefix)); again.HTML != got.HTML {
		t.Fatalf("pinned restamp changed bytes:\n first  %q\n second %q", got.HTML, again.HTML)
	}
}

func TestStampAidsMalformedAttributeQuoteRecovery(t *testing.T) {
	tests := []struct {
		name string
		html string
		want int
	}{
		{
			name: "quote becomes attribute-name character after quoted value",
			html: `<html><body><div title="a"b">X</div><figure>REAL</figure><table><tr><td>T</td></tr></table></body></html>`,
			want: 2,
		},
		{
			name: "inch mark in malformed attribute name",
			html: `<html><body><img src="s.png" alt="a 5" screen"><figure>ONE</figure><figure>TWO</figure></body></html>`,
			want: 3,
		},
		{
			name: "quoted value open through eof",
			html: `<html><body><div title="BEFORE><figure>decoy</figure><table>T</table></body></html>`,
			want: 0,
		},
		{
			name: "slash before unterminated quoted attribute",
			html: `<html><body><img/src="BEFORE><figure>decoy</figure><aside>decoy</aside></body></html>`,
			want: 0,
		},
		{
			name: "slash exits attribute name before malformed equals quote",
			html: `<html><body><div a/=">X</div><figure>REAL</figure></body></html>`,
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StampAids(tt.html)
			if got := len(result.AIDs); got != tt.want {
				t.Fatalf("artifact count = %d, want %d: %#v", got, tt.want, result.AIDs)
			}
		})
	}
}

func BenchmarkStampAids4000Sections(b *testing.B) {
	var doc strings.Builder
	doc.WriteString(`<body>`)
	for i := 0; i < 4000; i++ {
		doc.WriteString(`<section><p>text</p></section>`)
	}
	doc.WriteString(`</body>`)
	html := doc.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(html)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StampAids(html)
	}
}

func BenchmarkStampAidsAnnotationXMLAdversarial(b *testing.B) {
	var doc strings.Builder
	doc.Grow(480 << 10)
	doc.WriteString(`<math><annotation-xml data-padding="`)
	doc.WriteString(strings.Repeat("x", 440<<10))
	doc.WriteString(`" encoding="text/html">`)
	for i := 0; i < 2500; i++ {
		doc.WriteString(`<section><span>x</span></section>`)
	}
	doc.WriteString(`</annotation-xml></math>`)
	html := doc.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(html)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StampAids(html)
	}
}

func BenchmarkStampAids4000NestedSections(b *testing.B) {
	var doc strings.Builder
	doc.WriteString(`<body>`)
	for i := 0; i < 4000; i++ {
		doc.WriteString(`<section>`)
	}
	doc.WriteString(`text`)
	for i := 0; i < 4000; i++ {
		doc.WriteString(`</section>`)
	}
	doc.WriteString(`</body>`)
	html := doc.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(html)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StampAids(html)
	}
}

func BenchmarkScanOpenTags4000FlatSections(b *testing.B) {
	var doc strings.Builder
	doc.WriteString(`<body>`)
	for i := 0; i < 4000; i++ {
		doc.WriteString(`<section>text</section>`)
	}
	doc.WriteString(`</body>`)
	html := doc.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(html)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scanOpenTags(html)
	}
}

func BenchmarkScanOpenTags4000NestedSections(b *testing.B) {
	var doc strings.Builder
	doc.WriteString(`<body>`)
	for i := 0; i < 4000; i++ {
		doc.WriteString(`<section>`)
	}
	doc.WriteString(`text`)
	for i := 0; i < 4000; i++ {
		doc.WriteString(`</section>`)
	}
	doc.WriteString(`</body>`)
	html := doc.String()

	b.ReportAllocs()
	b.SetBytes(int64(len(html)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scanOpenTags(html)
	}
}
