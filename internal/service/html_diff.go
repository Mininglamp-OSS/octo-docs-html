package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var errDiffLimit = errors.New("diff complexity limit exceeded")

const (
	maxDiffNodes        = 4000
	maxDiffDepth        = 256
	maxDiffComparisons  = 200000
	maxDiffCompareBytes = 16 << 20
	maxDiffCompareText  = 4096
	maxDiffRawScanBytes = 16 << 20
	maxDiffChanges      = 1000
	maxDiffInputLines   = 12000
	maxDiffHunkLines    = 2000
	maxDiffTagBytes     = 256
	maxDiffPathBytes    = 16 << 10
	maxDiffPathsBytes   = 2 << 20
	maxDiffSnippetBytes = 8 << 10
	maxDiffOpeningBytes = 1024
	maxDiffOutputBytes  = 512 << 10
	diffContextLines    = 3
)

// VersionDiff is a bounded structural and source-level comparison of two HTML versions.
type VersionDiff struct {
	From      int             `json:"from"`
	To        int             `json:"to"`
	Summary   DiffSummary     `json:"summary"`
	Changes   []ElementChange `json:"changes"`
	CodeHunks []CodeHunk      `json:"code_hunks"`
}

// DiffSummary counts bounded element-level changes by kind.
type DiffSummary struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Modified int `json:"modified"`
}

// ElementChange describes one matched, added, or removed local DOM subtree.
type ElementChange struct {
	Kind       string `json:"kind"`
	BeforeAID  string `json:"before_aid,omitempty"`
	AfterAID   string `json:"after_aid,omitempty"`
	DOMPath    string `json:"dom_path"`
	BeforePath string `json:"before_path,omitempty"`
	AfterPath  string `json:"after_path,omitempty"`
	BeforeHTML string `json:"before_html,omitempty"`
	AfterHTML  string `json:"after_html,omitempty"`
}

// CodeHunk is a normalized unified-style HTML source hunk.
type CodeHunk struct {
	OldStart int      `json:"old_start"`
	OldLines int      `json:"old_lines"`
	NewStart int      `json:"new_start"`
	NewLines int      `json:"new_lines"`
	Lines    []string `json:"lines"`
}

type htmlDiffNode struct {
	tag         string
	aid         string
	attrs       map[string]string
	text        string
	textParts   []string
	textBounds  []int
	textDigest  string
	compareText string
	signature   string
	path        string
	outer       string
	parent      int
	children    []int
	childTags   []string
	siblingPos  int
	order       int
}

type diffOpenNode struct {
	index int
	start int
}

func buildVersionDiff(fromVersion, toVersion int, before, after string) (*VersionDiff, error) {
	beforeNodes, err := parseDiffHTML(before)
	if err != nil {
		return nil, err
	}
	afterNodes, err := parseDiffHTML(after)
	if err != nil {
		return nil, err
	}
	matches, err := matchDiffNodes(beforeNodes, afterNodes)
	if err != nil {
		return nil, err
	}
	matchedAfter := make(map[int]int, len(matches))
	changes := make([]ElementChange, 0)
	for beforeIndex, afterIndex := range matches {
		matchedAfter[afterIndex] = beforeIndex
		beforeNode, afterNode := beforeNodes[beforeIndex], afterNodes[afterIndex]
		if isDiffWrapper(beforeNode.tag) || diffNodeSignature(beforeNode) == diffNodeSignature(afterNode) {
			continue
		}
		changes = append(changes, ElementChange{
			Kind: "modified", BeforeAID: beforeNode.aid, AfterAID: afterNode.aid,
			DOMPath: afterNode.path, BeforePath: beforeNode.path, AfterPath: afterNode.path,
			BeforeHTML: diffNodeSnippet(beforeNode), AfterHTML: diffNodeSnippet(afterNode),
		})
	}
	for index, node := range beforeNodes {
		if _, ok := matches[index]; !ok {
			if isDiffWrapper(node.tag) {
				continue
			}
			if node.parent >= 0 {
				if _, parentMatched := matches[node.parent]; !parentMatched {
					continue
				}
			}
			changes = append(changes, ElementChange{Kind: "removed", BeforeAID: node.aid, DOMPath: node.path, BeforePath: node.path, BeforeHTML: diffNodeSnippet(node)})
		}
	}
	for index, node := range afterNodes {
		if _, ok := matchedAfter[index]; !ok {
			if isDiffWrapper(node.tag) {
				continue
			}
			if node.parent >= 0 {
				if _, parentMatched := matchedAfter[node.parent]; !parentMatched {
					continue
				}
			}
			changes = append(changes, ElementChange{Kind: "added", AfterAID: node.aid, DOMPath: node.path, AfterPath: node.path, AfterHTML: diffNodeSnippet(node)})
		}
	}
	if len(changes) > maxDiffChanges {
		return nil, errDiffLimit
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].DOMPath == changes[j].DOMPath {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].DOMPath < changes[j].DOMPath
	})
	hunks, err := diffCodeHunks(before, after)
	if err != nil {
		return nil, err
	}
	result := &VersionDiff{From: fromVersion, To: toVersion, Changes: changes, CodeHunks: hunks}
	for _, change := range changes {
		switch change.Kind {
		case "added":
			result.Summary.Added++
		case "removed":
			result.Summary.Removed++
		case "modified":
			result.Summary.Modified++
		}
	}
	if diffOutputSize(result) > maxDiffOutputBytes {
		return nil, errDiffLimit
	}
	return result, nil
}

