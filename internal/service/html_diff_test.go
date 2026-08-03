package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-docs-html/internal/platform/apperr"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/platform/sluglock"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/storage"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/storage/memory"
)

func TestBuildVersionDiffDetectsTextChangePastDisplayLimit(t *testing.T) {
	prefix := strings.Repeat("a", maxDiffCompareText+100)
	before := "<main><p>" + prefix + " before</p></main>"
	after := "<main><p>" + prefix + " after</p></main>"

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified == 0 || len(result.Changes) == 0 {
		t.Fatalf("change past comparison display limit was lost: %+v", result)
	}
}

func TestParseDiffHTMLImpliedEndTagsHavePathsAndSnippets(t *testing.T) {
	source := `<p>one<main>two</main><p>three<hgroup>head</hgroup><p>four<search>find</search><ul><li>a<li>b</ul><dl><dt>term<dd>value</dl><select><option>a<option>b</select><table><thead><tr><th>h<tbody><tr><td>x<td>y</table>`
	nodes, err := parseDiffHTML(source)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"/p[1]":                          "<p>one",
		"/p[2]":                          "<p>three",
		"/p[3]":                          "<p>four",
		"/ul[1]/li[1]":                   "<li>a",
		"/dl[1]/dt[1]":                   "<dt>term",
		"/dl[1]/dd[1]":                   "<dd>value",
		"/select[1]/option[1]":           "<option>a",
		"/table[1]/thead[1]":             "<thead><tr><th>h",
		"/table[1]/tbody[1]/tr[1]/td[1]": "<td>x",
	}
	for _, node := range nodes {
		if expected, ok := want[node.path]; ok {
			if node.outer != expected {
				t.Errorf("%s outer = %q; want %q", node.path, node.outer, expected)
			}
			delete(want, node.path)
		}
		if node.outer == "" {
			t.Errorf("%s has no outer snippet", node.path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing paths: %v", want)
	}
}

func TestDiffTextDigestUsesCompleteCanonicalSemantics(t *testing.T) {
	before, err := parseDiffHTML(`<p>A &amp; B   &#x63;` + strings.Repeat(" ", 20) + `tail</p>`)
	if err != nil {
		t.Fatal(err)
	}
	after, err := parseDiffHTML(`<p>A &#38; B c tail</p>`)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].textDigest != after[0].textDigest || diffNodeSignature(before[0]) != diffNodeSignature(after[0]) {
		t.Fatalf("semantically equivalent text differs: %q / %q", before[0].text, after[0].text)
	}

	var split htmlDiffNode
	appendDiffNodeText(&split, "A &am")
	appendDiffNodeText(&split, "p; B")
	if got := strings.Join(strings.Fields(html.UnescapeString(strings.Join(split.textParts, ""))), " "); got != "A & B" {
		t.Fatalf("entity split across chunks = %q", got)
	}
}

func TestDiffRawTextDigestPreservesBytes(t *testing.T) {
	before, _ := parseDiffHTML(`<script>let x = 1</script>`)
	after, _ := parseDiffHTML(`<script>let  x = 1</script>`)
	if before[0].textDigest == after[0].textDigest {
		t.Fatal("script whitespace change was lost")
	}
}

func TestParseDiffAttrsDuplicateNamesUseFirstValue(t *testing.T) {
	attrs := parseDiffAttrs(` VALUE="first" value="second" VaLuE="third"`)
	if attrs["value"] != "first" {
		t.Fatalf("value = %q; want first", attrs["value"])
	}
}

