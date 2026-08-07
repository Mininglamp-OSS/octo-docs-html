package service

import (
	"strings"
	"testing"
)

func diffNodePaths(t *testing.T, source string) []string {
	t.Helper()
	nodes, err := parseDiffHTML(source)
	if err != nil {
		t.Fatalf("parseDiffHTML(%q) err = %v", source, err)
	}
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.path != "" {
			out = append(out, node.path)
		}
	}
	return out
}

func hasDiffPath(list []string, want string) bool {
	for _, path := range list {
		if path == want {
			return true
		}
	}
	return false
}

// In an unquoted attribute value only whitespace and '>' end the value, so '/' is
// an ordinary value character. Splitting on it dropped every segment after the
// first into boolean attributes, and because attrs is a map the segment ORDER was
// erased: two different URLs produced the same signature and the change went
// silent.
func TestDiffUnquotedAttributeValueKeepsSlashes(t *testing.T) {
	for _, test := range []struct {
		raw  string
		name string
		want string
	}{
		{" href=docs/v1/intro", "href", "docs/v1/intro"},
		{" href=docs/intro/v1", "href", "docs/intro/v1"},
		{" href=/old", "href", "/old"},
		{" href=/new", "href", "/new"},
		{" href=http://example.com/a/b", "href", "http://example.com/a/b"},
		{" src=a/", "src", "a/"},
		{" src=a /", "src", "a"},
		{" data-x=a/", "data-x", "a/"},
		{` href="docs/v1/intro"`, "href", "docs/v1/intro"},
		{` href='docs/v1/intro'`, "href", "docs/v1/intro"},
	} {
		attrs := parseDiffAttrs(test.raw)
		if got := attrs[test.name]; got != test.want {
			t.Errorf("parseDiffAttrs(%q)[%q] = %q, want %q (full: %v)", test.raw, test.name, got, test.want, attrs)
		}
		// No spurious boolean attributes from split value segments.
		if len(attrs) != 1 {
			t.Errorf("parseDiffAttrs(%q) = %v, want exactly one attribute", test.raw, attrs)
		}
	}
	// Reordering path segments must change the structural signature.
	document := func(href string) string {
		return `<html><body><div><a href=` + href + `>go</a></div></body></html>`
	}
	result, err := buildVersionDiff(1, 2, document("docs/v1/intro"), document("docs/intro/v1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 1 || len(result.Changes) == 0 {
		t.Fatalf("unquoted URL segment swap reported no structural change: %+v", result)
	}
	if !hasDiffPath([]string{result.Changes[0].DOMPath}, "/html[1]/body[1]/div[1]/a[1]") {
		t.Fatalf("change reported at %q, want the <a>", result.Changes[0].DOMPath)
	}
	for _, pair := range [][2]string{
		{"/old", "/new"},
		{"http://a.test/x", "http://b.test/x"},
		{"http://a.test/x", "http://a.test/y"},
	} {
		result, err := buildVersionDiff(1, 2, document(pair[0]), document(pair[1]))
		if err != nil {
			t.Fatal(err)
		}
		if result.Summary.Modified != 1 {
			t.Fatalf("%q -> %q reported no structural change: %+v", pair[0], pair[1], result)
		}
	}
}

// A trailing '/' does not close an ordinary HTML element: the tree builder ignores
// the self-closing flag outside foreign content, so following siblings are
// children. In foreign content (SVG/MathML) the flag DOES close the element, but
// only when it is a real flag and not part of an unquoted attribute value.
func TestDiffSelfClosingFlagFollowsNamespace(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   string
		wantPath string
	}{
		{"unquoted_value_slash", `<html><body><div data-x=a/><p>x</p></div></body></html>`, "/html[1]/body[1]/div[1]/p[1]"},
		{"spaced_flag", `<html><body><div data-x=a /><p>x</p></div></body></html>`, "/html[1]/body[1]/div[1]/p[1]"},
		{"bare_flag", `<html><body><div/><p>x</p></div></body></html>`, "/html[1]/body[1]/div[1]/p[1]"},
		{"section_flag", `<html><body><section/><p>x</p></section></body></html>`, "/html[1]/body[1]/section[1]/p[1]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := diffNodePaths(t, test.source); !hasDiffPath(got, test.wantPath) {
				t.Fatalf("paths = %v, want %s", got, test.wantPath)
			}
		})
	}
	// Void elements never take children, with or without the slash.
	for _, test := range []struct {
		name    string
		source  string
		attr    string
		wantVal string
	}{
		{"void_unquoted_slash", `<html><body><img src=a/><p>x</p></body></html>`, "src", "a/"},
		{"void_spaced_flag", `<html><body><img src=a /><p>x</p></body></html>`, "src", "a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := diffNodePaths(t, test.source)
			if !hasDiffPath(got, "/html[1]/body[1]/p[1]") {
				t.Fatalf("paths = %v, want the <p> as a sibling of <img>", got)
			}
			nodes, err := parseDiffHTML(test.source)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range nodes {
				if node.tag == "img" && node.attrs[test.attr] != test.wantVal {
					t.Fatalf("img %s = %q, want %q", test.attr, node.attrs[test.attr], test.wantVal)
				}
			}
		})
	}
	// Foreign content honours a real self-closing flag.
	for _, test := range []struct {
		name        string
		source      string
		wantSibling string
	}{
		{"svg_quoted_flag", `<html><body><svg><path d="M0 0"/><path d="M1 1"/></svg></body></html>`, "/html[1]/body[1]/svg[1]/path[2]"},
		{"svg_spaced_flag", `<html><body><svg><circle r=1 /><rect x=2 /></svg></body></html>`, "/html[1]/body[1]/svg[1]/rect[1]"},
		{"math_flag", `<html><body><math><mi>x</mi><mspace/><mi>y</mi></math></body></html>`, "/html[1]/body[1]/math[1]/mi[2]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := diffNodePaths(t, test.source); !hasDiffPath(got, test.wantSibling) {
				t.Fatalf("paths = %v, want %s as a sibling", got, test.wantSibling)
			}
		})
	}
	// In foreign content an unquoted value ending in '/' is still not a flag.
	nested := diffNodePaths(t, `<html><body><svg><circle r=1/><rect x=2/></svg></body></html>`)
	if !hasDiffPath(nested, "/html[1]/body[1]/svg[1]/circle[1]/rect[1]") {
		t.Fatalf("paths = %v, want rect nested in circle (r=\"1/\" is a value, not a flag)", nested)
	}
	// Leaving the foreign subtree restores HTML rules.
	afterForeign := diffNodePaths(t, `<html><body><svg><path d="M0 0"/></svg><div/><p>x</p></div></body></html>`)
	if !hasDiffPath(afterForeign, "/html[1]/body[1]/div[1]/p[1]") {
		t.Fatalf("paths = %v, want the <p> inside <div> after the svg subtree", afterForeign)
	}
}

