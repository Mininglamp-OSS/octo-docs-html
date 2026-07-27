package core

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
)

// Artifact identity (data-odoc-aid) stamping, ported from stamp.ts.
//
// The SAME input HTML must produce the SAME stamped output byte-for-byte. All
// structural delimiters (<, >, tag names) are ASCII, so byte offsets land on the
// same logical boundaries JavaScript's UTF-16 offsets would; sliced content is
// identical bytes and therefore hashes identically via Cyrb53 (which re-encodes
// to UTF-16 internally). The one place UTF-16 semantics matter outside Cyrb53 is
// the 80-unit `head` excerpt, handled by utf16Slice.

var stampableTags = []string{
	"img", "svg", "canvas", "video", "pre", "figure", "iframe",
	"section", "aside", "blockquote", "table", "details",
}

var rawTextTags = []string{"script", "style", "textarea", "title"}

var intrinsicAttrs = []string{"viewBox", "src", "alt", "aria-label", "title"}

type stampElement struct {
	openStart    int
	openEnd      int
	closeEnd     int
	tag          string
	attrs        string
	innerHTML    string
	isVoid       bool
	cleanedAttrs string
	aid          string
}

type heading struct {
	end  int
	text string
}

// StampResult is the stamped HTML plus the artifact index.
type StampResult struct {
	HTML string
	AIDs []StampedArtifact
}

// jsSpace is the character-class body matching JavaScript's \s (ECMAScript
// WhiteSpace + LineTerminator). Go's RE2 \s is ASCII-only ([\t\n\f\r ]) — it
// omits vertical tab and every Unicode space (U+00A0 nbsp, U+3000 ideographic,
// U+2028/U+2029 line/paragraph separators, …). Using bare \s would collapse
// whitespace differently from the upstream TS, changing the normalized string
// fed to Cyrb53 and thus the data-odoc-aid — breaking byte-equivalence on any
// document containing non-ASCII whitespace. See docs/PORTING.md (trap 4).
const jsSpace = `\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// wsClass is jsSpace as a bracketed regex character class; use wsClass+"*" /
// wsClass+"+" wherever the TS source used \s* / \s+.
const wsClass = `[` + jsSpace + `]`

var (
	dataOdocAttrRe  = regexp.MustCompile(wsClass + `data-odoc-[\w-]+` + wsClass + `*=` + wsClass + `*"[^"]*"`)
	dataOdocAidRe   = regexp.MustCompile(wsClass + `+data-odoc-aid` + wsClass + `*=` + wsClass + `*"[^"]*"`)
	dataOdocAidRe2  = regexp.MustCompile(wsClass + `data-odoc-aid` + wsClass + `*=` + wsClass + `*"[^"]*"`)
	htmlCommentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	whitespaceRunRe = regexp.MustCompile(wsClass + `+`)
	tagStripRe      = regexp.MustCompile(`<[^>]+>`)
	selfCloseEndRe  = regexp.MustCompile(`/` + wsClass + `*$`)
	voidTagRe       = regexp.MustCompile(`(?i)^(img|iframe)$`)
	// rawAnyRe matches a raw-text open tag at ANY nesting depth (not just top
	// level); used to reject injected <script>/<style>/... inside a fragment.
	rawAnyRe = regexp.MustCompile(`(?i)<(` + strings.Join(rawTextTags, "|") + `)\b`)
	// eventAttrRe matches an inline event handler attribute (on...=), e.g.
	// onerror=, onclick=, onload=. wsClass allows JS whitespace around the '='.
	eventAttrRe = regexp.MustCompile(`(?i)` + wsClass + `on[a-z]+` + wsClass + `*=`)
	// jsURLRe matches a javascript: URL scheme anywhere in the fragment.
	jsURLRe = regexp.MustCompile(`(?i)javascript:`)
	// dataOdocAnyRe matches any data-odoc-* attribute; hand-written replacements
	// must not carry stamper-owned attributes (would make DOM selectors ambiguous).
	dataOdocAnyRe   = regexp.MustCompile(`(?i)data-odoc-[^` + jsSpace + `"'=<>/]*` + wsClass + `*=`)
	optInArtifactRe = regexp.MustCompile(`(?i)\bdata-odoc-artifact\b`)
	optInClassRe    = regexp.MustCompile(`(?i)class` + wsClass + `*=` + wsClass + `*"[^"]*\bodoc-artifact\b[^"]*"`)
	probeTagRe      = regexp.MustCompile(`(?i)<([a-z][\w-]*)\b`)
)