func TestBuildVersionDiffPreservesQuotedAttributeWhitespace(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
	}{
		{"duplicate first wins", `<input value="A B" value="same">`, `<input value="A  B" value="same">`},
		{"ordinary attribute", `<p title="a b">x</p>`, `<p title="a  b">x</p>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := buildVersionDiff(1, 2, test.before, test.after)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.CodeHunks) == 0 {
				t.Fatalf("quoted whitespace change produced no source hunk: %+v", result)
			}
			if test.name == "duplicate first wins" && len(result.Changes) == 0 {
				t.Fatalf("browser-visible duplicate attribute change was lost: %+v", result)
			}
		})
	}
}

func TestNormalizedHTMLLinesIgnoreFormattingWhitespace(t *testing.T) {
	compact := `<html><body><p>alpha</p><p>beta</p></body></html>`
	pretty := "<html>\n  <body>\n    <p>alpha</p>\n    <p>beta</p>\n  </body>\n</html>\n"
	result, err := buildVersionDiff(1, 2, compact, pretty)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CodeHunks) != 0 || len(result.Changes) != 0 {
		t.Fatalf("format-only revision was noisy: %+v", result)
	}
	lines, ok := normalizedHTMLLines("<p>x</p>\u00a0")
	if !ok || len(lines) != 4 || lines[3] != "\u00a0" {
		t.Fatalf("NBSP text run lost: %#v, ok=%v", lines, ok)
	}
}

// TestStructuralDigestIgnoresBlockSiblingIndentation guards the reindent-only
// P1: pure formatting whitespace between block-like siblings must not pollute
// the parent's structural textDigest, while whitespace at a visible inline
// boundary must still register.
func TestStructuralDigestIgnoresBlockSiblingIndentation(t *testing.T) {
	for _, count := range []int{100, 500} {
		t.Run(fmt.Sprintf("main_%d_paragraphs", count), func(t *testing.T) {
			var compact, pretty strings.Builder
			compact.WriteString("<main>")
			pretty.WriteString("<main>\n")
			for index := 0; index < count; index++ {
				fmt.Fprintf(&compact, "<p>item %d</p>", index)
				fmt.Fprintf(&pretty, "  <p>item %d</p>\n", index)
			}
			compact.WriteString("</main>")
			pretty.WriteString("</main>\n")
			result, err := buildVersionDiff(1, 2, compact.String(), pretty.String())
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Changes) != 0 || len(result.CodeHunks) != 0 {
				t.Fatalf("reindent-only revision was noisy: %+v", result)
			}
		})
	}

	blockCases := []struct {
		name          string
		before, after string
	}{
		{"div_block_children", `<div><p>x</p><ul><li>y</li></ul></div>`, "<div>\n  <p>x</p>\n  <ul>\n    <li>y</li>\n  </ul>\n</div>"},
		{"table_rows", `<table><tr><td>a</td></tr><tr><td>b</td></tr></table>`, "<table>\n  <tr><td>a</td></tr>\n  <tr><td>b</td></tr>\n</table>"},
	}
	for _, test := range blockCases {
		t.Run(test.name, func(t *testing.T) {
			result, err := buildVersionDiff(1, 2, test.before, test.after)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Changes) != 0 || len(result.CodeHunks) != 0 {
				t.Fatalf("block-sibling reindent was noisy: %+v", result)
			}
		})
	}

	// Inline-sibling whitespace is a visible boundary and must still be detected.
	inlineCases := []struct {
		name          string
		before, after string
	}{
		{"inline_span_gap", `<p><a>x</a><a>y</a></p>`, `<p><a>x</a> <a>y</a></p>`},
		{"see_here_gap", `<p>see<a>here</a></p>`, `<p>see <a>here</a></p>`},
		{"custom_element_gap", `<p><x-tag>x</x-tag><x-tag>y</x-tag></p>`, `<p><x-tag>x</x-tag> <x-tag>y</x-tag></p>`},
	}
	for _, test := range inlineCases {
		t.Run(test.name, func(t *testing.T) {
			result, err := buildVersionDiff(1, 2, test.before, test.after)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Changes) == 0 {
				t.Fatalf("visible inline-boundary whitespace change was lost: %+v", result)
			}
		})
	}
}

func TestBuildVersionDiffPrettyPrintedDocumentsStayBounded(t *testing.T) {
	var source strings.Builder
	source.WriteString("<main>\n")
	for index := 0; index < 3500; index++ {
		fmt.Fprintf(&source, "  <i>%d</i>\n", index)
	}
	source.WriteString("</main>\n")
	result, err := buildVersionDiff(1, 2, source.String(), source.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 0 || len(result.CodeHunks) != 0 {
		t.Fatalf("identical pretty document differed: %+v", result)
	}
}

func TestMatchDiffNodesIsDeterministicNearBudget(t *testing.T) {
	var before, after strings.Builder
	before.WriteString("<main>")
	after.WriteString("<main>")
	for index := 0; index < 350; index++ {
		fmt.Fprintf(&before, "<p>before %d</p>", index)
		fmt.Fprintf(&after, "<p>after %d</p>", index)
	}
	before.WriteString("</main>")
	after.WriteString("</main>")
	var first string
	for run := 0; run < 100; run++ {
		result, err := buildVersionDiff(1, 2, before.String(), after.String())
		encoded, _ := json.Marshal(result)
		got := fmt.Sprintf("%v:%s", err, encoded)
		if run == 0 {
			first = got
		} else if got != first {
			t.Fatalf("run %d = %q; first = %q", run, got, first)
		}
	}
}

func TestMatchDiffNodesBoundsAsymmetricSiblingWork(t *testing.T) {
	var beforeSource, afterSource strings.Builder
	beforeSource.WriteString("<main>")
	for index := 0; index < 3900; index++ {
		fmt.Fprintf(&beforeSource, "<i>b%d</i>", index)
	}
	beforeSource.WriteString("</main>")
	afterSource.WriteString("<main>")
	for index := 0; index < 51; index++ {
		fmt.Fprintf(&afterSource, "<i>a%d</i>", index)
	}
	afterSource.WriteString("</main>")
	before, err := parseDiffHTML(beforeSource.String())
	if err != nil {
		t.Fatal(err)
	}
	after, err := parseDiffHTML(afterSource.String())
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ {
		if _, err := matchDiffNodes(before, after); err != errDiffLimit {
			t.Fatalf("run %d error = %v; want diff limit", run, err)
		}
	}
}

func TestMatchManyIdenticalSiblingsDoesNotExhaustBudget(t *testing.T) {
	for _, siblings := range []int{700, maxDiffNodes - 1} {
		t.Run(strconv.Itoa(siblings), func(t *testing.T) {
			source := `<main>` + strings.Repeat(`<span>same</span>`, siblings) + `</main>`
			before, err := parseDiffHTML(source)
			if err != nil {
				t.Fatal(err)
			}
			after, err := parseDiffHTML(source)
			if err != nil {
				t.Fatal(err)
			}
			matches, err := matchDiffNodes(before, after)
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != siblings+1 {
				t.Fatalf("matches = %d; want %d", len(matches), siblings+1)
			}
		})
	}
}

func TestDiffCodeHunksRejectsHunkLineOverflow(t *testing.T) {
	before := strings.Repeat("<br>", maxDiffHunkLines/2+1)
	after := strings.Repeat("<hr>", maxDiffHunkLines/2+1)
	if _, err := diffCodeHunks(before, after); err != errDiffLimit {
		t.Fatalf("error = %v; want diff limit", err)
	}
}

func TestDiffOutputSizeIsEncodedJSONSize(t *testing.T) {
	result := &VersionDiff{Changes: []ElementChange{{Kind: "modified", BeforeHTML: strings.Repeat(`"`, 100)}}}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if got := diffOutputSize(result); got != len(encoded) {
		t.Fatalf("size = %d; want %d", got, len(encoded))
	}
}

