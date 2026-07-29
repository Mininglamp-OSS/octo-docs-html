package core

import (
	"regexp"
	"strings"
	"time"
)

// Publish-time anchor reconciliation, ported from reconcile.ts. When a new
// version is published, an element anchor may no longer resolve. For each comment
// alive at the version we try to re-bind it by fingerprint + nearest-heading,
// appending an anchor_changed event; otherwise we mark it lost.

var aidSelectorRe = regexp.MustCompile(`\[data-odoc-aid="(\w+)"\]`)

// knownAid extracts the aid an anchor currently targets, if any.
func knownAid(a *Anchor) string {
	if a.Kind == "element" && a.AID != "" {
		return a.AID
	}
	if a.Kind == "element" && a.Selector != "" {
		if m := aidSelectorRe.FindStringSubmatch(a.Selector); m != nil {
			return m[1]
		}
	}
	return ""
}

// findRebindAid finds the single confident re-bind target for a drifted anchor.
func findRebindAid(a *Anchor, aids []StampedArtifact) string {
	wantTag := ""
	if a.Fingerprint != nil && a.Fingerprint.Tag != "" {
		wantTag = a.Fingerprint.Tag
	} else if a.Label != "" {
		wantTag = strings.ToLower(a.Label)
	}
	wantHead := ""
	if a.Fallback != nil && a.Fallback.NearestHeading != nil {
		wantHead = a.Fallback.NearestHeading.Text
	}

	var matches []StampedArtifact
	for _, x := range aids {
		tagOK := wantTag == "" || x.Tag == wantTag
		headOK := wantHead == "" || (x.Heading != nil && strings.EqualFold(*x.Heading, wantHead))
		if tagOK && headOK {
			matches = append(matches, x)
		}
	}
	if len(matches) == 1 {
		return matches[0].AID
	}
	if len(matches) == 0 {
		var tagOnly []StampedArtifact
		for _, x := range aids {
			if wantTag == "" || x.Tag == wantTag {
				tagOnly = append(tagOnly, x)
			}
		}
		if len(tagOnly) == 1 {
			return tagOnly[0].AID
		}
	}
	return ""
}

// nextAnchor computes the new anchor to record for a drifted comment: a rebind, a
// lost marker, or nil (no change).
func nextAnchor(a *Anchor, aids []StampedArtifact) *Anchor {
	newAID := findRebindAid(a, aids)
	if newAID != "" {
		label := a.Label
		if label == "" && a.Fingerprint != nil {
			label = a.Fingerprint.Tag
		}
		if label == "" {
			label = "element"
		}
		return &Anchor{
			Kind:        "element",
			AID:         newAID,
			Selector:    `[data-odoc-aid="` + newAID + `"]`,
			Label:       label,
			Fingerprint: a.Fingerprint,
			Fallback:    a.Fallback,
		}
	}
	if a.Kind == "lost" {
		return nil // already lost, no candidate — don't churn the log
	}
	return &Anchor{
		Kind:        "lost",
		Reason:      "no_candidate",
		Label:       a.Label,
		Fingerprint: a.Fingerprint,
		Fallback:    a.Fallback,
	}
}

func reconcileEvent(anchor *Anchor, version int, at string) CommentEvent {
	rs := false
	return CommentEvent{
		Kind: "anchor_changed", AtVersion: version, At: at, By: "reconcile",
		ResetStatus: &rs, Anchor: anchor,
	}
}

func lostAnchor(a *Anchor, reason string) *Anchor {
	if a.Kind == "lost" && a.Reason == reason {
		return nil
	}
	return &Anchor{
		Kind:        "lost",
		Reason:      reason,
		Label:       a.Label,
		Fingerprint: a.Fingerprint,
		Fallback:    a.Fallback,
	}
}

func anchorMatchesArtifact(a *Anchor, artifact StampedArtifact) bool {
	if a.Fingerprint != nil && a.Fingerprint.Tag != "" {
		return artifact.Tag == strings.ToLower(a.Fingerprint.Tag)
	}
	return true
}

func migratedAnchor(a *Anchor, byAID map[string][]StampedArtifact, pinnedAID, pinnedTag string, migrations map[string]string) *Anchor {
	canonicalAID := migrations[pinnedAID]
	if a.Kind != "element" || canonicalAID == "" || canonicalAID == pinnedAID || knownAid(a) != pinnedAID {
		return nil
	}
	artifacts := byAID[canonicalAID]
	if len(artifacts) != 1 || artifacts[0].Tag != pinnedTag {
		return nil
	}
	copy := *a
	copy.Kind = "element"
	copy.AID = canonicalAID
	copy.Selector = `[data-odoc-aid="` + canonicalAID + `"]`
	copy.Label = pinnedTag
	copy.Fingerprint = &AnchorFingerprint{Tag: pinnedTag}
	return &copy
}