// isJSSpace reports whether r is whitespace per JavaScript's String.prototype
// .trim() (same set as jsSpace). It intentionally differs from unicode.IsSpace,
// which includes U+0085 (NEL, not JS whitespace) and excludes U+FEFF.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// trimJSSpace trims leading/trailing whitespace using JS .trim() semantics,
// replacing strings.TrimSpace so aid hashing stays byte-equivalent with upstream.
func trimJSSpace(s string) string {
	return strings.TrimFunc(s, isJSSpace)
}

// aidFor computes the content-hash aid for one artifact element.
func aidFor(tag, innerHTML, openAttrs string) string {
	var parts []string
	for _, a := range intrinsicAttrs {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(a) + `\s*=\s*"([^"]*)"`)
		if m := re.FindStringSubmatch(openAttrs); m != nil {
			parts = append(parts, a+"="+m[1])
		}
	}
	intrinsics := strings.Join(parts, "|")
	norm := htmlCommentRe.ReplaceAllString(innerHTML, "")
	norm = dataOdocAttrRe.ReplaceAllString(norm, "")
	norm = whitespaceRunRe.ReplaceAllString(norm, " ")
	norm = trimJSSpace(norm)
	return Cyrb53(tag+"|"+intrinsics+"|"+norm, 0)
}

// attrAwareOpenTagEnd returns the index just past the > that closes the open tag
// starting at lt, treating > inside quoted attribute values as ordinary text.
// Returns -1 if unterminated.
func attrAwareOpenTagEnd(html string, lt int) int {
	var quote byte
	for i := lt + 1; i < len(html); i++ {
		ch := html[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '"', '\'':
			quote = ch
		case '>':
			return i + 1
		}
	}
	return -1
}

// skipRawTextBodyAt returns the index just past a raw-text element's closing tag.
func skipRawTextBodyAt(html, openTag, attrs string, openEnd int) int {
	if selfCloseEndRe.MatchString(attrs) {
		return openEnd
	}
	re := regexp.MustCompile(`(?i)</` + regexp.QuoteMeta(openTag) + `\s*>`)
	loc := re.FindStringIndex(html[openEnd:])
	if loc == nil {
		return len(html)
	}
	return openEnd + loc[1]
}

// collectHeadings finds <hN> headings with their end offsets. The TS original
// uses a backreference (</h\1>) which RE2 forbids, so we loop the three heading
// levels and pair manually.
func collectHeadings(html string) []heading {
	var out []heading
	for _, level := range []string{"1", "2", "3"} {
		openRe := regexp.MustCompile(`(?i)<h` + level + `\b[^>]*>`)
		idx := 0
		for {
			loc := openRe.FindStringIndex(html[idx:])
			if loc == nil {
				break
			}
			contentStart := idx + loc[1]
			rel := indexFoldClose(html[contentStart:], "</h"+level)
			if rel < 0 {
				idx = contentStart
				continue
			}
			contentEnd := contentStart + rel
			// advance past the full close tag (</hN ...>)
			closeEndRel := strings.IndexByte(html[contentEnd:], '>')
			if closeEndRel < 0 {
				idx = contentStart
				continue
			}
			end := contentEnd + closeEndRel + 1
			inner := html[contentStart:contentEnd]
			text := tagStripRe.ReplaceAllString(inner, "")
			text = whitespaceRunRe.ReplaceAllString(text, " ")
			text = trimJSSpace(text)
			out = append(out, heading{end: end, text: text})
			idx = end
		}
	}
	// Sort by end offset so nearestHeading lookup (scan ascending) works as in TS,
	// where headings were collected in document order by a single regex.
	sort.SliceStable(out, func(i, j int) bool { return out[i].end < out[j].end })
	return out
}

// indexFoldClose finds the first case-insensitive occurrence of a closing tag
// prefix like "</h1" and returns the byte index of its '<', or -1.
func indexFoldClose(s, prefix string) int {
	lower := strings.ToLower(s)
	return strings.Index(lower, strings.ToLower(prefix))
}

