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
