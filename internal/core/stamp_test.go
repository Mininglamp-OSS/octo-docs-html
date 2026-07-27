package core

import (
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
// close tag. Only true void tags (img/iframe) may skip the close, with or without
// the trailing slash.
func TestSingleTopLevelTagNonVoidSelfCloseRejected(t *testing.T) {
	reject := []string{
		`<section/>`,         // non-void self-closed, no close tag
		`<div/>`,             // same
		`<section/><p>x</p>`, // self-closed then a sibling
	}
	for _, s := range reject {
		if _, ok := SingleTopLevelTag(s); ok {
			t.Errorf("non-void self-close must be rejected: %q", s)
		}
	}
	accept := []string{
		`<section></section>`, // explicit close is fine
		`<iframe/>`,           // true void with slash
		`<iframe src="x">`,    // true void without slash
		`<img/>`,
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
		`<div><a href="https://example.com">ok</a></div>`,
	}
	for _, s := range pass {
		if _, ok := SafeReplacementFragment(s); !ok {
			t.Errorf("expected safe accept: %q", s)
		}
	}
	fail := []string{
		`<img src=x onerror=alert(1)>`,                   // event handler on a void tag
		`<section onload="x()"><p>y</p></section>`,       // event handler on top-level tag
		`<div><script>alert(1)</script></div>`,           // nested raw-text (inner script)
		`<div><style>.a{}</style></div>`,                 // nested style
		`<div><a href="javascript:alert(1)">x</a></div>`, // javascript: URL
		`<a href="JavaScript:evil()">x</a>`,              // case-insensitive scheme
		`<button onClick="go()">x</button>`,              // mixed-case handler
	}
	for _, s := range fail {
		if _, ok := SafeReplacementFragment(s); ok {
			t.Errorf("expected injection reject: %q", s)
		}
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

// P1(1): pinning must be scoped to the replacement root ONLY. When the replaced
// element is nested inside a stampable ancestor (a <section>), editing it changes
// the ancestor's content — so the ancestor MUST rehash to a new aid rather than
// keep its stale one. We simulate the post-replace document: the ancestor still
// carries its previously-stamped (now stale) aid, and the pinned inner root
// carries the injected old inner aid.
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

// P1(2): a safe replacement root may be a NON-stampable tag (div/p). StampAidsPinned
// must still force-index that root and carry the pinned aid, so a comment anchored
// to it survives reconciliation (which only keeps anchors whose aid is in the
// stamped set).
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
	}
}

// P1(1): a SafeReplacementFragment may carry leading whitespace, so the root '<'
// is not at the fragment start. InjectRootAIDAt reports the root offset; adding it
// to the insertion boundary must point StampAidsPinned at the true root. Covers a
// stampable root (figure) and a non-stampable root (div): both keep/index the old
// aid despite the padding.
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
		if !strings.Contains(res.HTML, `<`+tag+` data-odoc-aid="padaid">`) {
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
