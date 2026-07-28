package service_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-docs-html/internal/core"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/platform/sluglock"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/service"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/service/docsbackend"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/storage"
	"github.com/Mininglamp-OSS/octo-docs-html/internal/storage/memory"
)

// noopRegistrar is a DocRegistrar that just records that Register ran. It
// serves the registration gate in afterPublished so the reconcile hook fires.
type noopRegistrar struct {
	registered atomic.Int32
	renamed    atomic.Int32
}

func (r *noopRegistrar) Register(_ context.Context, reg docsbackend.Registration, _ string) (*docsbackend.RegistrationResult, error) {
	r.registered.Add(1)
	return &docsbackend.RegistrationResult{
		DocID:       "doc-" + reg.OctoDocSlug,
		OctoDocSlug: reg.OctoDocSlug,
		ShareURL:    "https://docs.example.test/d/doc-" + reg.OctoDocSlug,
		Created:     true,
	}, nil
}
func (r *noopRegistrar) Rename(context.Context, string, string, string) {
	r.renamed.Add(1)
}
func (*noopRegistrar) Delete(context.Context, string, string) {}

// afterPublished invokes the injected reconciler after confirmed registration
// so grants written during the post-commit registration gap survive A4.
func TestAfterPublishedTriggersGrantReconciler(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20)
	registrar := &noopRegistrar{}
	docs = docs.WithDocsBackendRegistration(registrar, nil)

	var called atomic.Int32
	var seenSlug atomic.Value
	docs = docs.WithGrantReconciler(func(_ context.Context, slug string) error {
		called.Add(1)
		seenSlug.Store(slug)
		return nil
	})

	// MountType=group makes registrationForMount run Register, then reconcile.
	ctx := context.Background()
	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug:      "docGap",
		HTML:      "<html><body><p>x</p></body></html>",
		MountType: "group",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if got := called.Load(); got != 1 {
		t.Fatalf("reconciler called %d times; want 1", got)
	}
	if got := registrar.registered.Load(); got != 1 {
		t.Fatalf("registrar called %d times; want 1 (reconcile must run only after Register)", got)
	}
	if got, _ := seenSlug.Load().(string); got != "docGap" {
		t.Fatalf("reconciler saw slug %q; want docGap", got)
	}
}

