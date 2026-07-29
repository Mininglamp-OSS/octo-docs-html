package core

import "testing"

// ReconcileAnchors re-binds element anchors after a re-stamp: an anchor whose aid
// is still present is left alone; one whose aid vanished is rebound to a surviving
// candidate (or marked lost when it can't be resolved). These replace the former
// reconcile golden fixtures.

func elementComment(t *testing.T, id, aid string) []Comment {
	t.Helper()
	list, res := ApplyCommentOp(nil, CommentOp{
		Kind: "create", ID: id, At: "t0", Version: 1,
		Author: &Author{Login: "a"}, Text: "x", Anchor: &Anchor{Kind: "element", AID: aid},
	})
	if res.Status != 200 {
		t.Fatalf("create %s status %d", id, res.Status)
	}
	return list
}

func TestReconcileKeepsPresentAid(t *testing.T) {
	list := elementComment(t, "e1", "stays")
	ReconcileAnchors(list, []StampedArtifact{{AID: "stays", Tag: "img"}}, 1)
	a := SnapshotList(list, 1)[0].Anchor
	if a == nil || a.Kind != "element" || a.AID != "stays" {
		t.Errorf("present aid was disturbed: %+v", a)
	}
}

// When the anchored aid is gone but exactly one candidate remains, the anchor
// rebinds to it (single-candidate heuristic).
func TestReconcileRebindsToSoleCandidate(t *testing.T) {
	list := elementComment(t, "e1", "gone")
	ReconcileAnchors(list, []StampedArtifact{{AID: "present", Tag: "img"}}, 1)
	a := SnapshotList(list, 1)[0].Anchor
	if a == nil || a.Kind != "element" || a.AID != "present" {
		t.Errorf("expected rebind to 'present', got %+v", a)
	}
}

func TestReconcileRejectsPresentAidWithConflictingFingerprint(t *testing.T) {
	list, res := ApplyCommentOp(nil, CommentOp{
		Kind: "create", ID: "e1", At: "t0", Version: 1, Author: &Author{Login: "a"}, Text: "x",
		Anchor: &Anchor{Kind: "element", AID: "collision", Selector: `[data-odoc-aid="collision"]`, Label: "figure",
			Fingerprint: &AnchorFingerprint{Tag: "figure"}, Fallback: &AnchorFallback{NearestHeading: &struct {
				Text string `json:"text"`
			}{Text: "Target"}}},
	})
	if res.Status != 200 {
		t.Fatalf("create status = %d", res.Status)
	}
	headingTarget := "Target"
	headingOther := "Other"
	ReconcileAnchors(list, []StampedArtifact{
		{AID: "collision", Tag: "section", Head: "decoy", Heading: &headingTarget},
		{AID: "right", Tag: "figure", Head: "real", Heading: &headingTarget},
		{AID: "other", Tag: "figure", Head: "other", Heading: &headingOther},
	}, 2)
	a := SnapshotList(list, 2)[0].Anchor
	if a == nil || a.Kind != "lost" || a.Reason != "fingerprint_mismatch" {
		t.Fatalf("collision anchor = %+v, want fail-closed fingerprint mismatch", a)
	}
}

func TestReconcileMigratesUniqueLegacyAlias(t *testing.T) {
	list := elementComment(t, "e1", "legacy")
	list[0].Events[0].Anchor.Fingerprint = &AnchorFingerprint{Tag: "iframe"}
	ReconcileAnchors(list, []StampedArtifact{{
		AID: "canonical", Tag: "iframe", LegacyAIDs: []string{"legacy"},
	}}, 2)
	a := SnapshotList(list, 2)[0].Anchor
	if a == nil || a.Kind != "element" || a.AID != "canonical" || a.Selector != `[data-odoc-aid="canonical"]` {
		t.Fatalf("legacy alias anchor = %+v, want canonical migration", a)
	}
}

func TestReconcileLosesAmbiguousLegacyAlias(t *testing.T) {
	list := elementComment(t, "e1", "legacy")
	list[0].Events[0].Anchor.Fingerprint = &AnchorFingerprint{Tag: "iframe"}
	ReconcileAnchors(list, []StampedArtifact{
		{AID: "one", Tag: "iframe", LegacyAIDs: []string{"legacy"}},
		{AID: "two", Tag: "iframe", LegacyAIDs: []string{"legacy"}},
	}, 2)
	a := SnapshotList(list, 2)[0].Anchor
	if a == nil || a.Kind != "lost" || a.Reason != "ambiguous_aid" {
		t.Fatalf("ambiguous legacy alias anchor = %+v, want lost", a)
	}
}

func TestReconcileLosesLegacyAliasReusedAsCanonicalAID(t *testing.T) {
	list := elementComment(t, "e1", "legacy")
	list[0].Events[0].Anchor.Fingerprint = &AnchorFingerprint{Tag: "iframe"}
	ReconcileAnchors(list, []StampedArtifact{
		{AID: "legacy", Tag: "iframe"},
		{AID: "canonical", Tag: "iframe", LegacyAIDs: []string{"legacy"}},
	}, 2)
	a := SnapshotList(list, 2)[0].Anchor
	if a == nil || a.Kind != "lost" || a.Reason != "ambiguous_aid" {
		t.Fatalf("reused legacy alias anchor = %+v, want lost", a)
	}
}

func TestPinnedReconcileRefreshesFingerprintTag(t *testing.T) {
	list, res := ApplyCommentOp(nil, CommentOp{
		Kind: "create", ID: "e1", At: "t0", Version: 1, Author: &Author{Login: "a"}, Text: "x",
		Anchor: &Anchor{Kind: "element", AID: "pinned", Selector: `[data-odoc-aid="pinned"]`, Label: "section",
			Fingerprint: &AnchorFingerprint{Tag: "section"}},
	})
	if res.Status != 200 {
		t.Fatalf("create status = %d", res.Status)
	}
	reconcileAnchors(list, []StampedArtifact{{AID: "pinned", Tag: "figure"}}, 2, "pinned", "figure", nil)
	a := SnapshotList(list, 2)[0].Anchor
	if a == nil || a.AID != "pinned" || a.Label != "figure" || a.Fingerprint == nil || a.Fingerprint.Tag != "figure" {
		t.Fatalf("pinned anchor = %+v, want refreshed figure fingerprint", a)
	}
}