func parseDiffHTML(source string) ([]htmlDiffNode, error) {
	nodes := make([]htmlDiffNode, 0, 128)
	stack := make([]diffOpenNode, 0, 32)
	rootCounts := map[string]int{}
	childCounts := map[int]map[string]int{}
	rawScanBytes := 0
	pathBytes := 0
	for cursor := 0; cursor < len(source); {
		lt := strings.IndexByte(source[cursor:], '<')
		if lt < 0 {
			appendDiffText(nodes, stack, source[cursor:])
			break
		}
		lt += cursor
		appendDiffText(nodes, stack, source[cursor:lt])
		if strings.HasPrefix(source[lt:], "<!--") {
			end := strings.Index(source[lt+4:], "-->")
			if end < 0 {
				break
			}
			cursor = lt + 4 + end + 3
			continue
		}
		end := diffTagEnd(source, lt)
		if end < 0 {
			appendDiffText(nodes, stack, source[lt:])
			break
		}
		raw := source[lt+1 : end]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed[0] == '!' || trimmed[0] == '?' {
			cursor = end + 1
			continue
		}
		if trimmed[0] == '/' {
			tag, ok := diffTagName(trimmed[1:])
			if !ok {
				return nil, errDiffLimit
			}
			for pos := len(stack) - 1; pos >= 0; pos-- {
				entry := stack[pos]
				if nodes[entry.index].tag != tag {
					continue
				}
				for popped := len(stack) - 1; popped > pos; popped-- {
					open := stack[popped]
					nodes[open.index].outer = source[open.start:lt]
				}
				nodes[entry.index].outer = source[entry.start : end+1]
				stack = stack[:pos]
				break
			}
			cursor = end + 1
			continue
		}
		tag, ok := diffTagName(trimmed)
		if !ok {
			return nil, errDiffLimit
		}
		if tag == "" {
			cursor = end + 1
			continue
		}
		for len(stack) > 0 && impliedDiffEndTag(nodes[stack[len(stack)-1].index].tag, tag) {
			entry := stack[len(stack)-1]
			nodes[entry.index].outer = source[entry.start:lt]
			stack = stack[:len(stack)-1]
		}
		if len(nodes) >= maxDiffNodes || len(stack) >= maxDiffDepth {
			return nil, errDiffLimit
		}
		parent := -1
		counts := rootCounts
		if len(stack) > 0 {
			parent = stack[len(stack)-1].index
			counts = childCounts[parent]
			if counts == nil {
				counts = map[string]int{}
				childCounts[parent] = counts
			}
		}
		counts[tag]++
		segment := "/" + tag + fmt.Sprintf("[%d]", counts[tag])
		pathLen := len(segment)
		if parent >= 0 {
			pathLen += len(nodes[parent].path)
		}
		if pathLen > maxDiffPathBytes || pathBytes > maxDiffPathsBytes-pathLen {
			return nil, errDiffLimit
		}
		path := segment
		if parent >= 0 {
			path = nodes[parent].path + segment
		}
		pathBytes += pathLen
		attrs := parseDiffAttrs(trimmed[len(tag):])
		siblingPos := counts[tag] - 1
		if parent >= 0 {
			siblingPos = len(nodes[parent].children)
		}
		node := htmlDiffNode{tag: tag, aid: attrs["data-odoc-aid"], attrs: attrs, path: path, parent: parent, siblingPos: siblingPos, order: len(nodes)}
		nodes = append(nodes, node)
		index := len(nodes) - 1
		if parent >= 0 {
			nodes[parent].textBounds = append(nodes[parent].textBounds, len(nodes[parent].textParts))
			nodes[parent].children = append(nodes[parent].children, index)
			nodes[parent].childTags = append(nodes[parent].childTags, tag)
		}
		selfClosing := strings.HasSuffix(strings.TrimSpace(raw), "/") || isDiffVoidTag(tag)
		if selfClosing {
			nodes[index].outer = source[lt : end+1]
		} else if isDiffRawTextTag(tag) {
			closeStart, scanned := indexDiffRawClose(source, end+1, tag)
			rawScanBytes += scanned
			if rawScanBytes > maxDiffRawScanBytes {
				return nil, errDiffLimit
			}
			if closeStart < 0 {
				appendDiffNodeText(&nodes[index], source[end+1:])
				nodes[index].outer = source[lt:]
				cursor = len(source)
				continue
			}
			closeEnd := diffTagEnd(source, closeStart)
			if closeEnd < 0 {
				closeEnd = len(source) - 1
			}
			appendDiffNodeText(&nodes[index], source[end+1:closeStart])
			nodes[index].outer = source[lt : closeEnd+1]
			cursor = closeEnd + 1
			continue
		} else {
			stack = append(stack, diffOpenNode{index: index, start: lt})
		}
		cursor = end + 1
	}
	for _, entry := range stack {
		nodes[entry.index].outer = source[entry.start:]
	}
	for index := range nodes {
		literalRawText := isDiffLiteralRawTextTag(nodes[index].tag)
		fullText := ""
		textDigest := sha256.New()
		if literalRawText {
			fullText = strings.Join(nodes[index].textParts, "")
			writeDiffFrame(textDigest, fullText)
		} else {
			parts := make([]string, 0, len(nodes[index].textParts))
			for _, rawText := range nodes[index].textParts {
				parts = append(parts, html.UnescapeString(rawText))
			}
			bounds := append(append([]int(nil), nodes[index].textBounds...), len(parts))
			start := 0
			var fullTextBuilder strings.Builder
			for slot, end := range bounds {
				segment := strings.Join(parts[start:end], "")
				start = end
				fullTextBuilder.WriteString(segment)
				normalizedSegment := normalizeDiffTextSegment(segment)
				if normalizedSegment == "" {
					continue
				}
				// Pure ASCII-whitespace between block-like siblings (or against a
				// block edge) is reindent/pretty-print noise with no visible inline
				// boundary, so it must not perturb the digest. Whitespace touching an
				// inline (or unknown/custom, treated inline) neighbor stays framed so
				// "see <a>here</a>" keeps its visible space.
				if isOnlyHTMLASCIIWhitespace(segment) && !diffSlotWhitespaceVisible(nodes[index], slot) {
					continue
				}
				// Frame each non-empty segment with its boundary slot so text at
				// different child boundaries cannot collide, while a purely
				// structural child insertion (all-empty slots) leaves the digest
				// unchanged.
				writeDiffFrame(textDigest, strconv.Itoa(slot))
				writeDiffFrame(textDigest, normalizedSegment)
			}
			fullText = collapseHTMLASCIIWhitespace(fullTextBuilder.String())
		}
		nodes[index].textParts = nil
		nodes[index].textBounds = nil
		nodes[index].childTags = nil
		nodes[index].compareText = normalizeCompareText(fullText)
		nodes[index].textDigest = fmt.Sprintf("%x", textDigest.Sum(nil))
		nodes[index].text = fullText
		if len(nodes[index].text) > maxDiffCompareText {
			nodes[index].text = truncateUTF8(nodes[index].text, maxDiffCompareText)
		}
		nodes[index].signature = computeDiffNodeSignature(nodes[index])
	}
	return nodes, nil
}