// afterPublished with a nil reconciler registers without a panic.
func TestAfterPublishedNilReconcilerSafe(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20).
		WithDocsBackendRegistration(&noopRegistrar{}, nil)

	if _, err := docs.Publish(context.Background(), service.PublishInput{
		Slug:      "docNoRec",
		HTML:      "<html><body><p>x</p></body></html>",
		MountType: "group",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func TestAfterPublishedThreadMountTriggersReconciler(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	registrar := &noopRegistrar{}
	var called atomic.Int32
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20).
		WithDocsBackendRegistration(registrar, nil).
		WithGrantReconciler(func(context.Context, string) error {
			called.Add(1)
			return nil
		})

	if _, err := docs.Publish(context.Background(), service.PublishInput{
		Slug:      "docThr",
		HTML:      "<html><body><p>x</p></body></html>",
		MountType: "thread",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if called.Load() != 1 {
		t.Fatalf("reconciler calls = %d; want 1", called.Load())
	}
	if registrar.registered.Load() != 1 {
		t.Fatalf("register calls = %d; want 1", registrar.registered.Load())
	}
}

func TestReplaceElementRestoresPersistedMountContext(t *testing.T) {
	for _, mountType := range []string{"group", "space", "thread"} {
		t.Run(mountType, func(t *testing.T) {
			store := memory.New()
			locker := sluglock.NewMemory()
			comments := service.NewCommentService(store, locker)
			registrar := &noopRegistrar{}
			var reconciled atomic.Int32
			docs := service.NewDocService(store, store, comments, locker, "", 5<<20).
				WithDocsBackendRegistration(registrar, nil).
				WithGrantReconciler(func(context.Context, string) error {
					reconciled.Add(1)
					return nil
				})

			ctx := context.Background()
			slug := "replace-mounted-" + mountType
			if _, err := docs.Publish(ctx, service.PublishInput{
				Slug: slug, HTML: "<html><body><section><p>old</p></section></body></html>", MountType: mountType,
			}); err != nil {
				t.Fatalf("publish: %v", err)
			}
			rendered, err := docs.Render(ctx, slug, 1)
			if err != nil || rendered == nil {
				t.Fatalf("render: data=%v err=%v", rendered, err)
			}
			start := strings.Index(rendered.HTML, `data-odoc-aid="`)
			if start < 0 {
				t.Fatal("published document has no aid")
			}
			start += len(`data-odoc-aid="`)
			end := strings.Index(rendered.HTML[start:], `"`)
			if end < 0 {
				t.Fatal("published document has malformed aid")
			}
			aid := rendered.HTML[start : start+end]
			result, err := docs.ReplaceElement(ctx, slug, 1, aid, "<section><p>new</p></section>")
			if err != nil {
				t.Fatalf("replace: %v", err)
			}
			if !result.Registered || result.Status != "published" {
				t.Fatalf("replace registration = registered:%v status:%q", result.Registered, result.Status)
			}
			if got := registrar.registered.Load(); got != 2 {
				t.Fatalf("register calls = %d; want 2", got)
			}
			if got := reconciled.Load(); got != 2 {
				t.Fatalf("reconcile calls = %d; want 2", got)
			}
			meta, err := store.GetMeta(ctx, slug)
			if err != nil {
				t.Fatal(err)
			}
			if persistedMount, ok := meta.MountType(); !ok || persistedMount != mountType {
				t.Fatalf("persisted mount = %q, %v; want %s, true", persistedMount, ok, mountType)
			}
		})
	}
}

func TestPublishOmittedMountRestoresPersistedMountContext(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	registrar := &noopRegistrar{}
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20).
		WithDocsBackendRegistration(registrar, nil)

	ctx := context.Background()
	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug: "republish-mounted", HTML: "<html><body><p>v1</p></body></html>", MountType: "group",
	}); err != nil {
		t.Fatalf("initial publish: %v", err)
	}
	result, err := docs.Publish(ctx, service.PublishInput{
		Slug: "republish-mounted", HTML: "<html><body><p>v2</p></body></html>",
	})
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if !result.Registered || result.Status != "published" {
		t.Fatalf("republish registration = registered:%v status:%q", result.Registered, result.Status)
	}
	if got := registrar.registered.Load(); got != 2 {
		t.Fatalf("register calls = %d; want 2", got)
	}
	meta, err := store.GetMeta(ctx, "republish-mounted")
	if err != nil {
		t.Fatal(err)
	}
	if mountType, ok := meta.MountType(); !ok || mountType != "group" {
		t.Fatalf("persisted mount = %q, %v; want group, true", mountType, ok)
	}
}

func TestPublishExplicitEmptyMountPreservesExistingMount(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	registrar := &noopRegistrar{}
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20).
		WithDocsBackendRegistration(registrar, nil)

	ctx := context.Background()
	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug: "empty-mounted", HTML: "<html><body><p>v1</p></body></html>", MountType: "space",
	}); err != nil {
		t.Fatalf("initial publish: %v", err)
	}
	result, err := docs.Publish(ctx, service.PublishInput{
		Slug: "empty-mounted", HTML: "<html><body><p>v2</p></body></html>", MountTypePresent: true,
	})
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if !result.Registered || result.Status != "published" {
		t.Fatalf("republish registration = registered:%v status:%q", result.Registered, result.Status)
	}
	meta, err := store.GetMeta(ctx, "empty-mounted")
	if err != nil {
		t.Fatal(err)
	}
	if mountType, ok := meta.MountType(); !ok || mountType != "space" {
		t.Fatalf("persisted mount = %q, %v; want space, true", mountType, ok)
	}
}