// RAWTEXT elements do not decode character references; only the RCDATA elements
// textarea and title do. The two sets must be derived from one another so they
// cannot drift when the raw-text set grows.
func TestDiffRawTextEntityDecodingMatrix(t *testing.T) {
	for _, tag := range []string{"script", "style", "xmp", "iframe", "noembed", "noframes", "noscript"} {
		t.Run("literal_"+tag, func(t *testing.T) {
			if !isDiffLiteralRawTextTag(tag) {
				t.Fatalf("isDiffLiteralRawTextTag(%q) = false, want true (RAWTEXT does not decode entities)", tag)
			}
			before := `<html><body><` + tag + `>&lt;b&gt;</` + tag + `></body></html>`
			after := `<html><body><` + tag + `><b></` + tag + `></body></html>`
			probe := probeDiff(t, before, after)
			if probe.diffErr != nil {
				t.Fatal(probe.diffErr)
			}
			if probe.modified == 0 || len(probe.changePaths) == 0 {
				t.Fatalf("literal raw-text content change went silent: mod=%d paths=%v hunks=%q",
					probe.modified, probe.changePaths, probe.hunkText)
			}
			if !hasDiffPath(probe.changePaths, "/html[1]/body[1]/"+tag+"[1]") {
				t.Fatalf("paths = %v, want the %s element", probe.changePaths, tag)
			}
		})
	}
	for _, tag := range []string{"textarea", "title"} {
		t.Run("rcdata_"+tag, func(t *testing.T) {
			if isDiffLiteralRawTextTag(tag) {
				t.Fatalf("isDiffLiteralRawTextTag(%q) = true, want false (RCDATA decodes entities)", tag)
			}
			before := `<html><body><` + tag + `>&lt;b&gt;</` + tag + `></body></html>`
			after := `<html><body><` + tag + `><b></` + tag + `></body></html>`
			probe := probeDiff(t, before, after)
			if probe.diffErr != nil {
				t.Fatal(probe.diffErr)
			}
			if probe.modified != 0 || len(probe.changePaths) != 0 {
				t.Fatalf("RCDATA sides decode to the same text but were reported as changed: %+v", probe.changePaths)
			}
		})
	}
	// The literal set must stay derived from the raw-text set.
	for _, tag := range []string{"script", "style", "textarea", "title", "iframe", "noembed", "noframes", "xmp", "noscript"} {
		wantLiteral := tag != "textarea" && tag != "title"
		if isDiffLiteralRawTextTag(tag) != wantLiteral {
			t.Fatalf("isDiffLiteralRawTextTag(%q) = %v, want %v", tag, !wantLiteral, wantLiteral)
		}
	}
	for _, tag := range []string{"div", "p", "span"} {
		if isDiffLiteralRawTextTag(tag) {
			t.Fatalf("isDiffLiteralRawTextTag(%q) = true, want false", tag)
		}
	}
	// Both layers must agree: a literal raw-text edit may not be silent in one and
	// visible in the other.
	before := `<html><body><xmp>&lt;b&gt;</xmp></body></html>`
	after := `<html><body><xmp><b></xmp></body></html>`
	probe := probeDiff(t, before, after)
	if probe.modified == 0 && len(probe.hunkText) > 0 && strings.Contains(probe.hunkText, "b") {
		t.Fatalf("structural diff silent while hunks show the edit: %q", probe.hunkText)
	}
}

