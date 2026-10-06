package main

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// diffContext is the number of context lines to include before and after
	// each changed line in the generated unified diff.
	diffContext = 3

	// maxDiffCells limits the LCS diff computation size.
	// The LCS algorithm is O(oldLines * newLines); if the product exceeds
	// this threshold, the code falls back to a simpler prefix/suffix diff
	// to avoid excessive memory usage and runtime on large files.
	maxDiffCells = 4_000_000

	// dirDiagonal marks an LCS transition where the old and new lines match.
	// This produces a context line in the diff.
	dirDiagonal = 0

	// dirUp marks an LCS transition where a line exists only in the old file.
	// This produces a removed line in the diff.
	dirUp = 1

	// dirLeft marks an LCS transition where a line exists only in the new file.
	// This produces an added line in the diff.
	dirLeft = 2
)

type diffOp struct {
	kind byte // ' ', '-', '+'
	line string
}

func createUnifiedDiff(original, modified, filePath string) string {
	oldContent := normalizeLineEndings(original)
	newContent := normalizeLineEndings(modified)

	if oldContent == newContent {
		return ""
	}

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	ops := lineDiff(oldLines, newLines)
	return formatUnifiedDiff(filePath, ops)
}

// lineDiff implements a simple LCS-based line diff.
// It is O(n*m) in time and O(n*m) bytes in memory for the direction matrix.
// For very large files, it falls back to a simpler prefix/suffix diff.
func lineDiff(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)

	if int64(n)*int64(m) > maxDiffCells {
		return simpleLineDiff(oldLines, newLines)
	}

	width := m + 1
	dirs := make([]byte, (n+1)*width)
	prev := make([]int, m+1)
	cur := make([]int, m+1)

	for i := 1; i <= n; i++ {
		cur[0] = 0
		for j := 1; j <= m; j++ {
			switch {
			case oldLines[i-1] == newLines[j-1]:
				cur[j] = prev[j-1] + 1
				dirs[i*width+j] = dirDiagonal
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
				dirs[i*width+j] = dirUp
			default:
				cur[j] = cur[j-1]
				dirs[i*width+j] = dirLeft
			}
		}
		prev, cur = cur, prev
	}

	ops := make([]diffOp, 0, n+m)
	i, j := n, m

	for i > 0 || j > 0 {
		switch {
		case i == 0:
			ops = append(ops, diffOp{kind: '+', line: newLines[j-1]})
			j--
		case j == 0:
			ops = append(ops, diffOp{kind: '-', line: oldLines[i-1]})
			i--
		default:
			switch dirs[i*width+j] {
			case dirDiagonal:
				ops = append(ops, diffOp{kind: ' ', line: oldLines[i-1]})
				i--
				j--
			case dirUp:
				ops = append(ops, diffOp{kind: '-', line: oldLines[i-1]})
				i--
			case dirLeft:
				ops = append(ops, diffOp{kind: '+', line: newLines[j-1]})
				j--
			}
		}
	}

	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}

	return ops
}

// simpleLineDiff is a fallback for very large files.
// It keeps common prefix and suffix, and outputs the middle as removed/added lines.
func simpleLineDiff(oldLines, newLines []string) []diffOp {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}

	ops := make([]diffOp, 0, len(oldLines)+len(newLines))

	for i := 0; i < prefix; i++ {
		ops = append(ops, diffOp{kind: ' ', line: oldLines[i]})
	}
	for i := prefix; i < len(oldLines)-suffix; i++ {
		ops = append(ops, diffOp{kind: '-', line: oldLines[i]})
	}
	for i := prefix; i < len(newLines)-suffix; i++ {
		ops = append(ops, diffOp{kind: '+', line: newLines[i]})
	}
	for i := 0; i < suffix; i++ {
		ops = append(ops, diffOp{kind: ' ', line: newLines[len(newLines)-suffix+i]})
	}

	return ops
}

func formatUnifiedDiff(filePath string, ops []diffOp) string {
	hasChange := false
	for _, op := range ops {
		if op.kind != ' ' {
			hasChange = true
			break
		}
	}
	if !hasChange {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\toriginal\n", filePath)
	fmt.Fprintf(&b, "+++ %s\tmodified\n", filePath)

	changes := make([]int, 0, len(ops))
	for i, op := range ops {
		if op.kind != ' ' {
			changes = append(changes, i)
		}
	}

	type group struct{ start, end int }
	groups := make([]group, 0, len(changes))

	for _, idx := range changes {
		start := idx - diffContext
		if start < 0 {
			start = 0
		}
		end := idx + diffContext
		if end >= len(ops) {
			end = len(ops) - 1
		}

		if len(groups) > 0 && start <= groups[len(groups)-1].end+1 {
			if end > groups[len(groups)-1].end {
				groups[len(groups)-1].end = end
			}
		} else {
			groups = append(groups, group{start: start, end: end})
		}
	}

	for _, g := range groups {
		oldBefore, newBefore := 0, 0
		for i := 0; i < g.start; i++ {
			if ops[i].kind == ' ' || ops[i].kind == '-' {
				oldBefore++
			}
			if ops[i].kind == ' ' || ops[i].kind == '+' {
				newBefore++
			}
		}

		oldCount, newCount := 0, 0
		for i := g.start; i <= g.end; i++ {
			if ops[i].kind == ' ' || ops[i].kind == '-' {
				oldCount++
			}
			if ops[i].kind == ' ' || ops[i].kind == '+' {
				newCount++
			}
		}

		oldStart := oldBefore + 1
		if oldCount == 0 {
			oldStart = oldBefore
		}

		newStart := newBefore + 1
		if newCount == 0 {
			newStart = newBefore
		}

		fmt.Fprintf(&b, "@@ -%s +%s @@\n",
			formatHunkRange(oldStart, oldCount),
			formatHunkRange(newStart, newCount),
		)

		for i := g.start; i <= g.end; i++ {
			b.WriteByte(ops[i].kind)
			b.WriteString(ops[i].line)
			b.WriteByte('\n')
		}
	}

	return b.String()
}

func formatHunkRange(start, count int) string {
	if count == 0 || count == 1 {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