func TestPromoteRestoresMountAndRenames(t *testing.T) {
	for _, mountType := range []string{"group", "space", "thread"} {
		t.Run(mountType, func(t *testing.T) {
			store := memory.New()
			locker := sluglock.NewMemory()
			comments := service.NewCommentService(store, locker)
			registrar := &noopRegistrar{}
			var reconciled atomic.Int32
			docs := service.NewDocService(store, store, comments, locker, "", 5<<20).
				WithDocsBackendRegistration(registrar, nil).
				WithGrantReconciler(func(context.Context, string) error {
					reconciled.Add(1)
					return nil
				})

			ctx := context.Background()
			slug := "promote-mounted-" + mountType
			if _, err := docs.Publish(ctx, service.PublishInput{
				Slug: slug, HTML: "<html><body><p>v1</p></body></html>", Title: "Old", MountType: mountType,
			}); err != nil {
				t.Fatalf("publish: %v", err)
			}
			if _, err := docs.SaveDraft(ctx, slug, "<html><body><p>draft</p></body></html>", "", ""); err != nil {
				t.Fatalf("save draft: %v", err)
			}
			result, err := docs.Promote(ctx, slug, "New")
			if err != nil {
				t.Fatalf("promote: %v", err)
			}
			if !result.Registered || result.Status != "published" {
				t.Fatalf("promote registration = registered:%v status:%q", result.Registered, result.Status)
			}
			if got := registrar.registered.Load(); got != 2 {
				t.Fatalf("register calls = %d; want 2", got)
			}
			if got := registrar.renamed.Load(); got != 1 {
				t.Fatalf("rename calls = %d; want 1", got)
			}
			if got := reconciled.Load(); got != 2 {
				t.Fatalf("reconcile calls = %d; want 2", got)
			}
			meta, err := store.GetMeta(ctx, slug)
			if err != nil {
				t.Fatal(err)
			}
			if persistedMount, ok := meta.MountType(); !ok || persistedMount != mountType {
				t.Fatalf("persisted mount = %q, %v; want %s, true", persistedMount, ok, mountType)
			}
		})
	}
}