func TestDiffTruncationPreservesUTF8(t *testing.T) {
	line := strings.Repeat("界", 400)
	if got := limitDiffLine(line); !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
}

// TestDiffUnclosedTagTailIsPlainTextAndTerminates covers the malformed-input
// hardening for the diff parser: an unclosed '<' tail (no closing '>' exists)
// must be treated as plain text and terminate the scan — never an invalid
// slice, panic, or infinite loop — while the existing bounds still hold. The
// cases mirror the acceptance list ('<', 'abc<', '<div', '<!--', '<script')
// plus a few adjacent shapes. Exercises BOTH parse paths (parseDiffHTML for the
// structural tree and normalizedHTMLLines for the code-hunk view) via the full
// buildVersionDiff entry point.
func TestDiffUnclosedTagTailIsPlainTextAndTerminates(t *testing.T) {
	cases := []string{
		"<",
		"abc<",
		"<div",
		"<!--",
		"<script",
		"text<",
		"<!",
		"<?",
		"</",
		"</div",
		"<div class=\"x",
		"<a href='",
		"<!--unterminated comment",
		"<script>alert(1)",
		"<style>body{",
		"<textarea>hi",
		"<p>ok</p>trailing<",
	}
	for _, source := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parse panicked on %q: %v", source, r)
				}
			}()
			if _, err := parseDiffHTML(source); err != nil && err != errDiffLimit {
				t.Fatalf("parseDiffHTML(%q) unexpected err = %v", source, err)
			}
			if _, ok := normalizedHTMLLines(source); !ok {
				t.Fatalf("normalizedHTMLLines(%q) returned ok=false for a bounded input", source)
			}
			if _, err := buildVersionDiff(1, 2, source, source+"x"); err != nil && err != errDiffLimit {
				t.Fatalf("buildVersionDiff(%q) unexpected err = %v", source, err)
			}
		}()
	}
}