func indexDiffRawClose(source string, start int, tag string) (int, int) {
	for cursor := start; cursor < len(source); {
		relative := strings.IndexByte(source[cursor:], '<')
		if relative < 0 {
			return -1, len(source) - start
		}
		candidate := cursor + relative
		nameStart := candidate + 2
		nameEnd := nameStart + len(tag)
		if candidate+1 < len(source) && source[candidate+1] == '/' && nameEnd <= len(source) && strings.EqualFold(source[nameStart:nameEnd], tag) && (nameEnd == len(source) || unicode.IsSpace(rune(source[nameEnd])) || source[nameEnd] == '>' || source[nameEnd] == '/') {
			return candidate, candidate - start + 1
		}
		cursor = candidate + 1
	}
	return -1, len(source) - start
}

func diffTagEnd(source string, start int) int {
	var quote byte
	for i := start + 1; i < len(source); i++ {
		c := source[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '>':
			return i
		}
	}
	return -1
}

func diffTagName(raw string) (string, bool) {
	raw = strings.TrimLeftFunc(raw, unicode.IsSpace)
	end := 0
	for end < len(raw) {
		c := raw[end]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		suffix := end > 0 && (c == '-' || c == ':' || c >= '0' && c <= '9')
		if !letter && !suffix {
			break
		}
		end++
		if end > maxDiffTagBytes {
			return "", false
		}
	}
	return strings.ToLower(raw[:end]), true
}

func parseDiffAttrs(raw string) map[string]string {
	attrs := map[string]string{}
	for cursor := 0; cursor < len(raw); {
		for cursor < len(raw) && (unicode.IsSpace(rune(raw[cursor])) || raw[cursor] == '/') {
			cursor++
		}
		start := cursor
		for cursor < len(raw) && !unicode.IsSpace(rune(raw[cursor])) && raw[cursor] != '=' && raw[cursor] != '/' {
			cursor++
		}
		if start == cursor {
			cursor++
			continue
		}
		name := strings.ToLower(raw[start:cursor])
		for cursor < len(raw) && unicode.IsSpace(rune(raw[cursor])) {
			cursor++
		}
		value := ""
		if cursor < len(raw) && raw[cursor] == '=' {
			cursor++
			for cursor < len(raw) && unicode.IsSpace(rune(raw[cursor])) {
				cursor++
			}
			if cursor < len(raw) && (raw[cursor] == '\'' || raw[cursor] == '"') {
				quote := raw[cursor]
				cursor++
				start = cursor
				for cursor < len(raw) && raw[cursor] != quote {
					cursor++
				}
				value = raw[start:cursor]
				if cursor < len(raw) {
					cursor++
				}
			} else {
				start = cursor
				for cursor < len(raw) && !unicode.IsSpace(rune(raw[cursor])) && raw[cursor] != '/' {
					cursor++
				}
				value = raw[start:cursor]
			}
		}
		if _, exists := attrs[name]; !exists {
			attrs[name] = html.UnescapeString(value)
		}
	}
	return attrs
}

func appendDiffText(nodes []htmlDiffNode, stack []diffOpenNode, text string) {
	if len(stack) == 0 {
		return
	}
	index := stack[len(stack)-1].index
	appendDiffNodeText(&nodes[index], text)
}

func appendDiffNodeText(node *htmlDiffNode, text string) {
	if text == "" {
		return
	}
	// Keep chunks separate so entity decoding cannot cross comments or tags.
	// Finalization joins once, avoiding quadratic repeated concatenation.
	node.textParts = append(node.textParts, text)
}

// isBlockLevelDiffTag reports whether tag renders as a block-like box whose
// surrounding ASCII whitespace collapses away visually (block, table, list,
// section, form, headings). Unknown/custom elements are conservatively treated
// as inline (returns false) so their surrounding whitespace stays significant.
func isBlockLevelDiffTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "details", "dialog", "dd", "div",
		"dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2",
		"h3", "h4", "h5", "h6", "header", "hgroup", "hr", "li", "main", "menu", "nav",
		"ol", "p", "pre", "search", "section", "summary", "ul",
		"table", "caption", "colgroup", "thead", "tbody", "tfoot", "tr", "td", "th",
		"optgroup", "option":
		return true
	default:
		return false
	}
}

// diffSlotWhitespaceVisible reports whether a pure-whitespace text segment at
// boundary slot renders as a visible inline space. Slot s sits between child
// s-1 (left) and child s (right); a missing side is the parent's own edge,
// which is a block context. Whitespace is invisible reindent/pretty-print noise
// only when both neighbors are block-level, so it must not enter the digest.
// Any inline (or unknown/custom, treated inline) or void neighbor keeps it.
func diffSlotWhitespaceVisible(node htmlDiffNode, slot int) bool {
	return !diffBoundarySideIsBlock(node, slot-1) || !diffBoundarySideIsBlock(node, slot)
}

// diffBoundarySideIsBlock reports whether the child at childPos is block-level.
// An out-of-range position denotes the parent's own edge, which is block-like.
func diffBoundarySideIsBlock(node htmlDiffNode, childPos int) bool {
	if childPos < 0 || childPos >= len(node.children) {
		return true
	}
	return isBlockLevelDiffTag(node.childTags[childPos])
}

func normalizeDiffTextSegment(value string) string {
	if value == "" {
		return ""
	}
	// HTML only folds ASCII whitespace; a leading/trailing ASCII-whitespace
	// boundary is preserved as a single space so adjacent segments do not merge.
	leading := isHTMLASCIIWhitespace(value[0])
	trailing := isHTMLASCIIWhitespace(value[len(value)-1])
	value = collapseHTMLASCIIWhitespace(value)
	if leading {
		value = " " + value
	}
	if trailing {
		value += " "
	}
	return value
}