// hasDiffSelfClosingFlag must follow the tokenizer's attribute states: after a
// quoted value a '/' does set the flag, but inside an unquoted value it is an
// ordinary character (quotes there are ordinary too), and an unterminated quoted
// value swallows it.
func TestDiffSelfClosingFlagGrammar(t *testing.T) {
	for _, test := range []struct {
		attrRaw string
		want    bool
	}{
		{"/", true},
		{" /", true},
		{"\t/", true},
		{"\n/", true},
		{"\f/", true},
		{" //", true},
		{` a="b"/`, true},
		{` a="b" /`, true},
		{` a='b' /`, true},
		{` a="b>c" /`, true},
		{` a="b/c" /`, true},
		{" a=b /", true},
		{" a /", true},
		{" a=b/", false},
		{" a=b'/", false},
		{" a=b' c=d'/", false},
		{` a="b/`, false},
		{" a='b/", false},
		{" a=http://x/", false},
		{" a", false},
		{"", false},
	} {
		if got := hasDiffSelfClosingFlag(test.attrRaw); got != test.want {
			t.Errorf("hasDiffSelfClosingFlag(%q) = %v, want %v", test.attrRaw, got, test.want)
		}
	}
}

// "svg"/"math" in body pop themselves when the flag is set, so a self-closed
// inline icon must not swallow the rest of the document, and camelCase SVG names
// must not lose the flag (the tag name is lowercased, the raw text is not).
func TestDiffForeignSelfClosingIsNotSwallowing(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   string
		wantPath string
	}{
		{"svg_root", `<html><body><svg/><p>x</p></body></html>`, "/html[1]/body[1]/p[1]"},
		{"svg_icon", `<html><body><p>a</p><svg class="icon" aria-hidden="true"/><p>b</p></body></html>`, "/html[1]/body[1]/p[2]"},
		{"math_root", `<html><body><math/><p>x</p></body></html>`, "/html[1]/body[1]/p[1]"},
		{"camel_clipPath", `<html><body><svg><clipPath/><rect/></svg><p>x</p></body></html>`, "/html[1]/body[1]/svg[1]/rect[1]"},
		{"camel_linearGradient", `<html><body><svg><linearGradient/><rect/></svg><p>x</p></body></html>`, "/html[1]/body[1]/svg[1]/rect[1]"},
		{"upper_CIRCLE", `<html><body><svg><CIRCLE/><rect/></svg><p>x</p></body></html>`, "/html[1]/body[1]/svg[1]/rect[1]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := diffNodePaths(t, test.source); !hasDiffPath(got, test.wantPath) {
				t.Fatalf("paths = %v, want %s", got, test.wantPath)
			}
		})
	}
	// A self-closed foreign element must not consume a depth slot for the rest of
	// the document.
	deep := `<html><body><svg class="i"/>` + strings.Repeat("<div>", maxDiffDepth-2) + "x" + strings.Repeat("</div>", maxDiffDepth-2) + `</body></html>`
	if _, err := parseDiffHTML(deep); err != nil {
		t.Fatalf("self-closed <svg/> leaked a stack level: %v", err)
	}
	// Structural content changes under a self-closed foreign element stay visible.
	result, err := buildVersionDiff(1, 2,
		`<html><body><svg/><g>x</g></body></html>`,
		`<html><body><svg><g>x</g></svg></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) == 0 {
		t.Fatalf("reparenting <g> into <svg> reported no change: %+v", result)
	}
}

// Both layers must agree on whether a foreign self-closing element opens: the
// source layer keeps its own foreign stack, and a disagreement leaks the
// preformatted context to EOF (inflating lines and shifting the 413 threshold).
func TestDiffForeignSelfCloseLayersAgree(t *testing.T) {
	baseline := `<html><body><p>a` + "\n  " + `b</p></body></html>`
	withForeign := `<html><body><svg><text style="white-space:pre"/></svg><p>a` + "\n  " + `b</p></body></html>`
	baseLines, ok := normalizedHTMLLines(baseline)
	if !ok {
		t.Fatal("baseline source lines rejected")
	}
	foreignLines, ok := normalizedHTMLLines(withForeign)
	if !ok {
		t.Fatal("foreign source lines rejected")
	}
	countPre := func(lines []diffSourceLine) int {
		total := 0
		for _, line := range lines {
			if strings.HasPrefix(line.identity, "pre:") {
				total++
			}
		}
		return total
	}
	if got := countPre(foreignLines); got != countPre(baseLines) {
		t.Fatalf("self-closed foreign element leaked the preformatted context: pre lines = %d, want %d", got, countPre(baseLines))
	}
	// The structural layer must reach the same conclusion about the <p> text.
	textOf := func(source string) string {
		nodes, err := parseDiffHTML(source)
		if err != nil {
			t.Fatalf("parseDiffHTML(%q) err = %v", source, err)
		}
		for _, node := range nodes {
			if node.tag == "p" {
				return node.text
			}
		}
		return ""
	}
	if got, want := textOf(withForeign), textOf(baseline); got != want {
		t.Fatalf("structural text = %q, want %q", got, want)
	}
	// A closing tag must pop the foreign stack, so HTML rules resume afterwards.
	afterForeign := `<html><body><svg><circle r="1"/></svg><div/><p>x</p></div></body></html>`
	if got := diffNodePaths(t, afterForeign); !hasDiffPath(got, "/html[1]/body[1]/div[1]/p[1]") {
		t.Fatalf("paths = %v, want the <p> inside <div> once the svg subtree closed", got)
	}
}