func TestBuildVersionDiffMatchesChangedElementWithoutStableAID(t *testing.T) {
	before := `<html><body><section data-odoc-aid="old"><p class="lead">alpha text</p></section></body></html>`
	after := `<html><body><section data-odoc-aid="new"><p class="lead">alpha text updated</p></section></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 1 || result.Summary.Added != 0 || result.Summary.Removed != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	change := result.Changes[0]
	if change.Kind != "modified" || change.DOMPath != "/html[1]/body[1]/section[1]/p[1]" {
		t.Fatalf("change = %+v", change)
	}
	if change.BeforeHTML != `<p class="lead">alpha text</p>` || change.AfterHTML != `<p class="lead">alpha text updated</p>` {
		t.Fatalf("unexpected local HTML: %+v", change)
	}
	if len(result.CodeHunks) != 1 || len(result.CodeHunks[0].Lines) == 0 {
		t.Fatalf("code hunks = %+v", result.CodeHunks)
	}
}

func TestBuildVersionDiffRejectsExcessiveNormalizedLines(t *testing.T) {
	var source strings.Builder
	for range maxDiffInputLines + 1 {
		source.WriteString("x<br>")
	}
	if _, err := buildVersionDiff(1, 2, source.String(), source.String()+"changed"); err != errDiffLimit {
		t.Fatalf("error = %v; want diff limit", err)
	}
}

func TestBuildVersionDiffRejectsExcessiveDepth(t *testing.T) {
	var source strings.Builder
	for range maxDiffDepth + 1 {
		source.WriteString("<div>")
	}
	for range maxDiffDepth + 1 {
		source.WriteString("</div>")
	}
	if _, err := buildVersionDiff(1, 2, source.String(), source.String()); err != errDiffLimit {
		t.Fatalf("error = %v; want diff limit", err)
	}
}

func TestParseDiffHTMLBoundsDeepLongTagPaths(t *testing.T) {
	tag := "x" + strings.Repeat("a", maxDiffTagBytes-1)
	var source strings.Builder
	source.Grow(5 << 20)
	for range maxDiffDepth {
		source.WriteByte('<')
		source.WriteString(tag)
		source.WriteByte('>')
	}
	for source.Len() < 5<<20 {
		source.WriteString("<!-- padding -->")
	}
	for range maxDiffDepth {
		source.WriteString("</")
		source.WriteString(tag)
		source.WriteByte('>')
	}
	htmlSource := source.String()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := parseDiffHTML(htmlSource); err != errDiffLimit {
		t.Fatalf("error = %v; want diff limit", err)
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 6<<20 {
		t.Fatalf("parse allocated %d bytes before rejecting oversized paths", allocated)
	}
}

func TestParseDiffHTMLRejectsOversizedTagName(t *testing.T) {
	source := "<x" + strings.Repeat("a", maxDiffTagBytes) + "></x>"
	if _, err := parseDiffHTML(source); err != errDiffLimit {
		t.Fatalf("error = %v; want diff limit", err)
	}
}

func TestDiffMapsPathLimitToPayloadTooLarge(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	docs := NewDocService(store, store, NewCommentService(store, sluglock.NewMemory()), sluglock.NewMemory(), "", 5<<20)
	tag := "x" + strings.Repeat("a", maxDiffTagBytes-1)
	var source strings.Builder
	for range maxDiffDepth {
		source.WriteByte('<')
		source.WriteString(tag)
		source.WriteByte('>')
	}
	for range maxDiffDepth {
		source.WriteString("</")
		source.WriteString(tag)
		source.WriteByte('>')
	}
	for version := 1; version <= 2; version++ {
		if _, err := store.PutDoc(ctx, "path-limit", version, source.String()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutMeta(ctx, "path-limit", storage.DocMeta{Slug: "path-limit", Versions: []storage.VersionRef{{N: 1}, {N: 2}}}); err != nil {
		t.Fatal(err)
	}

	_, err := docs.Diff(ctx, "path-limit", 1, 2)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Status != 413 || appErr.Code != "diff_too_complex" {
		t.Fatalf("error = %#v; want 413 diff_too_complex", err)
	}
}

func TestMatchDiffNodesRejectsCumulativeComparisonBytes(t *testing.T) {
	before := []htmlDiffNode{{tag: "main", parent: -1, children: []int{1}}}
	before = append(before, htmlDiffNode{tag: "p", parent: 0, path: "/before", compareText: strings.Repeat("a", maxDiffCompareText)})
	after := []htmlDiffNode{{tag: "main", parent: -1}}
	for index := 0; index < maxDiffNodes-1; index++ {
		after = append(after, htmlDiffNode{
			tag:         "p",
			parent:      0,
			path:        "/after/" + string(rune(index+1)),
			compareText: strings.Repeat("b", maxDiffCompareText-8) + string(rune(index+1)),
		})
		after[0].children = append(after[0].children, index+1)
	}
	if _, err := matchDiffNodes(before, after); err != errDiffLimit {
		t.Fatalf("error = %v; want diff limit", err)
	}
}

func TestBuildVersionDiffBoundsLargeModifiedContainerSnippet(t *testing.T) {
	body := strings.Repeat("content", 30_000)
	before := `<html><body><main class="before">` + body + `</main></body></html>`
	after := `<html><body><main class="after">` + body + `</main></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	change := result.Changes[0]
	if len(change.BeforeHTML) > maxDiffSnippetBytes || len(change.AfterHTML) > maxDiffSnippetBytes {
		t.Fatalf("snippet sizes = %d, %d", len(change.BeforeHTML), len(change.AfterHTML))
	}
	if strings.Contains(change.BeforeHTML, body[:10_000]) || strings.Contains(change.AfterHTML, body[:10_000]) {
		t.Fatal("large container body leaked into snippet")
	}
	if !strings.Contains(change.BeforeHTML, "omitted") || !strings.Contains(change.AfterHTML, "omitted") {
		t.Fatalf("snippets = %q / %q", change.BeforeHTML, change.AfterHTML)
	}
}

