package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"mcp-vroot/jsonschema"
)

type ToolStateEditFile struct {
	Root string
}

type FileEdit struct {
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text,omitempty"`
}

type ToolEditFile struct {
	Path   string     `json:"path,omitempty"`
	Edits  []FileEdit `json:"edits,omitempty"`
	DryRun bool       `json:"dry_run,omitempty"`
}

func regToolFsEditFile(reg *Registry, conf *ToolStateEditFile) {
	reg.Register(&ToolDefine{
		Name:        "edit",
		Description: "Make line-based edits to a text file. Each edit replaces exact line sequences with new content. Returns a git-style diff showing the changes made.",
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"path": {
					Type: jsonschema.String,
				},
				"edits": {
					Type: jsonschema.Array,
					Items: &jsonschema.Definition{
						Type: jsonschema.Object,
						Properties: map[string]jsonschema.Definition{
							"old_text": {
								Type:        jsonschema.String,
								Description: "Text to search for (exact match)",
							},
							"new_text": {
								Type:        jsonschema.String,
								Description: "Text to replace with",
							},
						},
						Required: []string{
							"old_text",
							"new_text",
						},
					},
				},
				"dry_run": {
					Type:        jsonschema.Boolean,
					Description: `Preview changes using git-style diff format`,
				},
			},
			Required: []string{
				"path",
				"edits",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolEditFile)
		},
	}, conf, func(req *ToolRequest, out *ToolEditFile) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if len(out.Path) <= 0 {
			return ErrBadParam
		}
		out.Path = toRootPath(out.Path)
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolEditFile, state *ToolStateEditFile) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][edit_file]path=%v, dry_run=%v, length=%v\n", param.Path, param.DryRun, len(param.Edits))
		root, err := os.OpenRoot(state.Root)
		if err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		defer root.Close()

		// TODO: more efficiently, do not load all into memory
		diffText, err := ApplyFileEdits(root, param.Path, param.Edits, param.DryRun)
		if err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				Text: diffText,
			},
		}, false
	})
}

func ApplyFileEdits(root *os.Root, filePath string, edits []FileEdit, dryRun bool) (string, error) {
	raw, err := root.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	original := normalizeLineEndings(string(raw))
	modified := original

	for _, edit := range edits {
		old := normalizeLineEndings(edit.OldText)
		next := normalizeLineEndings(edit.NewText)

		// Exact match: replace first occurrence only.
		if strings.Contains(modified, old) {
			modified = strings.Replace(modified, old, next, 1)
			continue
		}

		// Flexible line-by-line match.
		// var matched bool
		// modified, matched = applyFlexibleReplace(modified, old, next)
		// if !matched {
		// 	return "", fmt.Errorf("could not find exact match for edit:\n%s", edit.OldText)
		// }
	}

	diff := createUnifiedDiff(original, modified, filePath)
	formattedDiff := formatDiffWithBackticks(diff)

	if !dryRun {
		if err := atomicReplaceFile(root, filePath, modified); err != nil {
			return "", err
		}
	}

	return formattedDiff, nil
}

func applyFlexibleReplace(modified, old, next string) (string, bool) {
	// Flexible line-by-line match.
	oldLines := strings.Split(old, "\n")
	contentLines := strings.Split(modified, "\n")
	matched := false

	for i := 0; i+len(oldLines) <= len(contentLines); i++ {
		ok := true
		for j := range oldLines {
			if strings.TrimSpace(oldLines[j]) != strings.TrimSpace(contentLines[i+j]) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}

		originalIndent := leadingWhitespace(contentLines[i])
		newLines := strings.Split(next, "\n")

		replaced := make([]string, 0, len(contentLines)-len(oldLines)+len(newLines))
		replaced = append(replaced, contentLines[:i]...)

		for j, line := range newLines {
			if j == 0 {
				replaced = append(replaced, originalIndent+trimLeftWhitespace(line))
				continue
			}

			var oldIndent string
			if j < len(oldLines) {
				oldIndent = leadingWhitespace(oldLines[j])
			}
			newIndent := leadingWhitespace(line)

			if oldIndent != "" && newIndent != "" {
				rel := utf8.RuneCountInString(newIndent) - utf8.RuneCountInString(oldIndent)
				if rel < 0 {
					rel = 0
				}
				replaced = append(replaced, originalIndent+strings.Repeat(" ", rel)+trimLeftWhitespace(line))
				continue
			}

			replaced = append(replaced, line)
		}

		replaced = append(replaced, contentLines[i+len(oldLines):]...)
		modified = strings.Join(replaced, "\n")
		matched = true
		break
	}
	return modified, matched
}

func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func leadingWhitespace(s string) string {
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	return s[:i]
}

func trimLeftWhitespace(s string) string {
	return s[len(leadingWhitespace(s)):]
}

func formatDiffWithBackticks(diff string) string {
	ticks := "```"
	for strings.Contains(diff, ticks) {
		ticks += "`"
	}
	return fmt.Sprintf("%sdiff\n%s%s\n\n", ticks, diff, ticks)
}