// isHTMLASCIIWhitespace reports whether b is one of the HTML collapsible ASCII
// whitespace bytes (tab, LF, FF, CR, space). Visible Unicode whitespace such as
// NBSP (U+00A0) is intentionally excluded.
func isHTMLASCIIWhitespace(b byte) bool {
	switch b {
	case '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

// collapseHTMLASCIIWhitespace folds runs of HTML ASCII whitespace to a single
// space and trims leading/trailing runs. It is linear and UTF-8 safe: multibyte
// runes (whose continuation bytes never match an ASCII whitespace byte) pass
// through untouched, so a literal U+00A0 never equals a plain space.
func collapseHTMLASCIIWhitespace(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	pendingSpace := false
	for i := 0; i < len(value); i++ {
		if isHTMLASCIIWhitespace(value[i]) {
			pendingSpace = builder.Len() > 0
			continue
		}
		if pendingSpace {
			builder.WriteByte(' ')
			pendingSpace = false
		}
		builder.WriteByte(value[i])
	}
	return builder.String()
}

func writeDiffFrame(builder interface{ Write([]byte) (int, error) }, value string) {
	length := strconv.AppendInt(nil, int64(len(value)), 10)
	_, _ = builder.Write(length)
	_, _ = builder.Write([]byte{':'})
	_, _ = builder.Write([]byte(value))
}

func impliedDiffEndTag(open, next string) bool {
	switch open {
	case "p":
		switch next {
		case "address", "article", "aside", "blockquote", "div", "dl", "fieldset", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr", "main", "menu", "nav", "ol", "p", "pre", "search", "section", "table", "ul":
			return true
		}
	case "li":
		return next == "li"
	case "dt", "dd":
		return next == "dt" || next == "dd"
	case "option":
		return next == "option" || next == "optgroup"
	case "thead":
		return next == "tbody" || next == "tfoot"
	case "tbody":
		return next == "tbody" || next == "tfoot"
	case "tfoot":
		return next == "tbody"
	case "tr":
		return next == "tr" || next == "thead" || next == "tbody" || next == "tfoot"
	case "td", "th":
		return next == "td" || next == "th" || next == "tr" || next == "thead" || next == "tbody" || next == "tfoot"
	}
	return false
}

func isDiffVoidTag(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func isDiffRawTextTag(tag string) bool {
	switch tag {
	case "script", "style", "textarea", "title":
		return true
	default:
		return false
	}
}

func isDiffLiteralRawTextTag(tag string) bool {
	return tag == "script" || tag == "style"
}

func isDiffWrapper(tag string) bool {
	return tag == "html" || tag == "head" || tag == "body"
}

func matchDiffNodes(before, after []htmlDiffNode) (map[int]int, error) {
	matches := map[int]int{}
	used := map[int]bool{}
	budget := diffMatchBudget{}
	matchRootNodes(before, after, matches, used)
	for changed := true; changed; {
		changed = false
		for _, pair := range sortedDiffMatches(matches) {
			beforeParent, afterParent := pair[0], pair[1]
			if matchAIDChildren(before, after, beforeParent, afterParent, matches, used) {
				changed = true
			}
			exactChanged, err := matchExactChildSequence(before, after, beforeParent, afterParent, matches, used, &budget)
			if err != nil {
				return nil, err
			}
			if exactChanged {
				changed = true
			}
		}
	}
	for beforeIndex, beforeNode := range before {
		if _, ok := matches[beforeIndex]; ok {
			continue
		}
		for afterIndex, afterNode := range after {
			if used[afterIndex] || beforeNode.path != afterNode.path || beforeNode.tag != afterNode.tag || !parentsMatch(beforeNode, afterNode, matches) {
				continue
			}
			matches[beforeIndex] = afterIndex
			used[afterIndex] = true
			break
		}
	}
	for beforeIndex, beforeNode := range before {
		if _, ok := matches[beforeIndex]; ok {
			continue
		}
		bestIndex, bestScore := -1, 0.0
		if !budget.addComparisons(len(before) + len(after)) {
			return nil, errDiffLimit
		}
		bounds := newSiblingOrderBounds(before, after, matches)
		for afterIndex, afterNode := range after {
			if used[afterIndex] || beforeNode.tag != afterNode.tag || !parentsMatch(beforeNode, afterNode, matches) {
				continue
			}
			compatible := bounds.compatible(before, after, beforeIndex, afterIndex)
			if !compatible {
				continue
			}
			if !budget.add(len(beforeNode.compareText) + len(afterNode.compareText)) {
				return nil, errDiffLimit
			}
			score := diffNodeSimilarity(beforeNode, afterNode)
			if score > bestScore {
				bestIndex, bestScore = afterIndex, score
			}
		}
		if bestIndex >= 0 && bestScore >= 0.55 {
			matches[beforeIndex] = bestIndex
			used[bestIndex] = true
		}
	}
	return matches, nil
}

func sortedDiffMatches(matches map[int]int) [][2]int {
	pairs := make([][2]int, 0, len(matches))
	for beforeIndex, afterIndex := range matches {
		pairs = append(pairs, [2]int{beforeIndex, afterIndex})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] == pairs[j][0] {
			return pairs[i][1] < pairs[j][1]
		}
		return pairs[i][0] < pairs[j][0]
	})
	return pairs
}

func matchAIDChildren(before, after []htmlDiffNode, beforeParent, afterParent int, matches map[int]int, used map[int]bool) bool {
	afterByAID := map[string][]int{}
	for _, afterIndex := range after[afterParent].children {
		if !used[afterIndex] && after[afterIndex].aid != "" {
			afterByAID[after[afterIndex].aid] = append(afterByAID[after[afterIndex].aid], afterIndex)
		}
	}
	changed := false
	for _, beforeIndex := range before[beforeParent].children {
		if _, ok := matches[beforeIndex]; ok || before[beforeIndex].aid == "" {
			continue
		}
		candidates := afterByAID[before[beforeIndex].aid]
		if len(candidates) != 1 || used[candidates[0]] || before[beforeIndex].tag != after[candidates[0]].tag {
			continue
		}
		matches[beforeIndex] = candidates[0]
		used[candidates[0]] = true
		changed = true
	}
	return changed
}

type diffMatchBudget struct {
	comparisons int
	bytes       int
}

func (budget *diffMatchBudget) add(bytes int) bool {
	return budget.addComparisonsAndBytes(1, bytes)
}

func (budget *diffMatchBudget) addComparisons(comparisons int) bool {
	return budget.addComparisonsAndBytes(comparisons, 0)
}

func (budget *diffMatchBudget) addComparisonsAndBytes(comparisons, bytes int) bool {
	budget.comparisons += comparisons
	budget.bytes += bytes
	return budget.comparisons <= maxDiffComparisons && budget.bytes <= maxDiffCompareBytes
}

func matchRootNodes(before, after []htmlDiffNode, matches map[int]int, used map[int]bool) {
	for beforeIndex, beforeNode := range before {
		if beforeNode.parent >= 0 {
			continue
		}
		for afterIndex, afterNode := range after {
			if !used[afterIndex] && afterNode.parent < 0 && beforeNode.tag == afterNode.tag {
				matches[beforeIndex] = afterIndex
				used[afterIndex] = true
				break
			}
		}
	}
}

func matchExactChildSequence(before, after []htmlDiffNode, beforeParent, afterParent int, matches map[int]int, used map[int]bool, budget *diffMatchBudget) (bool, error) {
	beforeChildren := unmatchedDiffChildren(before[beforeParent].children, matches, nil)
	afterChildren := unmatchedDiffChildren(after[afterParent].children, nil, used)
	if len(beforeChildren) == 0 || len(afterChildren) == 0 {
		return false, nil
	}
	changed := false
	if !budget.addComparisons(len(before) + len(after)) {
		return false, errDiffLimit
	}
	bounds := newSiblingOrderBounds(before, after, matches)
	beforeStart, afterStart := 0, 0
	for beforeStart < len(beforeChildren) && afterStart < len(afterChildren) {
		equal, err := diffSignaturesEqual(before[beforeChildren[beforeStart]], after[afterChildren[afterStart]], budget)
		if err != nil {
			return false, err
		}
		compatible := bounds.compatible(before, after, beforeChildren[beforeStart], afterChildren[afterStart])
		if !equal || !compatible {
			break
		}
		matches[beforeChildren[beforeStart]] = afterChildren[afterStart]
		used[afterChildren[afterStart]] = true
		changed = true
		beforeStart++
		afterStart++
	}
	beforeEnd, afterEnd := len(beforeChildren), len(afterChildren)
	for beforeEnd > beforeStart && afterEnd > afterStart {
		beforeIndex, afterIndex := beforeChildren[beforeEnd-1], afterChildren[afterEnd-1]
		equal, err := diffSignaturesEqual(before[beforeIndex], after[afterIndex], budget)
		if err != nil {
			return false, err
		}
		compatible := bounds.compatible(before, after, beforeIndex, afterIndex)
		if !equal || !compatible {
			break
		}
		beforeEnd--
		afterEnd--
		matches[beforeIndex] = afterIndex
		used[afterIndex] = true
		changed = true
	}
	beforeChildren = beforeChildren[beforeStart:beforeEnd]
	afterChildren = afterChildren[afterStart:afterEnd]
	if len(beforeChildren) == 0 || len(afterChildren) == 0 {
		return changed, nil
	}
	if len(beforeChildren) > (maxDiffComparisons-budget.comparisons)/len(afterChildren) {
		return false, errDiffLimit
	}
	width := len(afterChildren) + 1
	cells := make([]uint16, (len(beforeChildren)+1)*width)
	if !budget.addComparisons(len(before) + len(after)) {
		return false, errDiffLimit
	}
	bounds = newSiblingOrderBounds(before, after, matches)
	for beforePos := len(beforeChildren) - 1; beforePos >= 0; beforePos-- {
		for afterPos := len(afterChildren) - 1; afterPos >= 0; afterPos-- {
			beforeIndex, afterIndex := beforeChildren[beforePos], afterChildren[afterPos]
			equal, err := diffSignaturesEqual(before[beforeIndex], after[afterIndex], budget)
			if err != nil {
				return false, err
			}
			cell := beforePos*width + afterPos
			compatible := bounds.compatible(before, after, beforeIndex, afterIndex)
			if equal && compatible {
				cells[cell] = cells[(beforePos+1)*width+afterPos+1] + 1
			} else {
				skipBefore := cells[(beforePos+1)*width+afterPos]
				skipAfter := cells[beforePos*width+afterPos+1]
				if skipBefore >= skipAfter {
					cells[cell] = skipBefore
				} else {
					cells[cell] = skipAfter
				}
			}
		}
	}
	for beforePos, afterPos := 0, 0; beforePos < len(beforeChildren) && afterPos < len(afterChildren); {
		beforeIndex, afterIndex := beforeChildren[beforePos], afterChildren[afterPos]
		compatible := bounds.compatible(before, after, beforeIndex, afterIndex)
		if diffNodeSignature(before[beforeIndex]) == diffNodeSignature(after[afterIndex]) && compatible && cells[beforePos*width+afterPos] == cells[(beforePos+1)*width+afterPos+1]+1 {
			matches[beforeIndex] = afterIndex
			used[afterIndex] = true
			changed = true
			beforePos++
			afterPos++
		} else if cells[(beforePos+1)*width+afterPos] >= cells[beforePos*width+afterPos+1] {
			beforePos++
		} else {
			afterPos++
		}
	}
	return changed, nil
}

func diffSignaturesEqual(before, after htmlDiffNode, budget *diffMatchBudget) (bool, error) {
	beforeSignature, afterSignature := diffNodeSignature(before), diffNodeSignature(after)
	if !budget.add(0) {
		return false, errDiffLimit
	}
	return beforeSignature == afterSignature, nil
}

func unmatchedDiffChildren(children []int, matches map[int]int, used map[int]bool) []int {
	result := make([]int, 0, len(children))
	for _, index := range children {
		if matches != nil {
			if _, ok := matches[index]; ok {
				continue
			}
		}
		if used != nil && used[index] {
			continue
		}
		result = append(result, index)
	}
	return result
}

func parentsMatch(before, after htmlDiffNode, matches map[int]int) bool {
	if before.parent < 0 || after.parent < 0 {
		return before.parent == after.parent
	}
	matchedParent, ok := matches[before.parent]
	return ok && matchedParent == after.parent
}

type siblingOrderBounds struct {
	lower []int
	upper []int
}

func newSiblingOrderBounds(before, after []htmlDiffNode, matches map[int]int) siblingOrderBounds {
	bounds := siblingOrderBounds{lower: make([]int, len(before)), upper: make([]int, len(before))}
	for index := range bounds.lower {
		bounds.lower[index] = -1
		bounds.upper[index] = len(after)
	}
	for parentIndex := range before {
		children := before[parentIndex].children
		anchor := -1
		for _, child := range children {
			bounds.lower[child] = anchor
			if matched, ok := matches[child]; ok {
				anchor = matched
			}
		}
		anchor = -1
		for pos := len(children) - 1; pos >= 0; pos-- {
			child := children[pos]
			bounds.upper[child] = anchor
			if matched, ok := matches[child]; ok {
				anchor = matched
			}
		}
	}
	return bounds
}

func (bounds siblingOrderBounds) compatible(before, after []htmlDiffNode, beforeIndex, afterIndex int) bool {
	beforeNode, afterNode := before[beforeIndex], after[afterIndex]
	if beforeNode.parent < 0 || afterNode.parent < 0 {
		return true
	}
	if anchor := bounds.lower[beforeIndex]; anchor >= 0 && after[anchor].parent == afterNode.parent && after[anchor].siblingPos >= afterNode.siblingPos {
		return false
	}
	if anchor := bounds.upper[beforeIndex]; anchor >= 0 && after[anchor].parent == afterNode.parent && after[anchor].siblingPos <= afterNode.siblingPos {
		return false
	}
	return true
}

func diffNodeSimilarity(before, after htmlDiffNode) float64 {
	score := 0.25
	if before.parent >= 0 && after.parent >= 0 {
		beforeParent := before.path[:strings.LastIndex(before.path, "/")]
		afterParent := after.path[:strings.LastIndex(after.path, "/")]
		if beforeParent == afterParent {
			score += 0.25
		}
	}
	if before.order == after.order {
		score += 0.1
	}
	score += 0.25 * stringSimilarity(before.compareText, after.compareText)
	score += 0.15 * attrSimilarity(before.attrs, after.attrs)
	return score
}

func stringSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	shorter, longer := a, b
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if strings.Contains(longer, shorter) {
		return float64(len(shorter)) / float64(len(longer))
	}
	common := 0
	seen := map[string]bool{}
	for _, word := range strings.Fields(shorter) {
		seen[word] = true
	}
	for _, word := range strings.Fields(longer) {
		if seen[word] {
			common++
		}
	}
	return float64(2*common) / float64(len(strings.Fields(a))+len(strings.Fields(b)))
}