// findCloseEnd finds the closing-tag end offset for a non-void element.
func findCloseEnd(html, tag string, openEnd int) int {
	closeRe := regexp.MustCompile(`(?i)</` + regexp.QuoteMeta(tag) + `\s*>`)
	openRe := regexp.MustCompile(`(?i)<` + regexp.QuoteMeta(tag) + `\b`)
	rawRe := regexp.MustCompile(`(?i)<(` + strings.Join(rawTextTags, "|") + `)\b`)
	depth := 1
	scan := openEnd
	for scan < len(html) {
		close := relMatch(closeRe, html, scan)
		open := relMatch(openRe, html, scan)
		raw := relMatch(rawRe, html, scan)
		next, kind := earliest(close, open, raw)
		if next == nil {
			break
		}
		switch kind {
		case "raw":
			rEnd := attrAwareOpenTagEnd(html, next[0])
			if rEnd < 0 {
				return openEnd
			}
			rawTag := strings.ToLower(html[next[2]:next[3]])
			scan = skipRawTextBodyAt(html, rawTag, html[next[0]:rEnd], rEnd)
		case "close":
			depth--
			if depth == 0 {
				return next[1]
			}
			scan = next[1]
		case "open":
			depth++
			oEnd := attrAwareOpenTagEnd(html, next[0])
			if oEnd < 0 {
				scan = next[1]
			} else {
				scan = oEnd
			}
		}
	}
	return openEnd
}

// relMatch runs re against html[from:] and returns absolute submatch indices
// ([start,end, group1start,group1end...]) or nil.
func relMatch(re *regexp.Regexp, html string, from int) []int {
	loc := re.FindStringSubmatchIndex(html[from:])
	if loc == nil {
		return nil
	}
	out := make([]int, len(loc))
	for i, v := range loc {
		if v < 0 {
			out[i] = v
		} else {
			out[i] = v + from
		}
	}
	return out
}

// earliest returns the match with the smallest start index and its kind.
func earliest(close, open, raw []int) ([]int, string) {
	var best []int
	var kind string
	consider := func(m []int, k string) {
		if m == nil {
			return
		}
		if best == nil || m[0] < best[0] {
			best, kind = m, k
		}
	}
	// Order matters only for ties; TS sorts by index with stable order
	// close,open,raw — but ties at the same index can't happen for distinct
	// patterns starting with '<' + different next char, so any order is fine.
	consider(close, "close")
	consider(open, "open")
	consider(raw, "raw")
	return best, kind
}

func harvest(html string, openStart, openEnd int, tag, attrs string, seen map[int]bool, elements *[]stampElement) {
	if seen[openStart] {
		return
	}
	isVoid := voidTagRe.MatchString(tag) || selfCloseEndRe.MatchString(attrs)
	closeEnd := openEnd
	innerHTML := ""
	if !isVoid {
		closeEnd = findCloseEnd(html, tag, openEnd)
		end := closeEnd - len("</"+tag+">")
		if end >= openEnd && end <= len(html) {
			innerHTML = html[openEnd:end]
		}
	}
	seen[openStart] = true
	*elements = append(*elements, stampElement{
		openStart: openStart, openEnd: openEnd, closeEnd: closeEnd,
		tag: tag, attrs: attrs, innerHTML: innerHTML, isVoid: isVoid,
	})
}

// harvestAt force-harvests the element whose open tag begins at start, whatever
// its tag. StampAidsPinned uses it so a safe but non-stampable replacement root
// (div/p/...) is still indexed and can carry the pinned aid. No-op if start is
// out of range, not a '<', or the open tag is unterminated.
func harvestAt(html string, start int, seen map[int]bool, elements *[]stampElement) {
	if start < 0 || start >= len(html) || html[start] != '<' {
		return
	}
	loc := probeTagRe.FindStringSubmatchIndex(html[start:])
	if loc == nil || loc[0] != 0 {
		return
	}
	tag := strings.ToLower(html[start+loc[2] : start+loc[3]])
	end := attrAwareOpenTagEnd(html, start)
	if end < 0 {
		return
	}
	attrs := html[start+1+len(tag) : end-1]
	harvest(html, start, end, tag, attrs, seen, elements)
}

func harvestStampableTags(html string, seen map[int]bool, elements *[]stampElement) {
	for _, tag := range stampableTags {
		openRe := regexp.MustCompile(`(?i)<` + regexp.QuoteMeta(tag) + `\b`)
		idx := 0
		for {
			loc := openRe.FindStringIndex(html[idx:])
			if loc == nil {
				break
			}
			start := idx + loc[0]
			end := attrAwareOpenTagEnd(html, start)
			if end < 0 {
				idx = start + 1
				continue
			}
			attrs := html[start+1+len(tag) : end-1]
			harvest(html, start, end, tag, attrs, seen, elements)
			idx = start + 1
		}
	}
}

