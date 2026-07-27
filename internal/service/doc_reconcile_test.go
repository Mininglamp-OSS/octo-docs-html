package service_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