func TestLegacyPromoteDoesNotClaimUnregistered(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	registrar := &noopRegistrar{}
	var reconciled atomic.Int32
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20).
		WithDocsBackendRegistration(registrar, nil).
		WithGrantReconciler(func(context.Context, string) error {
			reconciled.Add(1)
			return nil
		})
	ctx := context.Background()
	created := time.Now().UTC().Format(time.RFC3339)
	if err := store.PutMeta(ctx, "legacy-promote", storage.DocMeta{
		Slug: "legacy-promote", Title: "Old", Versions: []storage.VersionRef{{N: 1, Created: &created}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDoc(ctx, "legacy-promote", 1, "<html><body><p>v1</p></body></html>"); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.SaveDraft(ctx, "legacy-promote", "<html><body><p>draft</p></body></html>", "", ""); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	result, err := docs.Promote(ctx, "legacy-promote", "New")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if result.Registered || result.Status != "published" {
		t.Fatalf("legacy promote = registered:%v status:%q; want false, published", result.Registered, result.Status)
	}
	if got := registrar.registered.Load(); got != 0 {
		t.Fatalf("legacy register calls = %d; want 0", got)
	}
	if got := registrar.renamed.Load(); got != 1 {
		t.Fatalf("legacy rename calls = %d; want 1", got)
	}
	if got := reconciled.Load(); got != 1 {
		t.Fatalf("legacy reconcile calls = %d; want 1", got)
	}
}

// issue-21: element/replace must preserve the target's aid so a comment anchored
// to it stays anchored (not marked lost) after the re-stamp — even when the
// replacement changes the element's tag and content — and the rendered v2 must
// still carry that old aid so the DOM selector resolves.
func TestReplaceElementPreservesAnchoredCommentAcrossTagChange(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20)

	ctx := context.Background()
	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug: "anchored", HTML: "<html><body><section><p>old</p></section></body></html>",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Grab the section's stamped aid from v1.
	v1, err := docs.Render(ctx, "anchored", 1)
	if err != nil || v1 == nil {
		t.Fatalf("render v1: data=%v err=%v", v1, err)
	}
	start := strings.Index(v1.HTML, `data-odoc-aid="`)
	if start < 0 {
		t.Fatal("v1 has no aid")
	}
	start += len(`data-odoc-aid="`)
	end := strings.Index(v1.HTML[start:], `"`)
	if end < 0 {
		t.Fatal("v1 has malformed aid")
	}
	oldAID := v1.HTML[start : start+end]

	// Anchor a comment to that aid.
	if res, err := comments.Create(ctx, "anchored", &core.Author{Login: "u"}, "note",
		&core.Anchor{Kind: "element", AID: oldAID}, 1); err != nil || res.Status != 200 {
		t.Fatalf("create comment: status=%d err=%v", res.Status, err)
	}

	// Replace the <section> with a DIFFERENT tag + content. The backend injects
	// oldAID onto the replacement root so the anchor survives.
	if _, err := docs.ReplaceElement(ctx, "anchored", 1, oldAID,
		"<figure><p>brand new</p></figure>"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	// v2 must still contain the old aid (so the anchor's selector resolves).
	v2, err := docs.Render(ctx, "anchored", 2)
	if err != nil || v2 == nil {
		t.Fatalf("render v2: data=%v err=%v", v2, err)
	}
	if !strings.Contains(v2.HTML, `data-odoc-aid="`+oldAID+`"`) {
		t.Fatalf("v2 dropped the preserved aid %q: %q", oldAID, v2.HTML)
	}
	if !strings.Contains(v2.HTML, "<figure") || strings.Contains(v2.HTML, "<section") {
		t.Fatalf("v2 did not apply the tag-changing replacement: %q", v2.HTML)
	}

	// The comment must still be element-anchored to oldAID, NOT lost.
	snaps, err := comments.List(ctx, "anchored", 2)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("comment count = %d; want 1", len(snaps))
	}
	a := snaps[0].Anchor
	if a == nil || a.Kind != "element" {
		t.Fatalf("comment anchor = %+v; want element-anchored (not lost)", a)
	}
	got := a.AID
	if got == "" && a.Selector != "" {
		got = a.Selector // selector carries [data-odoc-aid="..."] after a reconcile rebind
	}
	if !strings.Contains(got, oldAID) {
		t.Fatalf("comment anchor lost the aid: kind=%q aid=%q selector=%q", a.Kind, a.AID, a.Selector)
	}
}

// Honest contract: a BARE non-addressable replacement root (a <div>/<p> with no
// opt-in) is rejected with a validation error. The stamper does not harvest such
// a root, so pinning an aid onto it would silently vanish on the next publish and
// lose the anchored comment. The caller must use a stampable tag or the existing
// class "odoc-artifact" opt-in instead.
func TestReplaceElementRejectsBareNonAddressableRoot(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20)

	ctx := context.Background()
	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug: "barediv", HTML: "<html><body><section><p>old</p></section></body></html>",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	v1, err := docs.Render(ctx, "barediv", 1)
	if err != nil || v1 == nil {
		t.Fatalf("render v1: data=%v err=%v", v1, err)
	}
	start := strings.Index(v1.HTML, `data-odoc-aid="`) + len(`data-odoc-aid="`)
	end := strings.Index(v1.HTML[start:], `"`)
	oldAID := v1.HTML[start : start+end]

	// Bare <div> and bare <p> are both rejected (not harvestable).
	for _, bad := range []string{`<div>brand new</div>`, `<p>brand new</p>`} {
		_, rerr := docs.ReplaceElement(ctx, "barediv", 1, oldAID, bad)
		if rerr == nil {
			t.Fatalf("replace with bare root %q: want validation error, got nil", bad)
		}
	}
	// A <div> that carries the existing class opt-in IS accepted (harvestable).
	if _, err := docs.ReplaceElement(ctx, "barediv", 1, oldAID,
		`<div class="odoc-artifact">brand new</div>`); err != nil {
		t.Fatalf("replace with opt-in div: unexpected err %v", err)
	}
	v2, err := docs.Render(ctx, "barediv", 2)
	if err != nil || v2 == nil {
		t.Fatalf("render v2: data=%v err=%v", v2, err)
	}
	if !strings.Contains(v2.HTML, `data-odoc-aid="`+oldAID+`"`) {
		t.Fatalf("opt-in div root did not carry the preserved aid: %q", v2.HTML)
	}
}