func harvestOptInMarkers(html string, seen map[int]bool, elements *[]stampElement) {
	idx := 0
	for {
		loc := probeTagRe.FindStringSubmatchIndex(html[idx:])
		if loc == nil {
			break
		}
		start := idx + loc[0]
		tag := strings.ToLower(html[idx+loc[2] : idx+loc[3]])
		end := attrAwareOpenTagEnd(html, start)
		if end < 0 {
			idx = start + 1
			continue
		}
		attrs := html[start+1+len(tag) : end-1]
		if optInArtifactRe.MatchString(attrs) || optInClassRe.MatchString(attrs) {
			harvest(html, start, end, tag, attrs, seen, elements)
		}
		idx = start + 1
	}
}

// aidValueRe extracts the value of a data-odoc-aid attribute from an open tag's
// attribute string. Uses the same whitespace class as the stamper so it matches
// exactly what StampAids emitted.
var aidValueRe = regexp.MustCompile(`data-odoc-aid` + wsClass + `*=` + wsClass + `*"([^"]*)"`)

// ElementByAID locates the artifact whose stamped data-odoc-aid equals aid in an
// already-stamped document, returning its full outer HTML (open tag through close
// tag, or the self-terminating void tag) and lowercased tag name. It reuses the
// exact harvest/parse logic StampAids uses — matching on the aid the stamper
// already wrote — so element boundaries stay byte-identical to what was stamped;
// it does not recompute the content hash.
func ElementByAID(html, aid string) (outer, tag string, ok bool) {
	if aid == "" {
		return "", "", false
	}
	seen := map[int]bool{}
	var harvested []stampElement
	harvestStampableTags(html, seen, &harvested)
	harvestOptInMarkers(html, seen, &harvested)
	for _, e := range harvested {
		m := aidValueRe.FindStringSubmatch(e.attrs)
		if m != nil && m[1] == aid {
			return html[e.openStart:e.closeEnd], e.tag, true
		}
	}
	return "", "", false
}

// ReplaceElementByAID replaces the outer HTML of the artifact identified by aid
// with replacement, returning the rewritten document. It reuses ElementByAID's
// boundaries (same parse as the stamper) so exactly one element is swapped and
// the caller re-stamps via Publish. ok is false if aid is not present.
func ReplaceElementByAID(html, aid, replacement string) (result string, ok bool) {
	result, _, ok = ReplaceElementByAIDAt(html, aid, replacement)
	return result, ok
}

// ReplaceElementByAIDAt is ReplaceElementByAID that also returns boundary: the
// byte index in result where the replacement begins (== the replaced element's
// original openStart, since everything before it is byte-identical). The
// element/replace path adds InjectRootAIDAt's localRootOffset to this boundary to
// get the exact offset of the replacement's root '<', which it passes to
// StampAidsPinned so stamping pins exactly that root — not every previously-
// stamped element — to the injected aid. boundary is -1 when ok is false.
func ReplaceElementByAIDAt(html, aid, replacement string) (result string, boundary int, ok bool) {
	seen := map[int]bool{}
	var harvested []stampElement
	harvestStampableTags(html, seen, &harvested)
	harvestOptInMarkers(html, seen, &harvested)
	for _, e := range harvested {
		m := aidValueRe.FindStringSubmatch(e.attrs)
		if m != nil && m[1] == aid {
			return html[:e.openStart] + replacement + html[e.closeEnd:], e.openStart, true
		}
	}
	return "", -1, false
}

