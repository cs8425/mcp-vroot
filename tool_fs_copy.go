package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"mcp-vroot/jsonschema"
)

type ToolStateCopy struct {
	Root string
}

type ToolCopy struct {
	SrcPath string `json:"src,omitempty"`
	DstPath string `json:"dst,omitempty"`
}

func regToolFsCopy(reg *Registry, conf *ToolStateCopy) {
	reg.Register(&ToolDefine{
		Name: "copy",
		Description: `copy files and directories (recursively) to a new destination path.

Requirements:
- dst must NOT already exist (neither as a file nor as a directory); the operation fails if dst exists — no overwriting.
- dst cannot be an existing directory (files are not copied "into" a directory); dst is the new path itself.
- The parent directory of dst must already exist; it is not created automatically.

Copied file contents and permission bits only.
Timestamps, ownership, xattrs, ACLs, Symlink, and hard links are not preserved.`,
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"src": {
					Type: jsonschema.String,
				},
				"dst": {
					Type: jsonschema.String,
				},
			},
			Required: []string{
				"src",
				"dst",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolCopy)
		},
	}, conf, func(req *ToolRequest, out *ToolCopy) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if len(out.SrcPath) <= 0 {
			return ErrBadParam
		}
		out.SrcPath = toRootPath(out.SrcPath)
		if len(out.DstPath) <= 0 {
			return ErrBadParam
		}
		out.DstPath = toRootPath(out.DstPath)
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolCopy, state *ToolStateCopy) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][cp]src=%v, dst=%v\n", param.SrcPath, param.DstPath)
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

		_, err = root.Lstat(param.DstPath)
		if err == nil {
			// exist => error
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: destination already exists: %v", param.DstPath),
				},
			}, true
		}
		if !os.IsNotExist(err) {
			// not "not exist" => error
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		// do copy
		hint, err, isErr := doFsCopy(ctx, root, param.SrcPath, param.DstPath)
		if err != nil {
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
				Text: hint,
			},
		}, isErr
	})
}

const (
	copyBufSize       = 4 * 1024 * 1024
	MaxShowingSkipped = 64
)

type copyStats struct {
	files              int
	dirs               int
	skipped            int
	skippedList        []string
	skippedSpecial     int
	skippedSpecialList []string
}

// doFsCopy implements:
//   - file -> file
//   - directory -> recursive directory
//   - directory-contained symlink -> skip and report
//   - source symlink -> error
//   - destination must not exist
//   - destination must not be source itself or inside source
//
// hint is intended to be shown to the LLM.
// err is the underlying/tool error.
// isErr indicates that the tool call failed.
//
// On failure the original destination is left untouched as far as this
// function's own writes are concerned; work is done in a temporary sibling.
func doFsCopy(ctx context.Context, root *os.Root, src string, dst string) (hint string, err error, isErr bool) {
	// Source must exist.
	srcInfo, err := root.Lstat(src)
	if err != nil {
		return fmt.Sprintf(
			"source does not exist or cannot be accessed: %q",
			src,
		), err, true
	}

	// Direct source symlink => error, and tell LLM where it points.
	if srcInfo.Mode()&fs.ModeSymlink != 0 {
		target, readlinkErr := root.Readlink(src)
		if readlinkErr != nil {
			err := fmt.Errorf(
				"source %q is a symbolic link, but its target could not be read: %w",
				src,
				readlinkErr,
			)
			return err.Error(), err, true
		}

		err := fmt.Errorf(
			"source %q is a symbolic link -> %q; fs_cp does not copy symbolic links, use the target path explicitly",
			src,
			target,
		)
		return err.Error(), err, true
	}

	// Destination must not exist.
	if _, statErr := root.Lstat(dst); statErr == nil {
		err := fmt.Errorf(
			"destination already exists: %q",
			dst,
		)
		return err.Error(), err, true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Sprintf(
			"cannot check destination %q",
			dst,
		), statErr, true
	}

	// Parent directory must exist.
	dstParent := path.Dir(dst)
	dstParentInfo, statErr := root.Stat(dstParent)
	if statErr != nil {
		err := fmt.Errorf(
			"destination parent directory does not exist or cannot be accessed: %q",
			dstParent,
		)
		return err.Error(), err, true
	}
	if !dstParentInfo.IsDir() {
		err := fmt.Errorf(
			"destination parent is not a directory: %q",
			dstParent,
		)
		return err.Error(), err, true
	}

	// Do not copy a directory into itself or one of its descendants.
	if srcInfo.IsDir() {
		inside, checkErr := isInsideSource(root, src, dst, srcInfo)
		if checkErr != nil {
			return fmt.Sprintf(
				"cannot determine whether destination is inside source: source=%q destination=%q",
				src, dst,
			), checkErr, true
		}
		if inside {
			err := fmt.Errorf(
				"destination %q is the source itself or is inside source directory %q",
				dst,
				src,
			)
			return err.Error(), err, true
		}

		return doDirCopy(ctx, root, src, dst, srcInfo.Mode().Perm())
	}

	if srcInfo.Mode().IsRegular() {
		return doFileCopy(ctx, root, src, dst, srcInfo.Mode().Perm())
	}

	err = fmt.Errorf(
		"unsupported source file type: %q (%s)",
		src,
		srcInfo.Mode().String(),
	)
	return err.Error(), err, true
}