// issue-21 P1(2): a self-closing void (<img/>) is a valid replacement root. The
// backend injects the old aid before the trailing slash and re-stamps preserving
// it, so v2 carries a well-formed single-slash <img .../> with the aid and the
// anchored comment stays element-anchored to it.
func TestReplaceElementPreservesAnchorForSelfClosingRoot(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20)

	ctx := context.Background()
	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug: "imgroot", HTML: "<html><body><section><p>old</p></section></body></html>",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	v1, err := docs.Render(ctx, "imgroot", 1)
	if err != nil || v1 == nil {
		t.Fatalf("render v1: data=%v err=%v", v1, err)
	}
	start := strings.Index(v1.HTML, `data-odoc-aid="`) + len(`data-odoc-aid="`)
	end := strings.Index(v1.HTML[start:], `"`)
	oldAID := v1.HTML[start : start+end]

	if res, err := comments.Create(ctx, "imgroot", &core.Author{Login: "u"}, "note",
		&core.Anchor{Kind: "element", AID: oldAID}, 1); err != nil || res.Status != 200 {
		t.Fatalf("create comment: status=%d err=%v", res.Status, err)
	}

	// Replace the <section> with a self-closing void <img/> root.
	if _, err := docs.ReplaceElement(ctx, "imgroot", 1, oldAID, `<img src="a.png" alt="x"/>`); err != nil {
		t.Fatalf("replace: %v", err)
	}

	v2, err := docs.Render(ctx, "imgroot", 2)
	if err != nil || v2 == nil {
		t.Fatalf("render v2: data=%v err=%v", v2, err)
	}
	if !strings.Contains(v2.HTML, `<img src="a.png" alt="x" data-odoc-aid="`+oldAID+`"/>`) {
		t.Fatalf("self-closing root not reconstructed with single slash + preserved aid: %q", v2.HTML)
	}
	if strings.Contains(v2.HTML, `/ data-odoc-aid`) {
		t.Fatalf("self-closing root retained a stray slash: %q", v2.HTML)
	}

	snaps, err := comments.List(ctx, "imgroot", 2)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("comment count = %d; want 1", len(snaps))
	}
	a := snaps[0].Anchor
	if a == nil || a.Kind != "element" {
		t.Fatalf("comment anchor = %+v; want element-anchored (not lost)", a)
	}
	got := a.AID
	if got == "" && a.Selector != "" {
		got = a.Selector
	}
	if !strings.Contains(got, oldAID) {
		t.Fatalf("comment anchor lost the aid: kind=%q aid=%q selector=%q", a.Kind, a.AID, a.Selector)
	}
}