// SingleTopLevelTag reports the lowercased tag name if s parses to exactly one
// top-level element (with matching close or a void/self-closing tag) and nothing
// but whitespace around it. Used to reject multi-element or non-element fragments
// (and, via the raw-text harvest boundary, stray <script>/<style>) before an aid
// replace. ok is false otherwise.
func SingleTopLevelTag(s string) (tag string, ok bool) {
	trimmed := trimJSSpace(s)
	if trimmed == "" || trimmed[0] != '<' {
		return "", false
	}
	loc := probeTagRe.FindStringSubmatchIndex(trimmed)
	if loc == nil || loc[0] != 0 {
		return "", false
	}
	tag = strings.ToLower(trimmed[loc[2]:loc[3]])
	// Reject raw-text/script-like tags outright: they must never be injected via
	// an aid replace (script injection / boundary confusion).
	for _, rt := range rawTextTags {
		if tag == rt {
			return "", false
		}
	}
	openEnd := attrAwareOpenTagEnd(trimmed, loc[0])
	if openEnd < 0 {
		return "", false
	}
	// Only TRUE void tags may skip a close tag. A non-void tag written self-closed
	// (e.g. <section/>) is NOT void: the browser would swallow following siblings
	// into it, so require an explicit matching close tag (findCloseEnd path below).
	isVoid := voidTagRe.MatchString(tag)
	if isVoid {
		// Exactly one void element and nothing after it.
		return tag, trimJSSpace(trimmed[openEnd:]) == ""
	}
	closeEnd := findCloseEnd(trimmed, tag, openEnd)
	if closeEnd <= openEnd {
		return "", false
	}
	// Nothing but whitespace may follow the single element's close tag.
	return tag, trimJSSpace(trimmed[closeEnd:]) == ""
}

// SafeReplacementFragment gates an aid-replace fragment: it must be exactly one
// top-level element (SingleTopLevelTag) AND free of injection vectors anywhere in
// the string. SingleTopLevelTag stays a pure structural check (its own tests keep
// passing); the three content scans below run on the WHOLE fragment because the
// dangerous payloads (<div><script>…</script></div>, <img onerror=…>, href=
// javascript:…) hide at inner depth where a top-level-only check never looks.
func SafeReplacementFragment(s string) (tag string, ok bool) {
	tag, ok = SingleTopLevelTag(s)
	if !ok {
		return "", false
	}
	// 1) raw-text tags (script/style/textarea/title) at any depth.
	// 2) inline event handlers (on...=).
	// 3) javascript: URLs.
	if rawAnyRe.MatchString(s) || eventAttrRe.MatchString(s) || jsURLRe.MatchString(s) {
		return "", false
	}
	return tag, true
}

// HasDataOdocAttr reports whether s carries any data-odoc-* attribute. Callers
// reject hand-written replacements that carry stamper-owned attributes, which
// would create ambiguous DOM selectors after Publish re-stamps.
func HasDataOdocAttr(s string) bool {
	return dataOdocAnyRe.MatchString(s)
}

// InjectRootAID stamps data-odoc-aid="aid" onto the root open tag of fragment.
// See InjectRootAIDAt; this drops the returned root offset.
func InjectRootAID(fragment, aid string) string {
	out, _ := InjectRootAIDAt(fragment, aid)
	return out
}

// InjectRootAIDAt stamps data-odoc-aid="aid" onto the root open tag of fragment
// (a single top-level element, SafeReplacementFragment-validated, carrying no
// data-odoc-* of its own) and returns the byte index of that root's '<' in out.
// The element/replace path uses this so the replacement inherits the target's
// OLD aid: StampAidsPinned keeps that aid verbatim on the root at the reported
// offset, so the comment anchor persists across the re-stamp even when the
// tag/content changed. The fragment may carry leading whitespace (allowed by
// SafeReplacementFragment), so the root '<' is NOT necessarily at offset 0 —
// callers must add localRootOffset to the insertion boundary. Only the outermost
// element is stamped. Returns (fragment, -1) if it has no well-formed open tag.
func InjectRootAIDAt(fragment, aid string) (out string, localRootOffset int) {
	lt := strings.IndexByte(fragment, '<')
	if aid == "" || lt < 0 {
		return fragment, lt
	}
	openEnd := attrAwareOpenTagEnd(fragment, lt)
	if openEnd < 0 {
		return fragment, lt
	}
	// openEnd is just past '>'; the tag's last char is at openEnd-1. A void tag may
	// self-terminate ("... />"): insert the aid before that trailing slash so the
	// tag stays well-formed.
	insertAt := openEnd - 1
	if selfCloseEndRe.MatchString(fragment[lt+1 : insertAt]) {
		if slash := strings.LastIndexByte(fragment[:insertAt], '/'); slash > lt {
			insertAt = slash
		}
	}
	return fragment[:insertAt] + ` data-odoc-aid="` + aid + `"` + fragment[insertAt:], lt
}