func pinnedAnchor(a *Anchor, byAID map[string][]StampedArtifact, pinnedAID, pinnedTag string) *Anchor {
	if pinnedAID == "" || pinnedTag == "" || knownAid(a) != pinnedAID {
		return nil
	}
	artifacts := byAID[pinnedAID]
	if len(artifacts) != 1 || artifacts[0].Tag != pinnedTag {
		return nil
	}
	copy := *a
	fingerprint := AnchorFingerprint{Tag: pinnedTag}
	copy.Fingerprint = &fingerprint
	copy.AID = pinnedAID
	copy.Selector = `[data-odoc-aid="` + pinnedAID + `"]`
	copy.Label = pinnedTag
	return &copy
}

func canonicalAliasAnchor(a *Anchor, artifact StampedArtifact) *Anchor {
	copy := *a
	copy.Kind = "element"
	copy.AID = artifact.AID
	copy.Selector = `[data-odoc-aid="` + artifact.AID + `"]`
	if copy.Label == "" {
		copy.Label = artifact.Tag
	}
	return &copy
}

func reconcileComment(c *Comment, aids []StampedArtifact, byAID, byLegacyAID map[string][]StampedArtifact, version int, at, pinnedAID, pinnedTag string, migrations map[string]string) {
	snap := SnapshotAt(c, version)
	if snap == nil || snap.Deleted {
		return
	}
	a := snap.Anchor
	if a == nil || (a.Kind != "element" && a.Kind != "lost") {
		return
	}
	if a.Kind == "lost" && len(migrations) > 0 {
		return
	}
	if migrated := migratedAnchor(a, byAID, pinnedAID, pinnedTag, migrations); migrated != nil {
		AppendEvent(c, reconcileEvent(migrated, version, at))
		return
	}
	if refreshed := pinnedAnchor(a, byAID, pinnedAID, pinnedTag); refreshed != nil {
		AppendEvent(c, reconcileEvent(refreshed, version, at))
		return
	}
	aid := knownAid(a)
	if aid != "" {
		artifacts := byAID[aid]
		legacyMatches := byLegacyAID[aid]
		if len(artifacts) > 0 && len(legacyMatches) > 0 {
			if lost := lostAnchor(a, "ambiguous_aid"); lost != nil {
				AppendEvent(c, reconcileEvent(lost, version, at))
			}
			return
		}
		if len(artifacts) == 1 && anchorMatchesArtifact(a, artifacts[0]) {
			return
		}
		if len(artifacts) > 0 {
			reason := "fingerprint_mismatch"
			if len(artifacts) > 1 {
				reason = "ambiguous_aid"
			}
			if lost := lostAnchor(a, reason); lost != nil {
				AppendEvent(c, reconcileEvent(lost, version, at))
			}
			return
		}
		if len(legacyMatches) == 1 && anchorMatchesArtifact(a, legacyMatches[0]) {
			AppendEvent(c, reconcileEvent(canonicalAliasAnchor(a, legacyMatches[0]), version, at))
			return
		}
		if len(legacyMatches) > 0 {
			reason := "fingerprint_mismatch"
			if len(legacyMatches) > 1 {
				reason = "ambiguous_aid"
			}
			if lost := lostAnchor(a, reason); lost != nil {
				AppendEvent(c, reconcileEvent(lost, version, at))
			}
			return
		}
	}
	if anchor := nextAnchor(a, aids); anchor != nil {
		AppendEvent(c, reconcileEvent(anchor, version, at))
	}
}

// ReconcileAnchors reconciles open comment anchors against the freshly-stamped
// artifact set for a version, mutating comments in place.
func ReconcileAnchors(comments []Comment, aidsInVersion []StampedArtifact, v int) {
	reconcileAnchors(comments, aidsInVersion, v, "", "", nil)
}

func reconcileAnchors(comments []Comment, aidsInVersion []StampedArtifact, v int, pinnedAID, pinnedTag string, migrations map[string]string) {
	EnsureMigrated(comments)
	byAID := make(map[string][]StampedArtifact, len(aidsInVersion))
	byLegacyAID := make(map[string][]StampedArtifact)
	for _, a := range aidsInVersion {
		byAID[a.AID] = append(byAID[a.AID], a)
		for _, legacyAID := range a.LegacyAIDs {
			byLegacyAID[legacyAID] = append(byLegacyAID[legacyAID], a)
		}
	}
	version := v
	if version == 0 {
		version = 1
	}
	at := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for i := range comments {
		reconcileComment(&comments[i], aidsInVersion, byAID, byLegacyAID, version, at, pinnedAID, pinnedTag, migrations)
	}
}