func TestBuildVersionDiffListHeadInsertionDoesNotCascade(t *testing.T) {
	before := `<html><body><ul><li>alpha</li><li>beta</li><li>gamma</li></ul></body></html>`
	after := `<html><body><ul><li>new</li><li>alpha</li><li>beta</li><li>gamma</li></ul></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Added != 1 || result.Summary.Modified != 0 || result.Summary.Removed != 0 {
		t.Fatalf("summary = %+v; changes = %+v", result.Summary, result.Changes)
	}
	if len(result.Changes) != 1 || result.Changes[0].AfterHTML != "<li>new</li>" {
		t.Fatalf("changes = %+v", result.Changes)
	}
}

func TestBuildVersionDiffDuplicateListHeadInsertionDoesNotCascade(t *testing.T) {
	before := `<html><body><ul><li>x</li><li>x</li></ul></body></html>`
	after := `<html><body><ul><li>new</li><li>x</li><li>x</li></ul></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Added != 1 || result.Summary.Modified != 0 || result.Summary.Removed != 0 {
		t.Fatalf("summary = %+v; changes = %+v", result.Summary, result.Changes)
	}
	if len(result.Changes) != 1 || result.Changes[0].AfterHTML != "<li>new</li>" {
		t.Fatalf("changes = %+v", result.Changes)
	}
}

func TestBuildVersionDiffPreservesScriptEntityLiterals(t *testing.T) {
	before := `<html><head><script>const marker = "&lt;";</script></head><body></body></html>`
	after := `<html><head><script>const marker = "<";</script></head><body></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 1 || result.Summary.Added != 0 || result.Summary.Removed != 0 {
		t.Fatalf("summary = %+v; changes = %+v", result.Summary, result.Changes)
	}
	if len(result.Changes) != 1 || result.Changes[0].DOMPath != "/html[1]/head[1]/script[1]" {
		t.Fatalf("changes = %+v", result.Changes)
	}
}

func TestBuildVersionDiffCodeHunksPreserveLiteralRawTextEntities(t *testing.T) {
	for _, tag := range []string{"script", "style"} {
		t.Run(tag, func(t *testing.T) {
			before := "<html><head><" + tag + `>value = "&amp;";</` + tag + "></head><body></body></html>"
			after := "<html><head><" + tag + `>value = "&";</` + tag + "></head><body></body></html>"

			result, err := buildVersionDiff(1, 2, before, after)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.CodeHunks) != 1 {
				t.Fatalf("code hunks = %+v", result.CodeHunks)
			}
			lines := strings.Join(result.CodeHunks[0].Lines, "\n")
			if !strings.Contains(lines, `-value = "&amp;";`) || !strings.Contains(lines, `+value = "&";`) {
				t.Fatalf("code hunk lost raw-text difference:\n%s", lines)
			}
		})
	}
}

func TestParseDiffHTMLBoundsCommentSeparatedTextStorage(t *testing.T) {
	var source strings.Builder
	source.Grow(5 << 20)
	source.WriteString("<main>")
	for source.Len() < 5<<20 {
		source.WriteString("x<!-- separator -->")
	}
	source.WriteString("</main>")

	nodes, err := parseDiffHTML(source.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	if len(nodes[0].text) > maxDiffCompareText || len(nodes[0].compareText) > maxDiffCompareText {
		t.Fatalf("stored text sizes = %d, %d", len(nodes[0].text), len(nodes[0].compareText))
	}
}

func TestParseDiffHTMLCommentSeparatedTextAllocationsStayLinear(t *testing.T) {
	var source strings.Builder
	source.Grow(1 << 20)
	source.WriteString("<main>")
	for source.Len() < 1<<20 {
		source.WriteString("x<!-- separator -->")
	}
	source.WriteString("</main>")

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := parseDiffHTML(source.String()); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
		t.Fatalf("parse allocated %d bytes for %d-byte input", allocated, source.Len())
	}
}

func TestBuildVersionDiffDetectsCommentChangePastDisplayLimit(t *testing.T) {
	prefix := strings.Repeat("z", 4000)
	before := "<html><body><p>hi</p><!-- " + prefix + "ALPHA --></body></html>"
	after := "<html><body><p>hi</p><!-- " + prefix + "OMEGA --></body></html>"

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CodeHunks) == 0 {
		t.Fatal("comment-only change produced no code hunk")
	}
}

func TestBuildVersionDiffCodeHunksDetectLongTextTailChange(t *testing.T) {
	prefix := strings.Repeat("a", 6000)
	before := "<html><body><p>" + prefix + "ALPHA</p></body></html>"
	after := "<html><body><p>" + prefix + "OMEGA</p></body></html>"

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 1 || len(result.CodeHunks) == 0 {
		t.Fatalf("summary = %+v; code hunks = %+v", result.Summary, result.CodeHunks)
	}
}

func TestBuildVersionDiffCommentDoesNotChangeVisibleText(t *testing.T) {
	before := `<html><body><p>ab</p></body></html>`
	after := `<html><body><p>a<!-- comment -->b</p></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("comment changed structural text: %+v", result.Changes)
	}
	if len(result.CodeHunks) == 0 {
		t.Fatal("comment source change produced no code hunk")
	}
}