func TestReplaceElementAcceptsSelfClosingForeignRoot(t *testing.T) {
	for _, replacement := range []string{`<svg/>`, `<svg />`} {
		t.Run(replacement, func(t *testing.T) {
			store := memory.New()
			locker := sluglock.NewMemory()
			comments := service.NewCommentService(store, locker)
			docs := service.NewDocService(store, store, comments, locker, "", 5<<20)
			ctx := context.Background()

			if _, err := docs.Publish(ctx, service.PublishInput{
				Slug: "svgroot", HTML: "<html><body><section>old</section><aside>after</aside></body></html>",
			}); err != nil {
				t.Fatalf("publish: %v", err)
			}
			oldAID := firstAID(t, docs, "svgroot", 1)
			if _, err := docs.ReplaceElement(ctx, "svgroot", 1, oldAID, replacement); err != nil {
				t.Fatalf("replace %q: %v", replacement, err)
			}

			view, err := docs.GetElement(ctx, "svgroot", 2, oldAID)
			if err != nil || view == nil || view.Tag != "svg" || view.HTML != `<svg data-odoc-aid="`+oldAID+`"/>` {
				t.Fatalf("GetElement v2 = view=%v err=%v", view, err)
			}
			v2, err := docs.Render(ctx, "svgroot", 2)
			if err != nil || v2 == nil || !strings.Contains(v2.HTML, view.HTML+`<aside`) {
				t.Fatalf("render v2 = data=%v err=%v", v2, err)
			}

			if _, err := docs.ReplaceElement(ctx, "svgroot", 2, oldAID, `<svg width="9"/>`); err != nil {
				t.Fatalf("second replace: %v", err)
			}
			view, err = docs.GetElement(ctx, "svgroot", 3, oldAID)
			if err != nil || view == nil || view.HTML != `<svg width="9" data-odoc-aid="`+oldAID+`"/>` {
				t.Fatalf("GetElement v3 = view=%v err=%v", view, err)
			}

			v3, err := docs.Render(ctx, "svgroot", 3)
			if err != nil || v3 == nil {
				t.Fatalf("render v3: data=%v err=%v", v3, err)
			}
			if _, err := docs.Publish(ctx, service.PublishInput{Slug: "svgroot", HTML: v3.HTML}); err != nil {
				t.Fatalf("plain restamp publish: %v", err)
			}
			v4, err := docs.Render(ctx, "svgroot", 4)
			if err != nil || v4 == nil || !strings.Contains(v4.HTML, `<svg width="9" data-odoc-aid="`) || !strings.Contains(v4.HTML, `"/><aside`) {
				t.Fatalf("render v4 = data=%v err=%v", v4, err)
			}
		})
	}
}

// firstAID renders slug@version and returns the first stamped data-odoc-aid.
func firstAID(t *testing.T, docs *service.DocService, slug string, version int) string {
	t.Helper()
	rd, err := docs.Render(context.Background(), slug, version)
	if err != nil || rd == nil {
		t.Fatalf("render %s v%d: data=%v err=%v", slug, version, rd, err)
	}
	const marker = `data-odoc-aid="`
	start := strings.Index(rd.HTML, marker)
	if start < 0 {
		t.Fatalf("%s v%d has no aid: %q", slug, version, rd.HTML)
	}
	start += len(marker)
	end := strings.Index(rd.HTML[start:], `"`)
	if end < 0 {
		t.Fatalf("%s v%d malformed aid", slug, version)
	}
	return rd.HTML[start : start+end]
}