func doFileCopy(ctx context.Context, root *os.Root, src string, dst string, srcPrem fs.FileMode) (hint string, err error, isErr bool) {
	// Create a temporary sibling so the final destination does not
	// become visible until the copy has completed.
	var tmp string
	cleanup := func() {
		if tmp != "" {
			_ = root.RemoveAll(tmp)
		}
	}
	defer cleanup()

	// Regular file.
	dstParent := path.Dir(dst)
	tmp, err = createTempFile(root, dstParent, path.Base(dst))
	if err != nil {
		return "failed to create temporary destination file", err, true
	}

	// tmp file exist
	if err = copyFile(root, src, tmp, srcPrem); err != nil {
		return fmt.Sprintf(
			"copy failed; destination was not created: %v",
			err,
		), err, true
	}

	if _, statErr := root.Lstat(dst); statErr == nil {
		err := fmt.Errorf(
			"destination was created while copying: %q",
			dst,
		)
		return err.Error(), err, true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Sprintf(
			"cannot verify destination before commit: %q",
			dst,
		), statErr, true
	}

	if err = root.Rename(tmp, dst); err != nil {
		return fmt.Sprintf(
			"failed to commit copied file to %q",
			dst,
		), err, true
	}

	// flag ok
	tmp = ""

	return fmt.Sprintf(
		"Copied file %q to %q",
		src,
		dst,
	), nil, false
}

func doDirCopy(ctx context.Context, root *os.Root, src string, dst string, srcPrem fs.FileMode) (hint string, err error, isErr bool) {
	// Create a temporary sibling so the final destination does not
	// become visible until the copy has completed.
	var tmp string
	cleanup := func() {
		if tmp != "" {
			_ = root.RemoveAll(tmp)
		}
	}
	defer cleanup()

	var stats copyStats

	dstParent := path.Dir(dst)
	tmp, err = createTempDir(root, dstParent, path.Base(dst))
	if err != nil {
		return "failed to create temporary destination directory", err, true
	}

	if err = copyDir(root, src, tmp, &stats); err != nil {
		return fmt.Sprintf(
			"copy failed; destination was not created: %v",
			err,
		), err, true
	}

	// Re-check immediately before commit.
	// This reduces (but cannot completely eliminate) a race with
	// unrelated external writers.
	if _, statErr := root.Lstat(dst); statErr == nil {
		err := fmt.Errorf(
			"destination was created while copying: %q",
			dst,
		)
		return err.Error(), err, true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Sprintf(
			"cannot verify destination before commit: %q",
			dst,
		), statErr, true
	}

	if err := root.Chmod(tmp, srcPrem); err != nil {
		return fmt.Sprintf(
			"failed to set permission on temporary directory for %q",
			dst,
		), err, true
	}

	if err = root.Rename(tmp, dst); err != nil {
		return fmt.Sprintf(
			"failed to commit copied directory to %q",
			dst,
		), err, true
	}

	// flag ok
	tmp = ""

	hint = fmt.Sprintf(
		"Copied directory %q to %q",
		src,
		dst,
	)

	if stats.skipped > 0 {
		hint += fmt.Sprintf(
			". Skipped %d symbolic link(s), showing first %d:",
			stats.skipped,
			MaxShowingSkipped,
		)

		for _, item := range stats.skippedList {
			hint += "\n- " + item
		}
	}

	if stats.skippedSpecial > 0 {
		hint += fmt.Sprintf(
			". Skipped %d unsupported file(s), showing first %d:",
			stats.skippedSpecial,
			MaxShowingSkipped,
		)

		for _, item := range stats.skippedSpecialList {
			hint += "\n- " + item
		}
	}

	return hint, nil, false
}

func isInsideSource(
	root *os.Root,
	src string,
	dst string,
	srcInfo fs.FileInfo,
) (bool, error) {
	// Fast lexical check.
	if src == "." {
		return true, nil
	}

	if dst == src || strings.HasPrefix(dst, src+"/") {
		return true, nil
	}

	// Also check actual filesystem ancestry so a destination reached
	// through a symlink alias cannot bypass the lexical check.
	p := path.Dir(dst)

	for {
		info, err := root.Stat(p)
		if err != nil {
			return false, err
		}

		if os.SameFile(srcInfo, info) {
			return true, nil
		}

		if p == "." {
			break
		}

		next := path.Dir(p)
		if next == p {
			break
		}
		p = next
	}

	return false, nil
}