func TestBuildVersionDiffDoesNotJoinEntityAcrossComment(t *testing.T) {
	before := `<html><body><p>&am<!--x-->p;</p></body></html>`
	after := `<html><body><p>&amp;</p></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) == 0 {
		t.Fatalf("split entity was treated as equal: %+v", result)
	}
}

func TestBuildVersionDiffPreservesTextAtChildBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
	}{
		{"visible whitespace", `<p>see <a>here</a></p>`, `<p>see<a>here</a></p>`},
		{"redistributed text", `<p>abc<b>X</b>def</p>`, `<p>ab<b>X</b>cdef</p>`},
		{"leading first child", `<p>a<b>x</b></p>`, `<p><b>x</b>a</p>`},
		{"trailing last child", `<p><b>x</b>a</p>`, `<p><b>x</b></p>a`},
		{"adjacent children boundary", `<p><b>x</b><i>y</i></p>`, `<p>z<b>x</b><i>y</i></p>`},
		{"void child boundary", `<p>a<br>b</p>`, `<p>ab<br></p>`},
		{"reviewer repro one", `<p>a<b>x</b><i>y</i>b</p>`, `<p><b>x</b>a<i>y</i>b</p>`},
		{"reviewer repro two", `<p>a<b>x</b>b<i>y</i></p>`, `<p>a<b>x</b><i>y</i>b</p>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := buildVersionDiff(1, 2, test.before, test.after)
			if err != nil {
				t.Fatal(err)
			}
			if result.Summary.Modified == 0 || len(result.Changes) == 0 {
				t.Fatalf("child-boundary edit was equal: %+v", result)
			}
		})
	}
}

func TestBuildVersionDiffTreatsLiteralNBSPAsDistinctFromASCIISpace(t *testing.T) {
	// HTML only folds ASCII whitespace, so a literal U+00A0 must not compare
	// equal to a plain space in the text summary; the edit is a real change.
	before := "<html><body><p>a\u00a0b</p></body></html>"
	after := "<html><body><p>a b</p></body></html>"

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified == 0 || len(result.Changes) == 0 {
		t.Fatalf("NBSP vs ASCII space was treated as equal: %+v", result)
	}

	// The same visible NBSP text must stay equal to itself (no spurious change).
	same := "<html><body><p>a\u00a0b</p></body></html>"
	result, err = buildVersionDiff(1, 2, same, same)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 0 || len(result.Changes) != 0 {
		t.Fatalf("identical NBSP text reported a change: %+v", result)
	}
}

func TestBuildVersionDiffAllowsManyOrdinaryEdits(t *testing.T) {
	var before, after strings.Builder
	before.WriteString("<html><body>")
	after.WriteString("<html><body>")
	for index := range 100 {
		fmt.Fprintf(&before, "<p>paragraph %03d has the original ordinary text.</p>", index)
		fmt.Fprintf(&after, "<p>paragraph %03d has the revised ordinary text.</p>", index)
	}
	before.WriteString("</body></html>")
	after.WriteString("</body></html>")

	result, err := buildVersionDiff(1, 2, before.String(), after.String())
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 100 || len(result.CodeHunks) == 0 {
		t.Fatalf("unexpected diff: summary=%+v hunks=%d", result.Summary, len(result.CodeHunks))
	}
}