// utf16Slice returns the first n UTF-16 code units of s, matching JS slice(0,n).
func utf16Slice(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

// StampAids stamps data-odoc-aid on every commentable artifact in rawHTML,
// (re)computing each element's aid from its content. Callers pass HTML that
// carries no stamper-owned attributes; any pre-existing data-odoc-aid is stripped
// before hashing so the output is purely content-addressed.
func StampAids(rawHTML string) StampResult {
	return stampAids(rawHTML, "", -1)
}

// StampAidsPinned is StampAids with exactly ONE element pinned: the element whose
// open tag begins at pinnedOffset keeps pinnedAID verbatim instead of getting a
// content hash, and is force-indexed even when its tag is not normally stampable
// (e.g. a safe <div>/<p> replacement root). Every OTHER element — including a
// stampable ancestor of the pinned root — follows normal content-addressed
// stamping, so an ancestor whose content changed rehashes rather than keeping a
// stale aid.
//
// The element/replace path uses this: after validation the backend injects the
// target's OLD aid onto the replacement root (InjectRootAID) at a known offset,
// so the swapped element keeps its identity and any comment anchored to it
// survives reconciliation even when the replacement's tag or content changed.
// pinnedOffset < 0 (or an offset that resolves to no element) degrades to plain
// StampAids.
func StampAidsPinned(rawHTML, pinnedAID string, pinnedOffset int) StampResult {
	return stampAids(rawHTML, pinnedAID, pinnedOffset)
}

// stampAids implements StampAids / StampAidsPinned. When pinnedOffset >= 0 the
// element whose open tag starts there is force-harvested (even if non-stampable)
// and keeps pinnedAID instead of a content hash.
func stampAids(rawHTML, pinnedAID string, pinnedOffset int) StampResult {
	headings := collectHeadings(rawHTML)
	nearestHeadingAt := func(idx int) *string {
		var best *string
		for i := range headings {
			if headings[i].end <= idx {
				t := headings[i].text
				best = &t
			} else {
				break
			}
		}
		return best
	}

	seen := map[int]bool{}
	var harvested []stampElement
	// Force-harvest the pinned root FIRST so seen[] blocks a duplicate if its tag
	// also happens to be stampable; this guarantees the pinned root is indexed even
	// when its tag (div/p/...) is not in stampableTags.
	if pinnedOffset >= 0 && pinnedAID != "" {
		harvestAt(rawHTML, pinnedOffset, seen, &harvested)
	}
	harvestStampableTags(rawHTML, seen, &harvested)
	harvestOptInMarkers(rawHTML, seen, &harvested)

	aids := []StampedArtifact{}
	elements := make([]stampElement, 0, len(harvested))
	for _, e := range harvested {
		cleanedAttrs := dataOdocAidRe.ReplaceAllString(e.attrs, "")
		cleanedInner := dataOdocAidRe2.ReplaceAllString(e.innerHTML, "")
		var aid string
		if pinnedAID != "" && e.openStart == pinnedOffset {
			aid = pinnedAID // pin exactly the replacement root
		} else {
			aid = aidFor(e.tag, cleanedInner, cleanedAttrs)
		}
		aids = append(aids, StampedArtifact{
			AID:     aid,
			Tag:     e.tag,
			Head:    utf16Slice(e.innerHTML, 80),
			Heading: nearestHeadingAt(e.openStart),
		})
		e.cleanedAttrs = cleanedAttrs
		e.aid = aid
		elements = append(elements, e)
	}

	// Apply stamps in reverse offset order so earlier offsets stay valid.
	sort.SliceStable(elements, func(i, j int) bool {
		return elements[i].openStart > elements[j].openStart
	})
	out := rawHTML
	for _, e := range elements {
		var stampedOpen string
		if e.isVoid {
			// Strip any trailing self-close slash from cleanedAttrs so we don't emit a
			// stray '/' before data-odoc-aid, then re-add exactly one closing slash for
			// a self-terminated void tag: <img ... data-odoc-aid="x"/>.
			attrs := selfCloseEndRe.ReplaceAllString(e.cleanedAttrs, "")
			selfClose := ""
			if selfCloseEndRe.MatchString(e.attrs) {
				selfClose = "/"
			}
			stampedOpen = "<" + e.tag + attrs + ` data-odoc-aid="` + e.aid + `"` + selfClose + ">"
		} else {
			stampedOpen = "<" + e.tag + e.cleanedAttrs + ` data-odoc-aid="` + e.aid + `">`
		}
		out = out[:e.openStart] + stampedOpen + out[e.openEnd:]
	}
	return StampResult{HTML: out, AIDs: aids}
}
