package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-docs-html/internal/platform/sluglock"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/storage"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/storage/memory"
)

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

func TestReplaceElementPersistsCompatibleVersionChangeMetadata(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := NewCommentService(store, locker)
	docs := NewDocService(store, store, comments, locker, "", 5<<20)

	published, err := docs.Publish(ctx, PublishInput{Slug: "tracked", HTML: `<html><body><section><p>before</p></section></body></html>`})
	if err != nil {
		t.Fatal(err)
	}
	view, err := docs.Render(ctx, "tracked", published.Version)
	if err != nil {
		t.Fatal(err)
	}
	aid := firstAID(t, view.HTML)

	result, err := docs.ReplaceElement(ctx, "tracked", 1, aid, `<section><p>after</p></section>`)
	if err != nil {
		t.Fatal(err)
	}
	if result.ChangeSource != "element_replace" || result.BaseVersion != 1 || result.NewVersion != 2 || result.TargetAID != aid {
		t.Fatalf("replace result metadata = %+v", result)
	}
	meta, err := store.GetMeta(ctx, "tracked")
	if err != nil {
		t.Fatal(err)
	}
	changes, ok := meta.Extra[storage.VersionChangesExtraKey].(map[string]any)
	if !ok {
		t.Fatalf("version changes = %#v", meta.Extra[storage.VersionChangesExtraKey])
	}
	entry, ok := changes["2"].(map[string]any)
	if !ok || entry["change_source"] != "element_replace" || entry["base_version"] != float64(1) || entry["new_version"] != float64(2) || entry["target_aid"] != aid {
		t.Fatalf("version 2 metadata = %#v", changes["2"])
	}
}

func TestReplaceElementPreservesConflictingOpenMetadata(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := NewCommentService(store, locker)
	docs := NewDocService(store, store, comments, locker, "", 5<<20)

	published, err := docs.Publish(ctx, PublishInput{Slug: "metadata-conflict", HTML: `<html><body><section><p>before</p></section></body></html>`})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetMeta(ctx, "metadata-conflict")
	if err != nil {
		t.Fatal(err)
	}
	meta.Extra = map[string]any{storage.LegacyVersionChangesExtraKey: "caller-owned"}
	if err := store.PutMeta(ctx, meta.Slug, *meta); err != nil {
		t.Fatal(err)
	}
	view, err := docs.Render(ctx, meta.Slug, published.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.ReplaceElement(ctx, meta.Slug, 1, firstAID(t, view.HTML), `<section><p>after</p></section>`); err != nil {
		t.Fatal(err)
	}
	meta, err = store.GetMeta(ctx, meta.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Extra[storage.LegacyVersionChangesExtraKey] != "caller-owned" {
		t.Fatalf("legacy metadata overwritten: %#v", meta.Extra)
	}
	changes, ok := meta.Extra[storage.VersionChangesExtraKey].(map[string]any)
	if !ok || changes["2"] == nil {
		t.Fatalf("namespaced changes = %#v", meta.Extra[storage.VersionChangesExtraKey])
	}
}

func TestReplaceElementMigratesCompatibleLegacyVersionChanges(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := NewCommentService(store, locker)
	docs := NewDocService(store, store, comments, locker, "", 5<<20)

	published, err := docs.Publish(ctx, PublishInput{Slug: "metadata-legacy", HTML: `<html><body><section><p>before</p></section></body></html>`})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetMeta(ctx, "metadata-legacy")
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{"1": map[string]any{"change_source": "legacy"}}
	meta.Extra = map[string]any{storage.LegacyVersionChangesExtraKey: legacy}
	if err := store.PutMeta(ctx, meta.Slug, *meta); err != nil {
		t.Fatal(err)
	}
	view, err := docs.Render(ctx, meta.Slug, published.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docs.ReplaceElement(ctx, meta.Slug, 1, firstAID(t, view.HTML), `<section><p>after</p></section>`); err != nil {
		t.Fatal(err)
	}
	meta, err = store.GetMeta(ctx, meta.Slug)
	if err != nil {
		t.Fatal(err)
	}
	changes, ok := meta.Extra[storage.VersionChangesExtraKey].(map[string]any)
	if !ok || changes["1"] == nil || changes["2"] == nil {
		t.Fatalf("migrated changes = %#v", meta.Extra[storage.VersionChangesExtraKey])
	}
	if _, ok := meta.Extra[storage.LegacyVersionChangesExtraKey].(map[string]any); !ok {
		t.Fatalf("legacy map not preserved: %#v", meta.Extra)
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

func firstAID(t *testing.T, source string) string {
	t.Helper()
	const marker = `data-odoc-aid="`
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatal("no aid in stamped source")
	}
	start += len(marker)
	end := strings.IndexByte(source[start:], '"')
	if end < 0 {
		t.Fatal("unterminated aid")
	}
	return source[start : start+end]
}