// Honest contract continuity: on a SUPPORTED (naturally harvestable) root the
// pinned aid survives a GetElement, a SECOND ReplaceElement, AND a later plain
// Publish. We use a stampable <figure> root; because the stamper re-harvests it
// on every plain re-stamp, no persistent marker is needed and the anchored
// comment stays element-anchored through v4.
func TestSupportedRootStaysAddressableAcrossPlainRestamp(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20)
	ctx := context.Background()

	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug: "keepfig", HTML: "<html><body><section><p>old</p></section></body></html>",
	}); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	oldAID := firstAID(t, docs, "keepfig", 1)

	if res, err := comments.Create(ctx, "keepfig", &core.Author{Login: "u"}, "note",
		&core.Anchor{Kind: "element", AID: oldAID}, 1); err != nil || res.Status != 200 {
		t.Fatalf("create comment: status=%d err=%v", res.Status, err)
	}

	// v2: replace the <section> with a stampable <figure> root; the backend pins
	// oldAID onto it (no marker needed — figure is re-harvested on every re-stamp).
	if _, err := docs.ReplaceElement(ctx, "keepfig", 1, oldAID, "<figure>brand new</figure>"); err != nil {
		t.Fatalf("replace to figure (v2): %v", err)
	}
	view, err := docs.GetElement(ctx, "keepfig", 2, oldAID)
	if err != nil || view == nil || view.Tag != "figure" || !strings.Contains(view.HTML, "brand new") {
		t.Fatalf("GetElement v2 = view=%v err=%v; want the pinned figure", view, err)
	}

	// Second ReplaceElement by the SAME old aid must edit the figure and carry the
	// aid forward (v3).
	if _, err := docs.ReplaceElement(ctx, "keepfig", 2, oldAID, "<figure>edited twice</figure>"); err != nil {
		t.Fatalf("second replace (v3): %v", err)
	}
	v3, err := docs.Render(ctx, "keepfig", 3)
	if err != nil || v3 == nil {
		t.Fatalf("render v3: data=%v err=%v", v3, err)
	}
	if !strings.Contains(v3.HTML, "edited twice") || strings.Contains(v3.HTML, "brand new") {
		t.Fatalf("second replace edited the wrong element: %q", v3.HTML)
	}
	if !strings.Contains(v3.HTML, `data-odoc-aid="`+oldAID+`"`) {
		t.Fatalf("v3 dropped the preserved aid %q: %q", oldAID, v3.HTML)
	}

	// A subsequent PLAIN publish (v4) re-stamps the whole doc via StampAids. The
	// figure is stampable, so it is re-harvested and content-addressed: its aid may
	// change (content differs), but the anchored comment REBINDS to it by fingerprint
	// (tag + nearest heading) rather than going lost — the honest continuity
	// guarantee for a supported root.
	if _, err := docs.Publish(ctx, service.PublishInput{Slug: "keepfig", HTML: v3.HTML}); err != nil {
		t.Fatalf("plain publish v4: %v", err)
	}
	figAIDv4 := firstAID(t, docs, "keepfig", 4)
	if view, err := docs.GetElement(ctx, "keepfig", 4, figAIDv4); err != nil || view == nil || view.Tag != "figure" {
		t.Fatalf("GetElement v4 = view=%v err=%v; want the figure still addressable", view, err)
	}

	snaps, err := comments.List(ctx, "keepfig", 4)
	if err != nil {
		t.Fatalf("list comments v4: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("comment count = %d; want 1", len(snaps))
	}
	a := snaps[0].Anchor
	if a == nil || a.Kind != "element" {
		t.Fatalf("comment anchor = %+v; want element-anchored (rebound, not lost) at v4", a)
	}
	// Rebound to the figure's current aid at v4 (the only figure in the doc).
	got := a.AID
	if got == "" && a.Selector != "" {
		got = a.Selector
	}
	if !strings.Contains(got, figAIDv4) {
		t.Fatalf("comment did not rebind to the figure at v4: kind=%q aid=%q selector=%q want %q",
			a.Kind, a.AID, a.Selector, figAIDv4)
	}
}

// Fix 3 (service): ReplaceElement must NOT reject a fragment merely because the
// literal string "data-odoc-*" appears in a text node or an attribute VALUE; only
// a real data-odoc-* attribute NAME is stamper-owned. A nested opt-in on a bare
// root is still rejected (Fix 2, root-scope).
func TestReplaceElementDataOdocLiteralInValueOrTextAccepted(t *testing.T) {
	store := memory.New()
	locker := sluglock.NewMemory()
	comments := service.NewCommentService(store, locker)
	docs := service.NewDocService(store, store, comments, locker, "", 5<<20)
	ctx := context.Background()

	if _, err := docs.Publish(ctx, service.PublishInput{
		Slug: "odoclit", HTML: "<html><body><section><p>old</p></section></body></html>",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	oldAID := firstAID(t, docs, "odoclit", 1)

	// Supported root; "data-odoc-*" appears ONLY in a title value and in text.
	ok := `<section title="data-odoc-aid=fake"><p>see data-odoc-artifact text</p></section>`
	if _, err := docs.ReplaceElement(ctx, "odoclit", 1, oldAID, ok); err != nil {
		t.Fatalf("replace with data-odoc literal in value/text: unexpected err %v", err)
	}

	// A nested class opt-in does NOT make a bare <div> root harvestable (root-scope).
	aid2 := firstAID(t, docs, "odoclit", 2)
	nestedOptIn := `<div><span class="odoc-artifact">nested</span></div>`
	if _, err := docs.ReplaceElement(ctx, "odoclit", 2, aid2, nestedOptIn); err == nil {
		t.Fatal("nested opt-in on bare root: want validation error, got nil")
	}
}