func TestParseDiffHTMLBoundsMultipleRawTextElements(t *testing.T) {
	payload := strings.Repeat("A", 256<<10)
	var source strings.Builder
	source.WriteString("<html><head>")
	for range 8 {
		source.WriteString("<style>")
		source.WriteString(payload)
		source.WriteString("</STYLE><script>")
		source.WriteString(payload)
		source.WriteString("</SCRIPT>")
	}
	source.WriteString("</head><body><p>after raw text</p></body></html>")

	nodes, err := parseDiffHTML(source.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 20 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	for _, node := range nodes {
		if len(node.text) > maxDiffCompareText || len(node.compareText) > maxDiffCompareText {
			t.Fatalf("%s stored text sizes = %d, %d", node.tag, len(node.text), len(node.compareText))
		}
	}
	if nodes[len(nodes)-1].tag != "p" || nodes[len(nodes)-1].text != "after raw text" {
		t.Fatalf("last node = %+v", nodes[len(nodes)-1])
	}
}

func TestBuildVersionDiffParsesAfterRawTextCloseSlash(t *testing.T) {
	before := `<html><body><script>const value = 1;</script/><p>before</p></body></html>`
	after := `<html><body><script>const value = 1;</script/><p>after</p></body></html>`

	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Modified != 1 || result.Summary.Added != 0 || result.Summary.Removed != 0 {
		t.Fatalf("summary = %+v; changes = %+v", result.Summary, result.Changes)
	}
	if len(result.Changes) != 1 || result.Changes[0].DOMPath != "/html[1]/body[1]/p[1]" {
		t.Fatalf("changes = %+v", result.Changes)
	}
}

func TestBuildVersionDiffRejectsExcessiveNodeCount(t *testing.T) {
	var source strings.Builder
	source.WriteString("<html><body>")
	for range maxDiffNodes + 1 {
		source.WriteString("<i></i>")
	}
	source.WriteString("</body></html>")
	if _, err := buildVersionDiff(1, 2, source.String(), source.String()); err != errDiffLimit {
		t.Fatalf("error = %v; want diff limit", err)
	}
}

// reindentEqual builds a compact and a pretty-printed variant of the same block
// content and asserts the pair is a zero-noise no-op (Goal A): ordinary reindent
// of plain block/table/list elements without layout-affecting CSS must produce
// no structural changes and no source hunks.
func reindentEqual(t *testing.T, compact, pretty string) {
	t.Helper()
	result, err := buildVersionDiff(1, 2, compact, pretty)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 0 || len(result.CodeHunks) != 0 {
		t.Fatalf("reindent produced noise: changes=%d hunks=%d", len(result.Changes), len(result.CodeHunks))
	}
}

func TestBuildVersionDiffPlainReindentIsZeroNoise(t *testing.T) {
	for _, count := range []int{100, 500} {
		t.Run("p-"+strconv.Itoa(count), func(t *testing.T) {
			var compact, pretty strings.Builder
			for range count {
				compact.WriteString("<p>x</p>")
				pretty.WriteString("  <p>x</p>\n")
			}
			reindentEqual(t, "<main>"+compact.String()+"</main>", "<main>\n"+pretty.String()+"</main>")
		})
	}
	reindentEqual(t,
		`<table><tbody><tr><td>a</td><td>b</td></tr></tbody></table>`,
		"<table>\n  <tbody>\n    <tr>\n      <td>a</td>\n      <td>b</td>\n    </tr>\n  </tbody>\n</table>")
	reindentEqual(t,
		`<ul><li>a</li><li>b</li></ul>`,
		"<ul>\n  <li>a</li>\n  <li>b</li>\n</ul>")
	reindentEqual(t,
		`<div><section><p>a</p><p>b</p></section></div>`,
		"<div>\n  <section>\n    <p>a</p>\n    <p>b</p>\n  </section>\n</div>")
}

// The four final-review repros: a whitespace change that could be visible must
// surface in at least one layer, never an empty diff (Goal B).
func TestBuildVersionDiffWhitespaceSensitiveContextsAreNotEmptyDiffs(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
	}{
		{"pre-block-children", `<pre><div>x</div><div>y</div></pre>`, `<pre><div>x</div> <div>y</div></pre>`},
		{"ancestor-white-space-pre", `<div style="white-space:pre"><span>x</span><span>y</span></div>`, `<div style="white-space:pre"><span>x</span> <span>y</span></div>`},
		{"style-display-inline", `<style>div{display:inline}</style><div>x</div><div>y</div>`, `<style>div{display:inline}</style><div>x</div> <div>y</div>`},
		{"summary-display-inline", `<summary style="display:inline">x</summary><summary style="display:inline">y</summary>`, `<summary style="display:inline">x</summary> <summary style="display:inline">y</summary>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := buildVersionDiff(1, 2, tc.before, tc.after)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Changes) == 0 && len(result.CodeHunks) == 0 {
				t.Fatalf("whitespace change vanished: changes=0 hunks=0 for %q vs %q", tc.before, tc.after)
			}
		})
	}
}

func TestBuildVersionDiffPreformattedRunLengthChangeIsVisible(t *testing.T) {
	before := "<pre>a b</pre>"
	after := "<pre>a  b</pre>"
	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CodeHunks) == 0 {
		t.Fatal("differing whitespace-run length under <pre> produced no code hunk")
	}
}

// assertVisibleAndValid fails when a change that must surface vanishes from both
// layers, and verifies every emitted hunk line stays bounded and UTF-8 valid.
func assertVisibleAndValid(t *testing.T, before, after string) {
	t.Helper()
	result, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) == 0 && len(result.CodeHunks) == 0 {
		t.Fatalf("change vanished: changes=0 hunks=0 for %q vs %q", before, after)
	}
	const maxLineBytes = 1 << 12
	for _, hunk := range result.CodeHunks {
		for _, line := range hunk.Lines {
			if !utf8.ValidString(line) {
				t.Fatalf("hunk line is not valid UTF-8: %q", line)
			}
			if len(line) > maxLineBytes {
				t.Fatalf("hunk line unbounded: %d bytes", len(line))
			}
		}
	}
}

// Raw-text (RCDATA/literal) content changes must surface, including when the
// content holds a stray '<' that is text, not markup: the parser must scan to
// the close tag rather than splitting the run into a spurious tag.
func TestBuildVersionDiffRawTextWhitespaceAndStrayLtAreVisible(t *testing.T) {
	assertVisibleAndValid(t, "<textarea>a b</textarea>", "<textarea>a  b</textarea>")
	assertVisibleAndValid(t, "<textarea>a < b</textarea>", "<textarea>a <  b</textarea>")
	assertVisibleAndValid(t, "<title>a &amp; b</title>", "<title>a &amp;&amp; b</title>")
	// Identical raw-text content still yields no diff.
	ident, err := buildVersionDiff(1, 2, "<textarea>a < b</textarea>", "<textarea>a < b</textarea>")
	if err != nil {
		t.Fatal(err)
	}
	if len(ident.Changes) != 0 || len(ident.CodeHunks) != 0 {
		t.Fatalf("identical raw text produced a diff: changes=%d hunks=%d", len(ident.Changes), len(ident.CodeHunks))
	}
}

// Long content that shares its first >1024 bytes but diverges in the tail must
// not collide once display truncation applies; the full-content digest keeps
// distinct text distinct across pre, white-space:pre, and pure-whitespace runs.
func TestBuildVersionDiffLongTailChangesDoNotCollide(t *testing.T) {
	prefix := strings.Repeat("x", 1500)
	assertVisibleAndValid(t, "<pre>"+prefix+"ALPHA</pre>", "<pre>"+prefix+"OMEGA</pre>")

	long := strings.Repeat("word ", 400) // > 1024 bytes after collapse
	assertVisibleAndValid(t,
		`<div style="white-space:pre">`+long+"ALPHA</div>",
		`<div style="white-space:pre">`+long+"OMEGA</div>")

	// Pure-whitespace runs differing only in length or tail beyond 1024 must not
	// collide via the whitespace-run token.
	ws := strings.Repeat(" ", 1400)
	assertVisibleAndValid(t,
		`<div style="white-space:pre"><span>x</span>`+ws+"<span>y</span></div>",
		`<div style="white-space:pre"><span>x</span>`+ws+" <span>y</span></div>")
	assertVisibleAndValid(t,
		`<pre><span>x</span>`+strings.Repeat(" ", 1400)+"a<span>y</span></pre>",
		`<pre><span>x</span>`+strings.Repeat(" ", 1400)+"b<span>y</span></pre>")
}

func TestBuildVersionDiffDeterministicWhitespaceHunks(t *testing.T) {
	before := `<style>x{}</style><div>a</div><div>b</div>`
	after := `<style>x{}</style><div>a</div> <div>b</div>`
	first, err := buildVersionDiff(1, 2, before, after)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	for range 5 {
		next, err := buildVersionDiff(1, 2, before, after)
		if err != nil {
			t.Fatal(err)
		}
		nextJSON, _ := json.Marshal(next)
		if string(firstJSON) != string(nextJSON) {
			t.Fatalf("nondeterministic diff:\n%s\n%s", firstJSON, nextJSON)
		}
	}
}

func TestHasLayoutAffectingCSSConservativeSignals(t *testing.T) {
	positives := []string{
		`<style>p{}</style><p>x</p>`,
		`<link rel="stylesheet" href="a.css"><p>x</p>`,
		`<div style="display:flex"><p>x</p></div>`,
		`<div style="white-space:normal"><p>x</p></div>`,
	}
	for _, src := range positives {
		if !hasLayoutAffectingCSS(src) {
			t.Errorf("expected layout-affecting CSS for %q", src)
		}
	}
	negatives := []string{
		`<main><p>x</p><p>y</p></main>`,
		`<div class="lead"><p>x</p></div>`,
		`<div style="color:red"><p>x</p></div>`,
	}
	for _, src := range negatives {
		if hasLayoutAffectingCSS(src) {
			t.Errorf("did not expect layout-affecting CSS for %q", src)
		}
	}
}
