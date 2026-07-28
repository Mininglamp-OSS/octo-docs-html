package service

import (
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode"
)

var errDiffLimit = errors.New("diff complexity limit exceeded")

const (
	maxDiffNodes       = 4000
	maxDiffComparisons = 200000
	maxDiffChanges     = 1000
	maxDiffInputLines  = 12000
	maxDiffHunkLines   = 2000
	maxDiffOutputBytes = 512 << 10
	diffContextLines   = 3
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
	tag      string
	aid      string
	attrs    map[string]string
	text     string
	path     string
	outer    string
	parent   int
	children []int
	order    int
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
			BeforeHTML: beforeNode.outer, AfterHTML: afterNode.outer,
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
			changes = append(changes, ElementChange{Kind: "removed", BeforeAID: node.aid, DOMPath: node.path, BeforePath: node.path, BeforeHTML: node.outer})
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
			changes = append(changes, ElementChange{Kind: "added", AfterAID: node.aid, DOMPath: node.path, AfterPath: node.path, AfterHTML: node.outer})
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
			tag := diffTagName(trimmed[1:])
			for pos := len(stack) - 1; pos >= 0; pos-- {
				entry := stack[pos]
				if nodes[entry.index].tag != tag {
					continue
				}
				nodes[entry.index].outer = source[entry.start : end+1]
				stack = stack[:pos]
				break
			}
			cursor = end + 1
			continue
		}
		tag := diffTagName(trimmed)
		if tag == "" {
			cursor = end + 1
			continue
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
		path := "/" + tag + fmt.Sprintf("[%d]", counts[tag])
		if parent >= 0 {
			path = nodes[parent].path + path
		}
		attrs := parseDiffAttrs(trimmed[len(tag):])
		node := htmlDiffNode{tag: tag, aid: attrs["data-odoc-aid"], attrs: attrs, path: path, parent: parent, order: len(nodes)}
		nodes = append(nodes, node)
		index := len(nodes) - 1
		if parent >= 0 {
			nodes[parent].children = append(nodes[parent].children, index)
		}
		selfClosing := strings.HasSuffix(strings.TrimSpace(raw), "/") || isDiffVoidTag(tag)
		if selfClosing {
			nodes[index].outer = source[lt : end+1]
		} else if isDiffRawTextTag(tag) {
			closeStart := strings.Index(strings.ToLower(source[end+1:]), "</"+tag)
			if closeStart < 0 {
				nodes[index].text = strings.Join(strings.Fields(source[end+1:]), " ")
				nodes[index].outer = source[lt:]
				cursor = len(source)
				continue
			}
			closeStart += end + 1
			closeEnd := diffTagEnd(source, closeStart)
			if closeEnd < 0 {
				closeEnd = len(source) - 1
			}
			nodes[index].text = strings.Join(strings.Fields(source[end+1:closeStart]), " ")
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
	if len(nodes) > maxDiffNodes {
		return nil, errDiffLimit
	}
	return nodes, nil
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

func diffTagName(raw string) string {
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
	}
	return strings.ToLower(raw[:end])
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
		attrs[name] = html.UnescapeString(value)
	}
	return attrs
}