func createTempFile(
	root *os.Root,
	parent string,
	base string,
) (string, error) {
	for range 10 {
		suffix, err := randomSuffix()
		if err != nil {
			return "", err
		}

		name := path.Join(
			parent,
			fmt.Sprintf(".%s.mcp-copy-%s.tmp", base, suffix),
		)

		f, err := root.OpenFile(
			name,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0600,
		)
		if err == nil {
			if closeErr := f.Close(); closeErr != nil {
				_ = root.Remove(name)
				return "", closeErr
			}
			return name, nil
		}

		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}

	return "", errors.New("failed to create unique temporary file")
}

func createTempDir(
	root *os.Root,
	parent string,
	base string,
) (string, error) {
	for range 10 {
		suffix, err := randomSuffix()
		if err != nil {
			return "", err
		}

		name := path.Join(
			parent,
			fmt.Sprintf(".%s.mcp-copy-%s.tmp", base, suffix),
		)

		err = root.Mkdir(name, 0700)
		if err == nil {
			return name, nil
		}

		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}

	return "", errors.New("failed to create unique temporary directory")
}

func copyFile(
	root *os.Root,
	src string,
	dst string,
	perm fs.FileMode,
) error {
	srcFile, err := root.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := root.OpenFile(
		dst,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
		0600,
	)
	if err != nil {
		return err
	}

	var buf [copyBufSize]byte

	_, copyErr := io.CopyBuffer(dstFile, srcFile, buf[:])

	if copyErr == nil {
		copyErr = dstFile.Sync()
	}

	closeErr := dstFile.Close()

	if copyErr != nil {
		return copyErr
	}

	if closeErr != nil {
		return closeErr
	}

	// Preserve the source permission bits.
	// This is implementation-level metadata; the MCP tool does not expose
	// Unix permission bits to the LLM.
	if err := root.Chmod(dst, perm); err != nil {
		return err
	}

	return nil
}

func copyDir(
	root *os.Root,
	src string,
	dst string,
	stats *copyStats,
) error {
	srcInfo, err := root.Lstat(src)
	if err != nil {
		return err
	}

	if !srcInfo.IsDir() {
		return fmt.Errorf("not a directory: %q", src)
	}

	return fs.WalkDir(root.FS(), src, func(
		p string,
		d fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		// Root itself.
		if rel == "." {
			return nil
		}

		dstPath := path.Join(dst, rel)

		// Symlink: skip, do not descend.
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := root.Readlink(p)
			if err != nil {
				// return fmt.Errorf(
				// 	"readlink %q: %w",
				// 	p,
				// 	err,
				// )
				target = fmt.Sprintf("(readlink failed: %v)", err)
			}

			stats.skipped++

			if len(stats.skippedList) < MaxShowingSkipped {
				stats.skippedList = append(
					stats.skippedList,
					fmt.Sprintf("%s -> %s", p, target),
				)
			}

			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf(
				"stat %q: %w",
				p,
				err,
			)
		}

		switch {
		case info.IsDir():
			if err := root.Mkdir(
				dstPath,
				info.Mode().Perm(),
			); err != nil {
				return fmt.Errorf(
					"mkdir %q: %w",
					dstPath,
					err,
				)
			}

			stats.dirs++

		case info.Mode().IsRegular():
			// dst file not exist, need os.O_CREATE
			if err := copyFile(
				root,
				p,
				dstPath,
				info.Mode().Perm(),
			); err != nil {
				return fmt.Errorf(
					"copy %q -> %q: %w",
					p,
					dstPath,
					err,
				)
			}

			stats.files++

		default:
			// return fmt.Errorf(
			// 	"unsupported file type %q: %s",
			// 	p,
			// 	info.Mode(),
			// )
			stats.skippedSpecial++
			if len(stats.skippedSpecialList) < MaxShowingSkipped {
				stats.skippedSpecialList = append(
					stats.skippedSpecialList,
					fmt.Sprintf("%s (%s)", p, info.Mode()),
				)
			}
			return nil
		}

		return nil
	})
}

// func cleanRootPath(p string) (string, error) {
// 	if p == "" {
// 		return "", errors.New("path cannot be empty")
// 	}

// 	p = path.Clean(p)

// 	// Root paths are expected to be relative.
// 	// os.Root itself also rejects paths escaping the root.
// 	if path.IsAbs(p) {
// 		return "", fmt.Errorf("absolute path is not allowed: %q", p)
// 	}

// 	if p == ".." || strings.HasPrefix(p, "../") {
// 		return "", fmt.Errorf("path escapes filesystem root: %q", p)
// 	}

// 	return p, nil
// }