func attrSimilarity(a, b map[string]string) float64 {
	union, same := 0, 0
	seen := map[string]bool{}
	for key, value := range a {
		if key == "data-odoc-aid" {
			continue
		}
		seen[key] = true
		union++
		if b[key] == value {
			same++
		}
	}
	for key := range b {
		if key != "data-odoc-aid" && !seen[key] {
			union++
		}
	}
	if union == 0 {
		return 1
	}
	return float64(same) / float64(union)
}

func diffNodeSignature(node htmlDiffNode) string {
	if node.signature != "" {
		return node.signature
	}
	return computeDiffNodeSignature(node)
}

func computeDiffNodeSignature(node htmlDiffNode) string {
	keys := make([]string, 0, len(node.attrs))
	for key := range node.attrs {
		if key != "data-odoc-aid" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var builder strings.Builder
	writeDiffFrame(&builder, node.tag)
	for _, key := range keys {
		writeDiffFrame(&builder, key)
		writeDiffFrame(&builder, node.attrs[key])
	}
	writeDiffFrame(&builder, node.compareText)
	writeDiffFrame(&builder, node.textDigest)
	return builder.String()
}

func normalizeCompareText(value string) string {
	if len(value) > maxDiffCompareText {
		value = truncateUTF8(value, maxDiffCompareText)
	}
	return strings.ToLower(collapseHTMLASCIIWhitespace(value))
}

func diffNodeSnippet(node htmlDiffNode) string {
	if len(node.outer) <= maxDiffSnippetBytes {
		return node.outer
	}
	openingEnd := strings.IndexByte(node.outer, '>')
	opening := "<" + node.tag + ">"
	if openingEnd >= 0 && openingEnd < maxDiffOpeningBytes {
		opening = node.outer[:openingEnd+1]
	}
	if isDiffVoidTag(node.tag) {
		return opening
	}
	return opening + "<!-- omitted " + strconv.Itoa(len(node.outer)) + " bytes -->" + "</" + node.tag + ">"
}

func normalizeDiffHTML(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	var quote byte
	pendingSpace := false
	for index := 0; index < len(value); index++ {
		byteValue := value[index]
		if quote != 0 {
			builder.WriteByte(byteValue)
			if byteValue == quote {
				quote = 0
			}
			continue
		}
		if byteValue == '\'' || byteValue == '"' {
			if pendingSpace && builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			pendingSpace = false
			quote = byteValue
			builder.WriteByte(byteValue)
			continue
		}
		if isHTMLASCIIWhitespace(byteValue) {
			pendingSpace = builder.Len() > 0
			continue
		}
		if pendingSpace {
			builder.WriteByte(' ')
			pendingSpace = false
		}
		builder.WriteByte(byteValue)
	}
	return builder.String()
}

func diffCodeHunks(before, after string) ([]CodeHunk, error) {
	oldLines, ok := normalizedHTMLLines(before)
	if !ok {
		return nil, errDiffLimit
	}
	newLines, ok := normalizedHTMLLines(after)
	if !ok {
		return nil, errDiffLimit
	}
	ops, ok := diffLineOps(oldLines, newLines)
	if !ok {
		return nil, errDiffLimit
	}
	changeIndexes := make([]int, 0)
	for index, op := range ops {
		if op.kind != ' ' {
			changeIndexes = append(changeIndexes, index)
		}
	}
	if len(changeIndexes) == 0 {
		return []CodeHunk{}, nil
	}
	type hunkRange struct{ start, end int }
	ranges := make([]hunkRange, 0, len(changeIndexes))
	for _, index := range changeIndexes {
		start, end := index-diffContextLines, index+diffContextLines+1
		if start < 0 {
			start = 0
		}
		if end > len(ops) {
			end = len(ops)
		}
		if len(ranges) > 0 && start <= ranges[len(ranges)-1].end {
			ranges[len(ranges)-1].end = end
		} else {
			ranges = append(ranges, hunkRange{start: start, end: end})
		}
	}
	hunks := make([]CodeHunk, 0, len(ranges))
	totalLines := 0
	for _, interval := range ranges {
		totalLines += interval.end - interval.start
	}
	if totalLines > maxDiffHunkLines {
		return nil, errDiffLimit
	}
	for _, interval := range ranges {
		oldStart, newStart := 1, 1
		if interval.start > 0 {
			oldStart = ops[interval.start].oldLine
			newStart = ops[interval.start].newLine
			if oldStart == 0 {
				oldStart = ops[interval.start-1].oldLine + 1
			}
			if newStart == 0 {
				newStart = ops[interval.start-1].newLine + 1
			}
		}
		hunk := CodeHunk{OldStart: oldStart, NewStart: newStart}
		for _, op := range ops[interval.start:interval.end] {
			hunk.Lines = append(hunk.Lines, string(op.kind)+limitDiffLine(op.text))
			if op.kind != '+' {
				hunk.OldLines++
			}
			if op.kind != '-' {
				hunk.NewLines++
			}
		}
		hunks = append(hunks, hunk)
	}
	return hunks, nil
}

type diffLineOp struct {
	kind             byte
	text             string
	oldLine, newLine int
}

func diffLineOps(oldLines, newLines []string) ([]diffLineOp, bool) {
	const syncWindow = 64
	const maxLineComparisons = maxDiffInputLines * 128
	ops := make([]diffLineOp, 0, len(oldLines)+len(newLines))
	oldIndex, newIndex, comparisons := 0, 0, 0
	for oldIndex < len(oldLines) || newIndex < len(newLines) {
		if oldIndex < len(oldLines) && newIndex < len(newLines) && oldLines[oldIndex] == newLines[newIndex] {
			ops = append(ops, diffLineOp{kind: ' ', text: oldLines[oldIndex], oldLine: oldIndex + 1, newLine: newIndex + 1})
			oldIndex++
			newIndex++
			continue
		}
		bestOld, bestNew := -1, -1
		oldLimit, newLimit := oldIndex+syncWindow, newIndex+syncWindow
		if oldLimit > len(oldLines) {
			oldLimit = len(oldLines)
		}
		if newLimit > len(newLines) {
			newLimit = len(newLines)
		}
		for distance := 1; distance < syncWindow*2 && bestOld < 0; distance++ {
			for oldOffset := 0; oldOffset <= distance; oldOffset++ {
				newOffset := distance - oldOffset
				candidateOld, candidateNew := oldIndex+oldOffset, newIndex+newOffset
				if candidateOld >= oldLimit || candidateNew >= newLimit {
					continue
				}
				comparisons++
				if comparisons > maxLineComparisons {
					return nil, false
				}
				if oldLines[candidateOld] == newLines[candidateNew] {
					bestOld, bestNew = candidateOld, candidateNew
					break
				}
			}
		}
		if bestOld < 0 {
			bestOld, bestNew = oldLimit, newLimit
		}
		for oldIndex < bestOld {
			ops = append(ops, diffLineOp{kind: '-', text: oldLines[oldIndex], oldLine: oldIndex + 1, newLine: newIndex + 1})
			oldIndex++
		}
		for newIndex < bestNew {
			ops = append(ops, diffLineOp{kind: '+', text: newLines[newIndex], oldLine: oldIndex + 1, newLine: newIndex + 1})
			newIndex++
		}
	}
	return ops, true
}

func normalizedHTMLLines(source string) ([]string, bool) {
	lines := make([]string, 0, 256)
	// Whitespace between tags is treated as ignorable reindent noise only when we
	// can prove nothing in the document can make it significant. layoutCSS is a
	// bounded, conservative document-level signal (a <style> block, a stylesheet
	// link, or an inline style touching display/white-space) that some rule could
	// change layout so inter-tag whitespace may be visible. preStack tracks a
	// per-node preformatted / white-space:pre* ancestry so text nested inside such
	// contexts is preserved verbatim. When neither holds, whitespace-only runs
	// collapse away, keeping ordinary block reindent a zero-noise no-op.
	layoutCSS := hasLayoutAffectingCSS(source)
	preStack := make([]string, 0, 16)
	whitespaceSensitive := func() bool { return layoutCSS || len(preStack) > 0 }
	preformatted := func() bool { return len(preStack) > 0 }
	for cursor := 0; cursor < len(source); {
		if len(lines) >= maxDiffInputLines {
			return nil, false
		}
		if source[cursor] == '<' {
			end := diffTagEnd(source, cursor)
			if end < 0 {
				// Unclosed '<' tail (no closing '>'): treat the remainder as plain
				// text and terminate; do not slice an invalid tag range.
				if !appendNormalizedDiffText(&lines, source[cursor:], false, whitespaceSensitive(), preformatted()) {
					return nil, false
				}
				break
			}
			rawTag := source[cursor+1 : end]
			trimmed := strings.TrimSpace(rawTag)
			lines = append(lines, limitDiffLine(normalizeDiffHTML(source[cursor:end+1])))
			cursor = end + 1
			if trimmed == "" || trimmed[0] == '!' || trimmed[0] == '?' {
				continue
			}
			if trimmed[0] == '/' {
				if closeTag, ok := diffTagName(trimmed[1:]); ok {
					popPreformatted(&preStack, closeTag)
				}
				continue
			}
			selfClosing := strings.HasSuffix(strings.TrimSpace(rawTag), "/")
			tag, ok := diffTagName(trimmed)
			if !ok {
				return nil, false
			}
			if !selfClosing && !isDiffVoidTag(tag) && isPreformattedContext(tag, trimmed[len(tag):]) {
				preStack = append(preStack, tag)
			}
			if !isDiffRawTextTag(tag) {
				continue
			}
			// Raw-text elements hold text, not markup: their content must be scanned
			// to the matching close tag so an inner '<' is never split as a spurious
			// tag (the double-blind that hid textarea/title content changes). script
			// and style are literal (no entity decode); textarea and title are RCDATA
			// (entities decode). textarea preserves whitespace (it is on preStack);
			// title/script/style collapse it, matching how each renders.
			literalRaw := isDiffLiteralRawTextTag(tag)
			rawPreformatted := preformatted()
			closeStart, _ := indexDiffRawClose(source, cursor, tag)
			if closeStart < 0 {
				if !appendNormalizedDiffText(&lines, source[cursor:], literalRaw, rawPreformatted, rawPreformatted) {
					return nil, false
				}
				cursor = len(source)
				continue
			}
			if !appendNormalizedDiffText(&lines, source[cursor:closeStart], literalRaw, rawPreformatted, rawPreformatted) || len(lines) >= maxDiffInputLines {
				return nil, false
			}
			closeEnd := diffTagEnd(source, closeStart)
			if closeEnd < 0 {
				closeEnd = len(source) - 1
			}
			lines = append(lines, limitDiffLine(normalizeDiffHTML(source[closeStart:closeEnd+1])))
			cursor = closeEnd + 1
			if rawPreformatted {
				popPreformatted(&preStack, tag)
			}
			continue
		}
		end := strings.IndexByte(source[cursor:], '<')
		if end < 0 {
			end = len(source) - cursor
		}
		if !appendNormalizedDiffText(&lines, source[cursor:cursor+end], false, whitespaceSensitive(), preformatted()) {
			return nil, false
		}
		cursor += end
	}
	return lines, true
}

func appendNormalizedDiffText(lines *[]string, text string, literal, whitespaceSensitive, preformatted bool) bool {
	raw := text
	if !literal {
		text = html.UnescapeString(text)
	}
	if preformatted {
		// Inside a preformatted / white-space:pre* context every space, tab, and
		// newline is significant, so preserve the run verbatim (bounded) instead of
		// collapsing; an empty run still emits nothing.
		if text == "" {
			return true
		}
		if len(*lines) >= maxDiffInputLines {
			return false
		}
		*lines = append(*lines, limitDiffLine(text))
		return true
	}
	collapsed := collapseHTMLASCIIWhitespace(text)
	if collapsed == "" {
		// Pure whitespace. In a whitespace-sensitive context the exact run can be
		// visible, so emit a distinct token that differs when the run differs and
		// vanishes when the run is absent; elsewhere it is safe reindent noise.
		if !whitespaceSensitive || text == "" {
			return true
		}
		collapsed = whitespaceRunToken(raw)
		if len(*lines) >= maxDiffInputLines {
			return false
		}
		// whitespaceRunToken is already bounded and collision-free; do not re-bound.
		*lines = append(*lines, collapsed)
		return true
	}
	if len(*lines) >= maxDiffInputLines {
		return false
	}
	*lines = append(*lines, limitDiffLine(collapsed))
	return true
}

// whitespaceRunToken encodes a preserved pure-whitespace run so that runs which
// differ byte-for-byte produce different lines (e.g. under a <pre> ancestor)
// while an absent run emits no line at all. The token is self-bounded: a short
// quoted prefix for readability plus run length and the full-run SHA-256, so
// runs differing only in length or tail beyond the prefix never collide.
func whitespaceRunToken(run string) string {
	const prefixBytes = 64
	prefix := run
	if len(prefix) > prefixBytes {
		prefix = prefix[:prefixBytes]
	}
	digest := sha256.Sum256([]byte(run))
	return fmt.Sprintf("\u2408ws:%s#%d#%x", strconv.Quote(prefix), len(run), digest)
}

// isPreformattedContext reports whether an open tag establishes a
// whitespace-preserving context for its descendants: the native pre/textarea
// elements, or any element whose inline style sets white-space to a preserving
// value. attrRaw is the tag text after the name.
func isPreformattedContext(tag, attrRaw string) bool {
	if tag == "pre" || tag == "textarea" {
		return true
	}
	style := parseDiffAttrs(attrRaw)["style"]
	if style == "" {
		return false
	}
	return styleHasPreservedWhitespace(style)
}

func styleHasPreservedWhitespace(style string) bool {
	style = strings.ToLower(style)
	idx := strings.Index(style, "white-space")
	for idx >= 0 {
		value := style[idx+len("white-space"):]
		if colon := strings.IndexByte(value, ':'); colon >= 0 {
			value = value[colon+1:]
			if semi := strings.IndexByte(value, ';'); semi >= 0 {
				value = value[:semi]
			}
			if strings.Contains(value, "pre") || strings.Contains(value, "break-spaces") {
				return true
			}
		}
		next := strings.Index(style[idx+len("white-space"):], "white-space")
		if next < 0 {
			break
		}
		idx += len("white-space") + next
	}
	return false
}

func popPreformatted(preStack *[]string, closeTag string) {
	for pos := len(*preStack) - 1; pos >= 0; pos-- {
		if (*preStack)[pos] == closeTag {
			*preStack = (*preStack)[:pos]
			return
		}
	}
}

// hasLayoutAffectingCSS is a bounded, conservative scan for anything that could
// make inter-tag whitespace visible: a <style> element, a stylesheet <link>, or
// an inline style declaring display/white-space. It never parses CSS; it only
// decides whether whitespace changes must stay provable rather than assumed
// safe. False positives merely keep whitespace noise in the source hunk; a
// false negative would risk an empty diff, so the checks stay generous.
func hasLayoutAffectingCSS(source string) bool {
	scan := source
	if len(scan) > maxDiffRawScanBytes {
		scan = scan[:maxDiffRawScanBytes]
	}
	lower := strings.ToLower(scan)
	if strings.Contains(lower, "<style") {
		return true
	}
	// A rel="stylesheet" link (or a rel token containing it) can restyle layout.
	if strings.Contains(lower, "stylesheet") {
		return true
	}
	for idx := strings.Index(lower, "style="); idx >= 0; {
		tail := lower[idx+len("style="):]
		// Scope the display/white-space probe to this style attribute's own value so
		// a later unrelated "display" elsewhere in the document is not a false
		// positive. A quoted value ends at its matching quote; an unquoted one ends
		// at whitespace or the tag's '>'.
		value := tail
		if len(tail) > 0 && (tail[0] == '"' || tail[0] == '\'') {
			if end := strings.IndexByte(tail[1:], tail[0]); end >= 0 {
				value = tail[1 : 1+end]
			}
		} else if end := strings.IndexAny(tail, " \t\r\n>"); end >= 0 {
			value = tail[:end]
		}
		if strings.Contains(value, "display") || strings.Contains(value, "white-space") {
			return true
		}
		next := strings.Index(tail, "style=")
		if next < 0 {
			break
		}
		idx += len("style=") + next
	}
	return false
}

func isOnlyHTMLASCIIWhitespace(value string) bool {
	for index := 0; index < len(value); index++ {
		if !isHTMLASCIIWhitespace(value[index]) {
			return false
		}
	}
	return true
}

func limitDiffLine(line string) string {
	const maxLine = 1024
	if len(line) <= maxLine {
		return line
	}
	// A truncated prefix alone would let two long lines that share their first
	// maxLine bytes but differ later collide, silently dropping the change from
	// the line diff. Append the full-content SHA-256 so distinct complete text
	// never compares equal, while the string stays bounded. The digest is part of
	// the line identity used for equality, so equal content still matches.
	digest := sha256.Sum256([]byte(line))
	return truncateUTF8(line, maxLine) + fmt.Sprintf("\u2026\u2408sha256:%x", digest)
}

func diffOutputSize(result *VersionDiff) int {
	encoded, err := json.Marshal(result)
	if err != nil {
		return maxDiffOutputBytes + 1
	}
	return len(encoded)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit]
}
