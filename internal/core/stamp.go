package core

import (
	"html"
	"mime"
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

// isStampableTag reports whether tag is in the stampable set (tag is already
// lowercased). Gates a replacement root to elements the stamper harvests.
func isStampableTag(tag string) bool {
	for _, t := range stampableTags {
		if t == tag {
			return true
		}
	}
	return false
}

// isForeignRootTag reports whether tag is a foreign (SVG/MathML) root, for which
// a trailing slash IS a genuine self-close (unlike HTML non-void tags), AND which
// establishes a foreign-content subtree: every descendant is likewise foreign, so
// a trailing slash self-closes there too. Only svg is currently stampable; math
// is listed so an added stampable <math> root reconstructs correctly. tag is
// already lowercased.
func isForeignRootTag(tag string) bool {
	return tag == "svg" || tag == "math"
}

var rawTextTags = []string{"script", "style", "textarea", "title"}

var intrinsicAttrs = []string{"viewBox", "src", "alt", "aria-label", "title"}

type stampElement struct {
	openStart int
	openEnd   int
	closeEnd  int
	// tag is the lowercased canonical name used for matching/index logic; origTag
	// is the ORIGINAL open-tag name bytes, preserved for reconstruction so a
	// case-sensitive foreign name (<linearGradient>) is emitted verbatim.
	tag       string
	origTag   string
	attrs     string
	innerHTML string
	isVoid    bool
	// inForeign is true when this element sits inside an SVG/MathML foreign-content
	// subtree (or IS a foreign root). In foreign content a trailing slash is a
	// genuine self-close on ANY element, so reconstruction keeps the slash terminal
	// and inserts the aid before it. HTML (non-foreign) elements never do this.
	inForeign    bool
	cleanedAttrs string
	aid          string
}

type heading struct {
	end  int
	text string
}

type contentNamespace uint8

const (
	namespaceHTML contentNamespace = iota
	namespaceSVG
	namespaceMathML
)

type parsedOpenTag struct {
	start, openEnd       int
	closeStart, closeEnd int
	tag, origTag         string
	attrs                string
	namespace            contentNamespace
	annotationHTML       bool
	foreignPopped        bool
}

type scanStats struct {
	stackOps             int
	annotationAttrParses int
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
	htmlCommentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	whitespaceRunRe = regexp.MustCompile(wsClass + `+`)
	tagStripRe      = regexp.MustCompile(`<[^>]+>`)
	// voidTagRe classifies TRUE HTML void elements only. iframe is NOT void: it
	// is a normal element that may hold fallback content, so its boundary runs
	// through the real </iframe> close (removed from the void set on purpose).
	voidTagRe = regexp.MustCompile(`(?i)^(area|base|br|col|embed|hr|img|input|link|meta|param|source|track|wbr)$`)
	// rawAnyRe matches a raw-text open tag at ANY nesting depth (not just top
	// level); used to reject injected <script>/<style>/... inside a fragment.
	rawAnyRe = regexp.MustCompile(`(?i)<(` + strings.Join(rawTextTags, "|") + `)\b`)
	// probeTagRe finds a candidate open-tag name; the trailing \b is deliberately
	// loose (it treats ':' and other non-name chars as a boundary), so callers
	// must go through probeOpenTagName, which re-validates that the name ends at an
	// HTML-appropriate boundary (ASCII whitespace, '/', '>', or end of string).
	probeTagRe = regexp.MustCompile(`(?i)<([a-z][\w-]*)`)
)

// probeOpenTagName reports whether s begins (at offset 0) with an HTML open tag
// and, if so, returns the [nameStart, nameEnd) byte range of its tag name. HTML
// tag-name tokenization: a leading ASCII letter, then name chars, and the name
// must TERMINATE at an HTML-appropriate boundary — ASCII whitespace, '/', '>',
// or end of string. A ':' or other non-boundary char right after the name (as in
// <section:x>) means this is NOT a plain <section> open tag, so ok is false and
// no phantom stamp/AID is minted. Hyphenated custom names (<my-element>) and
// mixed case (<Section>) still parse. This tightens probeTagRe's loose \b.
func probeOpenTagName(s string) (nameStart, nameEnd int, ok bool) {
	loc := probeTagRe.FindStringSubmatchIndex(s)
	if loc == nil || loc[0] != 0 {
		return 0, 0, false
	}
	nameEnd = loc[3]
	if nameEnd < len(s) {
		c := s[nameEnd]
		if !isASCIISpace(c) && c != '/' && c != '>' {
			return 0, 0, false
		}
	}
	return loc[2], nameEnd, true
}

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

// saltAwayFromPinned returns the content hash for the element, re-seeding Cyrb53
// (deterministic increasing salt) only when it would collide with an active
// pinnedAID so the immovable pinned identity wins and the collider moves.
// pinnedAID=="" disables salting: non-colliding hashes stay byte-identical, so
// identical artifacts share one aid. Salted output is still base36 (\w-safe).
func saltAwayFromPinned(tag, innerHTML, openAttrs, pinnedAID string) string {
	aid := aidFor(tag, innerHTML, openAttrs)
	if pinnedAID == "" || aid != pinnedAID {
		return aid
	}
	base := tag + "|" + openAttrs + "|" + innerHTML
	return saltAwayFromPinnedWithHasher(base, pinnedAID, 1<<16, Cyrb53)
}

func saltAwayFromPinnedWithHasher(base, pinnedAID string, maxAttempts uint32, hash func(string, uint32) string) string {
	for seed := uint32(1); seed <= maxAttempts; seed++ {
		if salted := hash(base, seed); salted != pinnedAID {
			return salted
		}
	}
	return pinnedAID + "0"
}

// attrAwareOpenTagEnd returns the index just past the > that closes the open tag.
// It keeps quoted values distinct from attribute names so malformed-but-
// recoverable quotes follow browser tokenization. Returns -1 if unterminated.
func attrAwareOpenTagEnd(html string, lt int) int {
	const (
		tagName = iota
		beforeAttr
		attrName
		afterAttrName
		beforeValue
		quotedValue
		unquotedValue
		afterQuotedValue
		selfClosingStartTag
	)
	state := tagName
	var quote byte
	for i := lt + 1; i < len(html); i++ {
		ch := html[i]
		switch state {
		case tagName:
			switch {
			case isASCIISpace(ch):
				state = beforeAttr
			case ch == '/':
				state = selfClosingStartTag
			case ch == '>':
				return i + 1
			}
		case beforeAttr:
			switch {
			case isASCIISpace(ch):
			case ch == '/':
				state = selfClosingStartTag
			case ch == '>':
				return i + 1
			default:
				state = attrName
			}
		case attrName:
			switch {
			case isASCIISpace(ch):
				state = afterAttrName
			case ch == '/':
				state = selfClosingStartTag
			case ch == '=':
				state = beforeValue
			case ch == '>':
				return i + 1
			}
		case afterAttrName:
			switch {
			case isASCIISpace(ch):
			case ch == '/':
				state = selfClosingStartTag
			case ch == '=':
				state = beforeValue
			case ch == '>':
				return i + 1
			default:
				state = attrName
			}
		case beforeValue:
			switch {
			case isASCIISpace(ch):
			case ch == '"' || ch == '\'':
				quote = ch
				state = quotedValue
			case ch == '>':
				return i + 1
			default:
				state = unquotedValue
			}
		case quotedValue:
			if ch == quote {
				state = afterQuotedValue
			}
		case unquotedValue:
			switch {
			case isASCIISpace(ch):
				state = beforeAttr
			case ch == '>':
				return i + 1
			}
		case afterQuotedValue:
			switch {
			case isASCIISpace(ch):
				state = beforeAttr
			case ch == '>':
				return i + 1
			case ch == '/':
				state = selfClosingStartTag
			default:
				state = attrName
			}
		case selfClosingStartTag:
			if ch == '>' {
				return i + 1
			}
			state = beforeAttr
			i-- // reconsume under before-attribute-name semantics
		}
	}
	return -1
}

// terminalSelfCloseSlash returns the index of a genuine terminal self-closing
// marker in attrs, or -1. A slash consumed by a quoted or unquoted value is data.
func terminalSelfCloseSlash(attrs string) int {
	const (
		beforeAttr = iota
		attrName
		afterAttrName
		beforeValue
		quotedValue
		unquotedValue
		afterQuotedValue
	)
	state := beforeAttr
	var quote byte
	terminal := func(i int) bool { return i == len(attrs)-1 }
	for i := 0; i < len(attrs); i++ {
		ch := attrs[i]
		switch state {
		case beforeAttr:
			switch {
			case isASCIISpace(ch):
			case ch == '/' && terminal(i):
				return i
			default:
				state = attrName
			}
		case attrName:
			switch {
			case isASCIISpace(ch):
				state = afterAttrName
			case ch == '=':
				state = beforeValue
			case ch == '/' && terminal(i):
				return i
			}
		case afterAttrName:
			switch {
			case isASCIISpace(ch):
			case ch == '=':
				state = beforeValue
			case ch == '/' && terminal(i):
				return i
			default:
				state = attrName
			}
		case beforeValue:
			switch {
			case isASCIISpace(ch):
			case ch == '"' || ch == '\'':
				quote = ch
				state = quotedValue
			default:
				state = unquotedValue
			}
		case quotedValue:
			if ch == quote {
				state = afterQuotedValue
			}
		case unquotedValue:
			if isASCIISpace(ch) {
				state = beforeAttr
			}
		case afterQuotedValue:
			switch {
			case isASCIISpace(ch):
				state = beforeAttr
			case ch == '/' && terminal(i):
				return i
			default:
				state = attrName
			}
		}
	}
	return -1
}

// stampedOpenTag preserves caller attribute bytes and inserts the canonical AID
// before trailing attribute whitespace and, when real, the self-close slash.
// Stripping the inserted attribute therefore reconstructs the original opening
// tag byte-for-byte for the next hash pass.
func stampedOpenTag(name, attrs, aid string, trueSelfClose bool) string {
	insertAt := len(attrs)
	if trueSelfClose {
		insertAt = terminalSelfCloseSlash(attrs)
		if insertAt < 0 {
			insertAt = len(attrs)
		}
	}
	for insertAt > 0 && isASCIISpace(attrs[insertAt-1]) {
		insertAt--
	}
	return "<" + name + attrs[:insertAt] + ` data-odoc-aid="` + aid + `"` + attrs[insertAt:] + ">"
}

// rootOpenTagAttrs returns the attribute slice of the fragment's single root open
// tag: everything after the tag name up to (but excluding) the closing '>' or a
// self-closing '/'. Quote-aware so a '>' inside a quoted value doesn't truncate
// it. ok is false if there is no well-formed root open tag. It never inspects
// child tags, text nodes, or attribute values as structure.
func rootOpenTagAttrs(fragment string) (attrs string, ok bool) {
	trimmed := trimJSSpace(fragment)
	lt := strings.IndexByte(trimmed, '<')
	if lt < 0 {
		return "", false
	}
	_, ne, ok := probeOpenTagName(trimmed[lt:])
	if !ok {
		return "", false
	}
	openEnd := attrAwareOpenTagEnd(trimmed, lt)
	if openEnd < 0 {
		return "", false
	}
	// lt+ne is just past the tag name; openEnd-1 is the '>'. Trim a trailing
	// self-closing slash so it is not read as an attribute-name char.
	inner := trimmed[lt+ne : openEnd-1]
	if i := terminalSelfCloseSlash(inner); i >= 0 {
		inner = inner[:i]
	}
	return inner, true
}

// forEachAttrName walks an open-tag attribute slice quote-aware and calls fn with
// each attribute NAME (lowercased). Values are skipped: a name is the run of
// name chars starting after whitespace; an optional =value (quoted or bare) that
// follows is consumed but never treated as a name. This is how we inspect real
// attribute names without matching literal strings inside values.
func forEachAttrName(attrs string, fn func(name string)) {
	i := 0
	n := len(attrs)
	for i < n {
		// Skip separators between attributes.
		for i < n && (isASCIISpace(attrs[i]) || attrs[i] == '/') {
			i++
		}
		if i >= n {
			break
		}
		// Read the attribute name (up to space, '=', '/', or end).
		start := i
		for i < n && !isASCIISpace(attrs[i]) && attrs[i] != '=' && attrs[i] != '/' {
			i++
		}
		name := attrs[start:i]
		if name != "" {
			fn(strings.ToLower(name))
		}
		// Skip optional = and its value so value bytes are never read as names.
		for i < n && isASCIISpace(attrs[i]) {
			i++
		}
		if i < n && attrs[i] == '=' {
			i++
			for i < n && isASCIISpace(attrs[i]) {
				i++
			}
			if i < n && (attrs[i] == '"' || attrs[i] == '\'') {
				q := attrs[i]
				i++
				for i < n && attrs[i] != q {
					i++
				}
				if i < n {
					i++ // closing quote
				}
			} else {
				for i < n && !isASCIISpace(attrs[i]) {
					i++
				}
			}
		}
	}
}

// stripAttribute removes every case-insensitive occurrence of target from an
// open-tag attribute slice, including quoted, unquoted, and valueless forms.
func stripAIDAttribute(attrs string) string {
	var out strings.Builder
	cursor := 0
	i := 0
	for i < len(attrs) {
		for i < len(attrs) && (isASCIISpace(attrs[i]) || attrs[i] == '/') {
			i++
		}
		if i >= len(attrs) {
			break
		}
		nameStart := i
		for i < len(attrs) && !isASCIISpace(attrs[i]) && attrs[i] != '=' && attrs[i] != '/' {
			i++
		}
		if i == nameStart {
			i++
			continue
		}
		nameEnd := i
		for i < len(attrs) && isASCIISpace(attrs[i]) {
			i++
		}
		valueEnd := nameEnd
		canonicalAID := false
		if i < len(attrs) && attrs[i] == '=' {
			i++
			for i < len(attrs) && isASCIISpace(attrs[i]) {
				i++
			}
			if i < len(attrs) && (attrs[i] == '"' || attrs[i] == '\'') {
				quote := attrs[i]
				i++
				valueStart := i
				for i < len(attrs) && attrs[i] != quote {
					i++
				}
				if quote == '"' && i < len(attrs) {
					value := attrs[valueStart:i]
					canonicalAID = len(value) >= 9 && len(value) <= 12
					for j := 0; canonicalAID && j < len(value); j++ {
						canonicalAID = value[j] >= '0' && value[j] <= '9' || value[j] >= 'a' && value[j] <= 'z'
					}
				}
				if i < len(attrs) {
					i++
				}
			} else {
				// In HTML's unquoted-attribute-value state, '/' is ordinary data.
				// Only whitespace can end the value and make a later slash an actual
				// self-closing marker (for example "aid=x />", not "aid=x/>").
				for i < len(attrs) && !isASCIISpace(attrs[i]) {
					i++
				}
			}
			valueEnd = i
		}
		if strings.EqualFold(attrs[nameStart:nameEnd], "data-odoc-aid") {
			removeStart := nameStart
			spaceStart := removeStart
			for spaceStart > cursor && isASCIISpace(attrs[spaceStart-1]) {
				spaceStart--
			}
			// Emission contributes exactly one ASCII space. Preserve preceding
			// formatting only for that canonical shape; otherwise the whole caller
			// separator belongs to the forged attribute and must not affect hashes.
			if canonicalAID && removeStart-spaceStart == 1 && attrs[spaceStart] == ' ' {
				removeStart--
			} else {
				removeStart = spaceStart
			}
			out.WriteString(attrs[cursor:removeStart])
			cursor = valueEnd
		}
	}
	out.WriteString(attrs[cursor:])
	return out.String()
}

// stripAllAIDs removes caller-supplied AIDs from every real opening tag in one
// structural pass before harvesting. This makes every ancestor hash independent
// of nested forged/stale AIDs without rescanning each artifact subtree.
//
// A removed attribute can expose a slash that was not originally a terminal
// self-close marker ("<svg/ data-odoc-aid=...>"). Keep a separating space in
// that case. Likewise, preserve the distinction between an unquoted value ending
// in slash and a following true self-close marker.
func stripAllAIDs(html string) string {
	opens := scanOpenTags(html)
	var out strings.Builder
	cursor := 0
	for _, open := range opens {
		attrs := open.attrs
		cleaned := stripAIDAttribute(attrs)
		if cleaned == attrs {
			continue // never normalize a caller tag unless an AID was removed
		}
		if terminalSelfCloseSlash(attrs) < 0 && terminalSelfCloseSlash(cleaned) >= 0 &&
			(open.namespace != namespaceHTML || voidTagRe.MatchString(open.tag)) {
			// Canonical output places an AID after a malformed slash on void/foreign
			// tags. Removing that AID must not turn the slash into a real self-close.
			cleaned += " "
		}
		attrsStart := open.start + 1 + len(open.origTag)
		attrsEnd := open.openEnd - 1
		out.WriteString(html[cursor:attrsStart])
		out.WriteString(cleaned)
		cursor = attrsEnd
	}
	if cursor == 0 {
		return html
	}
	out.WriteString(html[cursor:])
	return out.String()
}

func hasEventHandlerAttr(s string) bool {
	for _, open := range scanOpenTags(s) {
		found := false
		forEachAttrName(open.attrs, func(name string) {
			if len(name) <= 2 || name[0] != 'o' || name[1] != 'n' {
				return
			}
			for i := 2; i < len(name); i++ {
				if name[i] < 'a' || name[i] > 'z' {
					return
				}
			}
			found = true
		})
		if found {
			return true
		}
	}
	return false
}

// forEachAttr walks real attributes and reports their lowercased names and raw
// values. It is deliberately structural: text that only resembles an attribute
// inside another value is never reported.
func forEachAttr(attrs string, fn func(name, value string)) {
	i := 0
	for i < len(attrs) {
		for i < len(attrs) && (isASCIISpace(attrs[i]) || attrs[i] == '/') {
			i++
		}
		start := i
		for i < len(attrs) && !isASCIISpace(attrs[i]) && attrs[i] != '=' && attrs[i] != '/' {
			i++
		}
		if start == i {
			i++
			continue
		}
		name := strings.ToLower(attrs[start:i])
		for i < len(attrs) && isASCIISpace(attrs[i]) {
			i++
		}
		value := ""
		if i < len(attrs) && attrs[i] == '=' {
			i++
			for i < len(attrs) && isASCIISpace(attrs[i]) {
				i++
			}
			if i < len(attrs) && (attrs[i] == '"' || attrs[i] == '\'') {
				quote := attrs[i]
				i++
				valueStart := i
				for i < len(attrs) && attrs[i] != quote {
					i++
				}
				value = attrs[valueStart:i]
				if i < len(attrs) {
					i++
				}
			} else {
				valueStart := i
				for i < len(attrs) && !isASCIISpace(attrs[i]) {
					i++
				}
				value = attrs[valueStart:i]
			}
		}
		fn(name, value)
	}
}

func normalizedURLValue(value string) string {
	decoded := html.UnescapeString(value)
	var normalized strings.Builder
	for _, r := range decoded {
		if r <= 0x20 || r == 0x7f {
			continue
		}
		normalized.WriteRune(r)
	}
	return strings.ToLower(normalized.String())
}

func unsafeReplacementURL(value string) bool {
	url := normalizedURLValue(value)
	if strings.HasPrefix(url, "javascript:") || strings.HasPrefix(url, "vbscript:") {
		return true
	}
	if !strings.HasPrefix(url, "data:") {
		return false
	}
	media := strings.TrimPrefix(url, "data:")
	if comma := strings.IndexByte(media, ','); comma >= 0 {
		media = media[:comma]
	}
	if semi := strings.IndexByte(media, ';'); semi >= 0 {
		media = media[:semi]
	}
	mediaType, _, err := mime.ParseMediaType(media)
	if err != nil {
		return false
	}
	switch mediaType {
	case "text/html", "application/xhtml+xml", "text/xml", "application/xml", "image/svg+xml":
		return true
	default:
		return strings.HasSuffix(mediaType, "+xml")
	}
}

func unsafeReplacementAttrs(s string) bool {
	for _, open := range scanOpenTags(s) {
		attrs := make(map[string][]string)
		unsafe := false
		forEachAttr(open.attrs, func(name, value string) {
			// The tokenizer drops duplicate attributes; no sink may inspect a value
			// the browser will not retain.
			if _, exists := attrs[name]; exists {
				return
			}
			attrs[name] = []string{value}
			if name == "srcdoc" {
				unsafe = value != ""
				return
			}
			switch name {
			case "href", "src", "xlink:href", "action", "formaction":
				unsafe = unsafe || unsafeReplacementURL(value)
			case "data":
				unsafe = unsafe || open.tag == "object" && unsafeReplacementURL(value)
			}
		})
		if unsafe {
			return true
		}
		if open.namespace != namespaceSVG || (open.tag != "animate" && open.tag != "set") {
			continue
		}
		targets := attrs["attributename"]
		if len(targets) == 0 {
			continue
		}
		target := normalizedURLValue(targets[0])
		if target != "href" && target != "xlink:href" {
			continue
		}
		for _, name := range []string{"from", "to", "values"} {
			for _, value := range attrs[name] {
				candidates := []string{value}
				if name == "values" {
					candidates = strings.Split(html.UnescapeString(value), ";")
				}
				for _, candidate := range candidates {
					if unsafeReplacementURL(candidate) {
						return true
					}
				}
			}
		}
	}
	return false
}

// isASCIISpace reports whether b is an HTML whitespace byte.
func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

// isDataOdocName reports whether an attribute name is a real data-odoc-* name:
// the literal prefix "data-odoc-" (case-insensitive) followed by zero or more
// name chars. This covers the valueless form (data-odoc-artifact), mixed case,
// suffixes (data-odoc-aid2), and underscores (data-odoc-_private). "data-odoc"
// with no trailing hyphen (e.g. data-odoc / data-odocx) is NOT matched.
func isDataOdocName(name string) bool {
	return strings.HasPrefix(name, "data-odoc-")
}

// rootHasClassToken reports whether the fragment's single root open tag carries a
// class attribute whose space-separated token set contains token. Only the ROOT
// open tag is inspected (quote-aware), so a nested child's class never opts in.
func rootHasClassToken(fragment, token string) bool {
	attrs, ok := rootOpenTagAttrs(fragment)
	if !ok {
		return false
	}
	return classTokenMatch(attrs, token)
}

// classTokenMatch reports whether attrs contains a class="..." whose token list
// includes token. attrs is a single open-tag attribute slice.
func classTokenMatch(attrs, token string) bool {
	found := false
	// Re-scan quote-aware for a class value; forEachAttrName skips values, so pull
	// the class value directly.
	i := 0
	n := len(attrs)
	for i < n {
		for i < n && (isASCIISpace(attrs[i]) || attrs[i] == '/') {
			i++
		}
		start := i
		for i < n && !isASCIISpace(attrs[i]) && attrs[i] != '=' && attrs[i] != '/' {
			i++
		}
		name := strings.ToLower(attrs[start:i])
		for i < n && isASCIISpace(attrs[i]) {
			i++
		}
		var val string
		if i < n && attrs[i] == '=' {
			i++
			for i < n && isASCIISpace(attrs[i]) {
				i++
			}
			if i < n && (attrs[i] == '"' || attrs[i] == '\'') {
				q := attrs[i]
				i++
				vs := i
				for i < n && attrs[i] != q {
					i++
				}
				val = attrs[vs:i]
				if i < n {
					i++
				}
			} else {
				vs := i
				for i < n && !isASCIISpace(attrs[i]) {
					i++
				}
				val = attrs[vs:i]
			}
		}
		if name == "class" {
			for _, t := range strings.Fields(val) {
				if t == token {
					found = true
				}
			}
		}
	}
	return found
}

func attrValue(attrs, target string) (string, bool) {
	i := 0
	for i < len(attrs) {
		for i < len(attrs) && (isASCIISpace(attrs[i]) || attrs[i] == '/') {
			i++
		}
		start := i
		for i < len(attrs) && !isASCIISpace(attrs[i]) && attrs[i] != '=' && attrs[i] != '/' {
			i++
		}
		name := strings.ToLower(attrs[start:i])
		for i < len(attrs) && isASCIISpace(attrs[i]) {
			i++
		}
		value := ""
		if i < len(attrs) && attrs[i] == '=' {
			i++
			for i < len(attrs) && isASCIISpace(attrs[i]) {
				i++
			}
			if i < len(attrs) && (attrs[i] == '"' || attrs[i] == '\'') {
				quote := attrs[i]
				i++
				valueStart := i
				for i < len(attrs) && attrs[i] != quote {
					i++
				}
				value = attrs[valueStart:i]
				if i < len(attrs) {
					i++
				}
			} else {
				valueStart := i
				for i < len(attrs) && !isASCIISpace(attrs[i]) {
					i++
				}
				value = attrs[valueStart:i]
			}
		}
		if name == target {
			return value, true
		}
	}
	return "", false
}

// isRawTextTag reports whether tag (lowercased) is a raw-text/RCDATA element
// whose body must not be scanned for markup.
func isRawTextTag(tag string) bool {
	for _, rt := range rawTextTags {
		if tag == rt {
			return true
		}
	}
	return false
}

// rawTextCloseEnd returns the index in html just past a raw-text/RCDATA close
// tag for openTag at or after `from`, or -1 if none. Browser-aligned close
// recognition: `</tag` must be followed by an ASCII whitespace, '/', or '>'
// (so `</script>`, `</script >`, `</script/>`, `</script x>` all close), but
// `</scriptx>` is NOT a close. The returned end is just past the '>' that
// terminates the close tag (its attribute-like tail is consumed like the
// browser's end-tag token). Quote-unaware by design: an end tag has no quoted
// values in the tokenizer's RAWTEXT close path.
func rawTextCloseBoundary(html, openTag string, from int) (int, int) {
	lowerTag := strings.ToLower(openTag)
	tagLen := len(lowerTag)
	for i := from; i < len(html); i++ {
		if html[i] != '<' || i+1 >= len(html) || html[i+1] != '/' {
			continue
		}
		nameStart := i + 2
		nameEnd := nameStart + tagLen
		if nameEnd > len(html) || strings.ToLower(html[nameStart:nameEnd]) != lowerTag {
			continue
		}
		// The char after the tag name must end the name: whitespace, '/', or '>'.
		// Anything else (e.g. 'x' in </scriptx>) is not this close tag.
		if nameEnd == len(html) {
			continue // "</script" with no terminator: not a complete close
		}
		c := html[nameEnd]
		if !isASCIISpace(c) && c != '/' && c != '>' {
			continue
		}
		// Consume the end tag's tail up to and including the next '>'.
		gt := strings.IndexByte(html[nameEnd:], '>')
		if gt < 0 {
			return i, len(html)
		}
		return i, nameEnd + gt + 1
	}
	return -1, -1
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

// endTagBoundary reports whether s[i:] begins a browser-aligned end tag for tag
// (already lowercased) and, if so, returns the index just past the '>' that
// terminates it. Browser end-tag recognition: after `</tag` the next
// char must END the name — ASCII whitespace, '/', or '>' — so `</section>`,
// `</section >`, `</section/>`, `</section x>` (attribute-like tail) all close, but
// `</sectionx>` does NOT. The tail up to and including the next '>' is consumed
// like the browser's end-tag token; an unterminated tail runs to EOF. Quote-
// unaware by design: an end tag carries no quoted values in the tokenizer.
func endTagBoundary(s string, i int, tag string) (end int, ok bool) {
	if s[i] != '<' || i+1 >= len(s) || s[i+1] != '/' {
		return 0, false
	}
	nameStart := i + 2
	nameEnd := nameStart + len(tag)
	if nameEnd > len(s) || !strings.EqualFold(s[nameStart:nameEnd], tag) {
		return 0, false
	}
	if nameEnd == len(s) {
		return 0, false // "</tag" with no terminator is not a complete close
	}
	c := s[nameEnd]
	if !isASCIISpace(c) && c != '/' && c != '>' {
		return 0, false // e.g. the 'x' in </sectionx>
	}
	gt := strings.IndexByte(s[nameEnd:], '>')
	if gt < 0 {
		return len(s), true // unterminated tail: consume the rest
	}
	return nameEnd + gt + 1, true
}

func endTagName(s string, i int) (string, bool) {
	if i+2 >= len(s) || s[i] != '<' || s[i+1] != '/' {
		return "", false
	}
	nameStart := i + 2
	if !isASCIILetter(s[nameStart]) {
		return "", false
	}
	nameEnd := nameStart + 1
	for nameEnd < len(s) && isTagNameByte(s[nameEnd]) {
		nameEnd++
	}
	if nameEnd == len(s) {
		return "", false
	}
	c := s[nameEnd]
	if !isASCIISpace(c) && c != '/' && c != '>' {
		return "", false
	}
	return strings.ToLower(s[nameStart:nameEnd]), true
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isTagNameByte(c byte) bool {
	return isASCIILetter(c) || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// findCloseEnd finds the matching close tag for a non-void element opened at
// openEnd and returns (closeStart, closeEnd): the '<' of the close tag and the
// index just past its '>'. It is context-aware exactly like forEachOpenTag —
// comments and raw-text/RCDATA bodies are skipped as text so a same-name open or
// close that appears only inside `<!-- ... -->` or a script/style/textarea/title
// body never affects nesting — and it counts nested same-name elements to find
// the matching close. Close recognition is browser-aligned (endTagBoundary), so
// `</tag/>`, `</tag x>`, and mixed case/whitespace tails all close. Returns
// (openEnd, openEnd) when no matching close is found so callers treat innerHTML
// as empty and the outer boundary as the open tag alone.
func findCloseEndIn(opens []parsedOpenTag, tag string, openEnd int) (closeStart, closeEnd int) {
	for _, open := range opens {
		if open.openEnd == openEnd && open.tag == tag {
			return open.closeStart, open.closeEnd
		}
	}
	return openEnd, openEnd
}

func findCloseEnd(html, tag string, openEnd int) (closeStart, closeEnd int) {
	return findCloseEndIn(scanOpenTags(html), tag, openEnd)
}

func harvest(html string, open parsedOpenTag, seen map[int]bool, elements *[]stampElement) {
	openStart := open.start
	if seen[openStart] {
		return
	}
	// Only TRUE HTML void elements are void for boundary purposes. A trailing
	// slash on a non-void HTML tag (<section/>) is NOT a self-close: the browser
	// keeps the element open and swallows following siblings, so the boundary must
	// run through the element's real close tag. This mirrors SingleTopLevelTag,
	// which also classifies voidness by tag name alone. Inside foreign content
	// (SVG/MathML) a trailing slash IS a self-close, but that is handled by
	// findCloseEnd returning no close (closeEnd==openEnd) plus inForeign at
	// reconstruction — we do NOT mark it isVoid (it is not an HTML void element).
	inForeign := open.namespace != namespaceHTML
	isVoid := !inForeign && voidTagRe.MatchString(open.tag)
	closeEnd := open.openEnd
	innerHTML := ""
	foreignSelfClose := inForeign && terminalSelfCloseSlash(open.attrs) >= 0
	if !isVoid && !foreignSelfClose {
		// findCloseEnd returns the close tag's actual '<' (closeStart) and its end.
		// innerHTML is [openEnd, closeStart): computing it from closeStart (never by
		// subtracting len("</"+tag+">")) stays correct for browser-aligned close tails
		// like </section/>, </section x>, and mixed case/whitespace forms.
		closeEnd = open.closeEnd
		if open.closeStart >= open.openEnd && open.closeStart <= len(html) {
			innerHTML = html[open.openEnd:open.closeStart]
		}
	}
	seen[openStart] = true
	*elements = append(*elements, stampElement{
		openStart: openStart, openEnd: open.openEnd, closeEnd: closeEnd,
		tag: open.tag, origTag: open.origTag, inForeign: inForeign,
		attrs: open.attrs, innerHTML: innerHTML, isVoid: isVoid,
	})
}

// harvestAt force-harvests the element whose open tag begins at start, whatever
// its tag. StampAidsPinned uses it so the pinned replacement root is indexed at a
// known offset even before the tag/opt-in harvesters reach it. No-op if start is
// out of range, not a '<', or the open tag is unterminated. inForeign is derived
// from foreignContextAt so a pinned foreign root/descendant reconstructs with a
// genuine self-close.
func harvestAt(html string, opens []parsedOpenTag, start int, seen map[int]bool, elements *[]stampElement) {
	for _, open := range opens {
		if open.start == start {
			harvest(html, open, seen, elements)
			return
		}
	}
}

// harvestStampableTags harvests every stampable-tag element via the shared
// structural walker, so comment and raw-text/RCDATA context is honored: a
// <section>/<img>/... that appears only inside a comment or a script/style/
// textarea/title body (or after a malformed terminator) is text, not an element,
// and is neither indexed nor mutated. Retains document offsets, tag, and attrs.
func harvestStampableTags(html string, opens []parsedOpenTag, seen map[int]bool, elements *[]stampElement) {
	for _, open := range opens {
		if isStampableTag(open.tag) {
			harvest(html, open, seen, elements)
		}
	}
}

func harvestOptInMarkers(html string, opens []parsedOpenTag, seen map[int]bool, elements *[]stampElement) {
	for _, open := range opens {
		if hasOptInMarker(open.attrs) {
			harvest(html, open, seen, elements)
		}
	}
}

// hasOptInMarker reports whether an open tag's attribute slice carries a valid
// opt-in marker: the valueless data-odoc-artifact attribute OR a class token
// "odoc-artifact". Both are inspected quote-aware on real attribute names/values,
// so persistent harvesting matches what IsHarvestableReplacementRoot accepts at
// pin time across every quote style (double, single, unquoted).
func hasOptInMarker(attrs string) bool {
	if classTokenMatch(attrs, "odoc-artifact") {
		return true
	}
	found := false
	forEachAttrName(attrs, func(name string) {
		if name == "data-odoc-artifact" {
			found = true
		}
	})
	return found
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
	opens := scanOpenTags(html)
	seen := map[int]bool{}
	var harvested []stampElement
	harvestStampableTags(html, opens, seen, &harvested)
	harvestOptInMarkers(html, opens, seen, &harvested)
	// Document order: match the FIRST element carrying aid, as the browser's
	// querySelector would, so server lookup and the DOM agree on the target.
	sort.SliceStable(harvested, func(i, j int) bool {
		return harvested[i].openStart < harvested[j].openStart
	})
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
	opens := scanOpenTags(html)
	seen := map[int]bool{}
	var harvested []stampElement
	harvestStampableTags(html, opens, seen, &harvested)
	harvestOptInMarkers(html, opens, seen, &harvested)
	// Document order: replace the FIRST element carrying aid, matching the browser
	// (and ElementByAID) so server-side replace and the DOM target the same node.
	sort.SliceStable(harvested, func(i, j int) bool {
		return harvested[i].openStart < harvested[j].openStart
	})
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
	ns, ne, ok := probeOpenTagName(trimmed)
	if !ok {
		return "", false
	}
	tag = strings.ToLower(trimmed[ns:ne])
	// Reject raw-text/script-like tags outright: they must never be injected via
	// an aid replace (script injection / boundary confusion).
	for _, rt := range rawTextTags {
		if tag == rt {
			return "", false
		}
	}
	openEnd := attrAwareOpenTagEnd(trimmed, 0)
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
	attrs := trimmed[ne : openEnd-1]
	if isForeignRootTag(tag) && terminalSelfCloseSlash(attrs) >= 0 {
		return tag, trimJSSpace(trimmed[openEnd:]) == ""
	}
	closeStart, closeEnd := findCloseEnd(trimmed, tag, openEnd)
	if closeEnd <= openEnd || closeStart < openEnd {
		return "", false
	}
	// Nothing but whitespace may follow the single element's close tag.
	return tag, trimJSSpace(trimmed[closeEnd:]) == ""
}

// SafeReplacementFragment is a defense-in-depth guardrail for automated
// replacement, not a complete HTML sanitizer. It rejects dangerous structures,
// real event attributes, srcdoc, and executable URL schemes at any depth.
func SafeReplacementFragment(s string) (tag string, ok bool) {
	tag, ok = SingleTopLevelTag(s)
	if !ok {
		return "", false
	}
	if rawAnyRe.MatchString(s) || hasEventHandlerAttr(s) || unsafeReplacementAttrs(s) {
		return "", false
	}
	return tag, true
}

// HasDataOdocAttr reports whether s carries any real data-odoc-* attribute on an
// HTML open tag. It inspects actual open-tag attribute NAMES (quote-aware),
// never raw text or attribute values: a text node or a value that merely
// contains the literal string "data-odoc-..." is NOT a match, while the valueless
// form (data-odoc-artifact), mixed case, suffixes (data-odoc-aid2), and
// underscores (data-odoc-_private) all are. Callers reject hand-written
// replacements that carry stamper-owned attributes (ambiguous DOM selectors after
// a re-stamp).
func HasDataOdocAttr(s string) bool {
	found := false
	forEachOpenTag(s, func(_, _ int, _, _ string, _ bool, attrs string) {
		forEachAttrName(attrs, func(name string) {
			if isDataOdocName(name) {
				found = true
			}
		})
	})
	return found
}

// commentEnd returns the index just past an HTML comment that opens at lt (where
// s[lt]=='<'), or -1 if s[lt:] does not open a comment. Browser-aligned enough
// for the malformed terminators real documents hit: after "<!--" the tokenizer's
// comment-start / comment-start-dash states treat "<!-->" and "<!--->" as ABRUPT
// closings (empty comment) rather than requiring a full "-->"; otherwise the
// comment content runs to the first valid terminator — either "-->" (comment-end
// state) or "--!>" (comment-end-bang state) — choosing whichever appears EARLIER,
// or EOF if neither is present. Recognizing "--!>" keeps a following real tag
// visible instead of being swallowed by an over-scanned comment.
func commentEnd(s string, lt int) int {
	n := len(s)
	if lt+3 >= n || s[lt+1] != '!' || s[lt+2] != '-' || s[lt+3] != '-' {
		return -1
	}
	p := lt + 4
	// Abrupt closings: "<!-->" (comment-start '>') and "<!--->" (comment-start-dash
	// '>') produce an empty comment that ends right there.
	if p < n && s[p] == '>' {
		return p + 1
	}
	if p+1 < n && s[p] == '-' && s[p+1] == '>' {
		return p + 2
	}
	// Normal content: end at the EARLIEST valid terminator, "-->" or "--!>". Both
	// close the comment; the browser reaches whichever comes first in the source, so
	// a comment written with "--!>" does not over-scan past a following real element.
	// Unterminated ⇒ consume the rest.
	dash := strings.Index(s[p:], "-->")
	bang := strings.Index(s[p:], "--!>")
	switch {
	case dash < 0 && bang < 0:
		return n
	case bang < 0 || (dash >= 0 && dash <= bang):
		return p + dash + len("-->")
	default:
		return p + bang + len("--!>")
	}
}

// namespaceForChild applies the foreign-content integration-point rules.
func namespaceForChild(parent parsedOpenTag, childTag string) contentNamespace {
	if parent.foreignPopped {
		switch childTag {
		case "svg":
			return namespaceSVG
		case "math":
			return namespaceMathML
		default:
			return namespaceHTML
		}
	}
	base := parent.namespace
	switch parent.namespace {
	case namespaceSVG:
		if parent.tag == "foreignobject" || parent.tag == "desc" || parent.tag == "title" {
			base = namespaceHTML
		}
	case namespaceMathML:
		switch parent.tag {
		case "mi", "mo", "mn", "ms", "mtext":
			if childTag != "mglyph" && childTag != "malignmark" {
				base = namespaceHTML
			}
		case "annotation-xml":
			if childTag == "svg" {
				return namespaceSVG
			}
			if parent.annotationHTML {
				base = namespaceHTML
			}
		}
	}
	if base == namespaceHTML {
		switch childTag {
		case "svg":
			return namespaceSVG
		case "math":
			return namespaceMathML
		}
	}
	return base
}

// foreignBreakoutStart reports start tags that the HTML parser reprocesses in
// the HTML namespace after popping active foreign content.
func foreignBreakoutStart(tag, attrs string) bool {
	switch tag {
	case "b", "big", "blockquote", "body", "br", "center", "code", "dd", "div", "dl", "dt",
		"em", "embed", "h1", "h2", "h3", "h4", "h5", "h6", "head", "hr", "i", "img",
		"li", "listing", "menu", "meta", "nobr", "ol", "p", "pre", "ruby", "s", "small",
		"span", "strong", "strike", "sub", "sup", "table", "tt", "u", "ul", "var":
		return true
	case "font":
		for _, name := range []string{"color", "face", "size"} {
			if _, ok := attrValue(attrs, name); ok {
				return true
			}
		}
	}
	return false
}

func scanOpenTags(s string) []parsedOpenTag {
	return scanOpenTagsWithStats(s, nil)
}

func scanOpenTagsWithStats(s string, stats *scanStats) []parsedOpenTag {
	var opens []parsedOpenTag
	var stack []int
	var stackTagPrev []int
	stackTopByTag := make(map[string]int)
	popStack := func(newLen int) {
		for len(stack) > newLen {
			pos := len(stack) - 1
			idx := stack[pos]
			stackTopByTag[opens[idx].tag] = stackTagPrev[pos]
			stack = stack[:pos]
			stackTagPrev = stackTagPrev[:pos]
			if stats != nil {
				stats.stackOps++
			}
		}
	}
	popForeign := func(tokenTag string) bool {
		popped := false
		for pos := len(stack) - 1; pos >= 0 && namespaceForChild(opens[stack[pos]], tokenTag) != namespaceHTML; pos-- {
			opens[stack[pos]].foreignPopped = true
			popped = true
		}
		return popped
	}
	for i := 0; i < len(s); {
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			break
		}
		lt += i
		if ce := commentEnd(s, lt); ce >= 0 {
			i = ce
			continue
		}
		if lt+1 < len(s) && s[lt+1] == '/' {
			if len(stack) > 0 {
				pos := len(stack) - 1
				idx := stack[pos]
				if closeEnd, ok := endTagBoundary(s, lt, opens[idx].tag); ok {
					opens[idx].closeStart = lt
					opens[idx].closeEnd = closeEnd
					popStack(pos)
					i = closeEnd
					continue
				}
			}
			closeTag, ok := endTagName(s, lt)
			if !ok {
				i = lt + 1
				continue
			}
			closeEnd, ok := endTagBoundary(s, lt, closeTag)
			if !ok {
				i = lt + 1
				continue
			}
			// Foreign-content </p> and </br> tokens pop foreign nodes, then are
			// reprocessed in HTML mode. Keep source frames only for boundaries.
			if (closeTag == "p" || closeTag == "br") && popForeign(closeTag) {
				if closeTag == "br" {
					i = closeEnd
					continue
				}
			}
			pos, active := stackTopByTag[closeTag]
			if !active || pos < 0 {
				i = closeEnd
				continue
			}
			idx := stack[pos]
			opens[idx].closeStart = lt
			opens[idx].closeEnd = closeEnd
			popStack(pos)
			i = closeEnd
			continue
		}
		if lt+1 < len(s) && (s[lt+1] == '!' || s[lt+1] == '?') {
			i = lt + 1
			continue
		}
		_, ne, ok := probeOpenTagName(s[lt:])
		if !ok {
			i = lt + 1
			continue
		}
		openEnd := attrAwareOpenTagEnd(s, lt)
		if openEnd < 0 {
			break
		}
		origTag := s[lt+1 : lt+ne]
		tag := strings.ToLower(origTag)
		attrs := s[lt+ne : openEnd-1]
		ns := namespaceHTML
		if len(stack) > 0 {
			ns = namespaceForChild(opens[stack[len(stack)-1]], tag)
			if ns != namespaceHTML && foreignBreakoutStart(tag, attrs) {
				// Source frames remain only for close-boundary accounting.
				popForeign(tag)
				ns = namespaceHTML
			}
		} else if tag == "svg" {
			ns = namespaceSVG
		} else if tag == "math" {
			ns = namespaceMathML
		}
		annotationHTML := false
		if ns == namespaceMathML && tag == "annotation-xml" {
			if stats != nil {
				stats.annotationAttrParses++
			}
			if encoding, found := attrValue(attrs, "encoding"); found {
				encoding = strings.ToLower(strings.TrimSpace(html.UnescapeString(encoding)))
				annotationHTML = encoding == "text/html" || encoding == "application/xhtml+xml"
			}
		}
		opens = append(opens, parsedOpenTag{
			start: lt, openEnd: openEnd, closeStart: openEnd, closeEnd: openEnd,
			tag: tag, origTag: origTag, attrs: attrs, namespace: ns,
			annotationHTML: annotationHTML,
		})
		idx := len(opens) - 1
		selfClosed := ns != namespaceHTML && terminalSelfCloseSlash(attrs) >= 0
		if !selfClosed && (ns != namespaceHTML || !voidTagRe.MatchString(tag)) {
			previous, ok := stackTopByTag[tag]
			if !ok {
				previous = -1
			}
			stack = append(stack, idx)
			stackTagPrev = append(stackTagPrev, previous)
			stackTopByTag[tag] = len(stack) - 1
			if stats != nil {
				stats.stackOps++
			}
		}
		if ns == namespaceHTML && isRawTextTag(tag) {
			closeStart, closeEnd := rawTextCloseBoundary(s, tag, openEnd)
			if closeEnd < 0 {
				closeEnd = len(s)
			} else {
				opens[idx].closeStart = closeStart
				opens[idx].closeEnd = closeEnd
			}
			if len(stack) > 0 && stack[len(stack)-1] == idx {
				popStack(len(stack) - 1)
			}
			i = closeEnd
			continue
		}
		i = openEnd
	}
	return opens
}

// forEachOpenTag walks real open tags with namespace-aware foreign tracking.
func forEachOpenTag(s string, fn func(start, openEnd int, tag, origTag string, inForeign bool, attrs string)) {
	for _, open := range scanOpenTags(s) {
		fn(open.start, open.openEnd, open.tag, open.origTag, open.namespace != namespaceHTML, open.attrs)
	}
}

// IsHarvestableReplacementRoot reports whether the root element of fragment is
// one the stamper actually harvests, so a pinned aid on it survives every later
// plain re-stamp. True only when the SINGLE ROOT open tag is a stampable tag OR
// carries the caller-writable opt-in (class token "odoc-artifact") ON THE ROOT
// ITSELF. The opt-in is checked quote-aware on the root open tag only: a nested
// child carrying class="odoc-artifact" (or the token buried in a text node or
// another attribute's value) does NOT opt the root in. A bare non-addressable
// root (div/p/... with no root opt-in) is NOT harvestable: pinning it would work
// once but the aid would vanish on the next publish, silently losing an anchored
// comment, so callers reject it with a 400. fragment must already be
// SafeReplacementFragment-valid.
func IsHarvestableReplacementRoot(fragment string) bool {
	tag, ok := SingleTopLevelTag(fragment)
	if !ok {
		return false
	}
	if isStampableTag(tag) {
		return true
	}
	// Only the class opt-in is caller-writable (data-odoc-artifact is rejected by
	// HasDataOdocAttr) and only when it sits on the ROOT open tag, matching what
	// harvestOptInMarkers re-harvests on every re-stamp.
	return rootHasClassToken(fragment, "odoc-artifact")
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
	// Empty aid is a no-op with NO usable injection point: report -1 so a caller
	// that adds localRootOffset to a boundary cannot accidentally shift by lt.
	if aid == "" {
		return fragment, -1
	}
	if lt < 0 {
		return fragment, -1
	}
	openEnd := attrAwareOpenTagEnd(fragment, lt)
	if openEnd < 0 {
		return fragment, -1
	}
	// openEnd is just past '>'; the tag's last char is at openEnd-1. A void tag may
	// self-terminate ("... />"): insert the aid before that trailing slash so the
	// tag stays well-formed. Attribute-state parsing keeps slashes in quoted and
	// unquoted values as data.
	insertAt := openEnd - 1
	_, nameEnd, ok := probeOpenTagName(fragment[lt:])
	if ok {
		attrsStart := lt + nameEnd
		if slash := terminalSelfCloseSlash(fragment[attrsStart:insertAt]); slash >= 0 {
			insertAt = attrsStart + slash
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

// StampAidsPinned is StampAids with exactly ONE element pinned: the element at
// pinnedOffset keeps pinnedAID verbatim; every other element is content-addressed
// as usual (so a stampable ancestor whose content changed rehashes, not keeping a
// stale aid). An ordinary hash that collides with the pin is salted away (pinned
// identity wins); non-colliding hashes are unchanged.
//
// Used by element/replace: the backend injects the target's OLD aid onto the
// replacement root at a known offset so the immediate replacement version can
// reconcile the anchor and fingerprint atomically. The caller restricts the root
// to a harvestable element (IsHarvestableReplacementRoot) so later plain stamps
// keep it addressable under normal content-derived identity. An invalid/missing
// offset degrades to plain StampAids (no pin, no salting).
func StampAidsPinned(rawHTML, pinnedAID string, pinnedOffset int) StampResult {
	return stampAids(rawHTML, pinnedAID, pinnedOffset)
}

// stampAids implements StampAids / StampAidsPinned. When pinnedOffset >= 0 the
// element whose open tag starts there is force-harvested and keeps pinnedAID
// instead of a content hash.
func stampAids(rawHTML, pinnedAID string, pinnedOffset int) StampResult {
	pinnedOrdinal := -1
	if pinnedAID != "" && pinnedOffset >= 0 {
		for i, open := range scanOpenTags(rawHTML) {
			if open.start == pinnedOffset {
				pinnedOrdinal = i
				break
			}
		}
	}
	rawHTML = stripAllAIDs(rawHTML)
	if pinnedOrdinal >= 0 {
		opens := scanOpenTags(rawHTML)
		if pinnedOrdinal < len(opens) {
			pinnedOffset = opens[pinnedOrdinal].start
		}
	}
	headings := collectHeadings(rawHTML)
	opens := scanOpenTags(rawHTML)
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
	// Force-harvest the pinned root FIRST so seen[] blocks a duplicate if the tag
	// harvesters also reach it; this fixes the pinned root at its known offset.
	if pinnedOffset >= 0 && pinnedAID != "" {
		harvestAt(rawHTML, opens, pinnedOffset, seen, &harvested)
	}
	harvestStampableTags(rawHTML, opens, seen, &harvested)
	harvestOptInMarkers(rawHTML, opens, seen, &harvested)
	// Iterate/index in DOCUMENT ORDER (ascending openStart). Harvesting groups by
	// tag, but the browser resolves [data-odoc-aid="x"] to the FIRST such element in
	// document order; ordering here keeps the aid index and ElementByAID consistent
	// with what the browser picks.
	sort.SliceStable(harvested, func(i, j int) bool {
		return harvested[i].openStart < harvested[j].openStart
	})

	// The pin is ACTIVE only when pinnedAID is set AND some harvested element sits
	// at pinnedOffset. An invalid/missing offset (< 0, or one that resolves to no
	// element) must degrade to plain StampAids: no element keeps pinnedAID, so there
	// is no immovable identity to protect and we must NOT salt ordinary colliders
	// (that would perturb content hashes for no reason). effectivePinned is "" in
	// that case, disabling saltAwayFromPinned entirely.
	effectivePinned := ""
	if pinnedAID != "" {
		for _, e := range harvested {
			if e.openStart == pinnedOffset {
				effectivePinned = pinnedAID
				break
			}
		}
	}

	aids := []StampedArtifact{}
	elements := make([]stampElement, 0, len(harvested))
	for _, e := range harvested {
		cleanedAttrs := stripAIDAttribute(e.attrs)
		cleanedInner := e.innerHTML
		var aid string
		if effectivePinned != "" && e.openStart == pinnedOffset {
			aid = pinnedAID // pin exactly the replacement root for this stamp
		} else {
			// Ordinary content-addressed hash. Salted away ONLY when an active pin's aid
			// collides; otherwise byte-identical to before, so two genuinely identical
			// artifacts still share one aid (content-addressing).
			aid = saltAwayFromPinned(e.tag, cleanedInner, cleanedAttrs, effectivePinned)
		}
		artifact := StampedArtifact{
			AID:     aid,
			Tag:     e.tag,
			Head:    utf16Slice(e.innerHTML, 80),
			Heading: nearestHeadingAt(e.openStart),
		}
		// Older releases hashed iframe attributes but excluded fallback content.
		// Preserve that value only as reconciliation metadata: it is never emitted
		// into HTML and never defines replacement boundaries.
		if e.tag == "iframe" {
			legacyAID := aidFor("iframe", "", cleanedAttrs)
			if legacyAID != aid {
				artifact.LegacyAIDs = []string{legacyAID}
			}
		}
		aids = append(aids, artifact)
		e.cleanedAttrs = cleanedAttrs
		e.aid = aid
		elements = append(elements, e)
	}

	var out strings.Builder
	out.Grow(len(rawHTML) + len(elements)*32)
	cursor := 0
	for _, e := range elements {
		out.WriteString(rawHTML[cursor:e.openStart])
		// name is the ORIGINAL open-tag name bytes: emitting origTag (not the
		// lowercased e.tag) preserves case-sensitive foreign names such as
		// <linearGradient>. Matching/index logic keeps using e.tag (lowercase).
		name := e.origTag
		var stampedOpen string
		if e.isVoid {
			stampedOpen = stampedOpenTag(name, e.cleanedAttrs, e.aid, terminalSelfCloseSlash(e.cleanedAttrs) >= 0)
		} else if e.inForeign && e.closeEnd == e.openEnd && terminalSelfCloseSlash(e.attrs) >= 0 {
			// Genuinely self-closing foreign element: it sits in SVG/MathML foreign
			// content (inForeign) with no real close tag (closeEnd == openEnd, empty
			// inner) and a terminal unquoted slash. This covers foreign roots (<svg/>),
			// nested foreign roots (inner <svg/> in <svg><svg/></svg>), and opt-in
			// descendants (<path class="odoc-artifact"/>). The aid goes BEFORE the slash
			// so self-closing semantics survive: <path ... data-odoc-aid="x"/>. HTML
			// non-void <section/> never reaches here — not in foreign content and it
			// spans to a real close (closeEnd > openEnd) — so it stays non-self-closing.
			stampedOpen = stampedOpenTag(name, e.cleanedAttrs, e.aid, true)
		} else {
			stampedOpen = stampedOpenTag(name, e.cleanedAttrs, e.aid, false)
		}
		out.WriteString(stampedOpen)
		cursor = e.openEnd
	}
	out.WriteString(rawHTML[cursor:])
	return StampResult{HTML: out.String(), AIDs: aids}
}