func appendDiffText(nodes []htmlDiffNode, stack []diffOpenNode, text string) {
	if len(stack) == 0 || strings.TrimSpace(text) == "" {
		return
	}
	index := stack[len(stack)-1].index
	if nodes[index].text != "" {
		nodes[index].text += " "
	}
	nodes[index].text += strings.Join(strings.Fields(html.UnescapeString(text)), " ")
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

func isDiffWrapper(tag string) bool {
	return tag == "html" || tag == "head" || tag == "body"
}

func matchDiffNodes(before, after []htmlDiffNode) (map[int]int, error) {
	matches := map[int]int{}
	used := map[int]bool{}
	afterAID := map[string][]int{}
	for index, node := range after {
		if node.aid != "" {
			afterAID[node.aid] = append(afterAID[node.aid], index)
		}
	}
	for index, node := range before {
		candidates := afterAID[node.aid]
		if node.aid != "" && len(candidates) == 1 && !used[candidates[0]] {
			matches[index] = candidates[0]
			used[candidates[0]] = true
		}
	}
	afterPath := map[string]int{}
	for index, node := range after {
		if !used[index] {
			afterPath[node.path] = index
		}
	}
	for index, node := range before {
		if _, ok := matches[index]; ok {
			continue
		}
		if candidate, ok := afterPath[node.path]; ok && !used[candidate] && node.tag == after[candidate].tag {
			matches[index] = candidate
			used[candidate] = true
		}
	}
	comparisons := 0
	for beforeIndex, beforeNode := range before {
		if _, ok := matches[beforeIndex]; ok {
			continue
		}
		bestIndex, bestScore := -1, 0.0
		for afterIndex, afterNode := range after {
			if used[afterIndex] || beforeNode.tag != afterNode.tag {
				continue
			}
			comparisons++
			if comparisons > maxDiffComparisons {
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
	score += 0.25 * stringSimilarity(before.text, after.text)
	score += 0.15 * attrSimilarity(before.attrs, after.attrs)
	return score
}

func stringSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	a, b = strings.ToLower(a), strings.ToLower(b)
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
	keys := make([]string, 0, len(node.attrs))
	for key := range node.attrs {
		if key != "data-odoc-aid" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString(node.tag)
	for _, key := range keys {
		builder.WriteByte('|')
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(node.attrs[key])
	}
	builder.WriteByte('|')
	builder.WriteString(strings.Join(strings.Fields(node.text), " "))
	return builder.String()
}

func normalizeDiffHTML(value string) string {
	return strings.Join(strings.Fields(value), " ")
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
	lineBudget := maxDiffHunkLines
	for _, interval := range ranges {
		if lineBudget == 0 {
			break
		}
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
			if len(hunk.Lines) >= lineBudget {
				break
			}
			hunk.Lines = append(hunk.Lines, string(op.kind)+op.text)
			if op.kind != '+' {
				hunk.OldLines++
			}
			if op.kind != '-' {
				hunk.NewLines++
			}
		}
		lineBudget -= len(hunk.Lines)
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
	ops := make([]diffLineOp, 0, len(oldLines)+len(newLines))
	oldIndex, newIndex, comparisons := 0, 0, 0
	for oldIndex < len(oldLines) || newIndex < len(newLines) {
		if oldIndex < len(oldLines) && newIndex < len(newLines) && oldLines[oldIndex] == newLines[newIndex] {
			ops = append(ops, diffLineOp{kind: ' ', text: oldLines[oldIndex], oldLine: oldIndex + 1, newLine: newIndex + 1})
			oldIndex++
			newIndex++
			continue
		}
		bestOld, bestNew, bestDistance := -1, -1, syncWindow*2+1
		oldLimit, newLimit := oldIndex+syncWindow, newIndex+syncWindow
		if oldLimit > len(oldLines) {
			oldLimit = len(oldLines)
		}
		if newLimit > len(newLines) {
			newLimit = len(newLines)
		}
		for candidateOld := oldIndex; candidateOld < oldLimit; candidateOld++ {
			for candidateNew := newIndex; candidateNew < newLimit; candidateNew++ {
				comparisons++
				if comparisons > maxDiffComparisons {
					return nil, false
				}
				if oldLines[candidateOld] == newLines[candidateNew] && candidateOld-oldIndex+candidateNew-newIndex < bestDistance {
					bestOld, bestNew = candidateOld, candidateNew
					bestDistance = candidateOld - oldIndex + candidateNew - newIndex
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
	for cursor := 0; cursor < len(source); {
		if len(lines) >= maxDiffInputLines {
			return nil, false
		}
		if source[cursor] == '<' {
			end := diffTagEnd(source, cursor)
			if end < 0 {
				end = len(source) - 1
			}
			lines = append(lines, limitDiffLine(normalizeDiffHTML(source[cursor:end+1])))
			cursor = end + 1
			continue
		}
		end := strings.IndexByte(source[cursor:], '<')
		if end < 0 {
			end = len(source) - cursor
		}
		text := strings.Join(strings.Fields(html.UnescapeString(source[cursor:cursor+end])), " ")
		if text != "" {
			lines = append(lines, limitDiffLine(text))
		}
		cursor += end
	}
	return lines, true
}

func limitDiffLine(line string) string {
	const maxLine = 1024
	if len(line) <= maxLine {
		return line
	}
	return line[:maxLine] + "…"
}

func diffOutputSize(result *VersionDiff) int {
	size := 0
	for _, change := range result.Changes {
		size += len(change.Kind) + len(change.BeforeAID) + len(change.AfterAID) + len(change.DOMPath)
		size += len(change.BeforePath) + len(change.AfterPath) + len(change.BeforeHTML) + len(change.AfterHTML)
	}
	for _, hunk := range result.CodeHunks {
		for _, line := range hunk.Lines {
			size += len(line)
		}
	}
	return size
}
