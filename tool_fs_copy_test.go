package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests drive doFsCopy() directly through an os.Root, which is where the
// path handling actually matters. The spellings below ("./x", "/x", "x/", "x/.",
// "a/../b") are what the tool surface receives from the LLM; they used to make
// every directory copy die inside fs.WalkDir(root.FS(), ...) with
// `stat ./x: invalid argument`.

// --- helpers ---------------------------------------------------------------

func copyTestMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", p, err)
	}
}

func copyTestWriteFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write %q: %v", p, err)
	}
}

// copyTestFixture builds a tree covering recursion depth, hidden files,
// non-ASCII names and an empty directory.
func copyTestFixture(t *testing.T, root string) {
	t.Helper()

	for _, d := range []string{"sub/deep", "sub2", "empty"} {
		copyTestMkdirAll(t, filepath.Join(root, filepath.FromSlash(d)))
	}

	files := []struct {
		path string
		text string
	}{
		{"a.txt", "root file A\n"},
		{".hidden", "hidden dotfile\n"},
		{"中文 檔名.txt", "unicode name\n"},
		{"sub/b.txt", "sub dir file B\n"},
		{"sub/deep/c.txt", "deep nested C\n"},
		{"sub2/d.txt", "sub2 file D\n"},
	}
	for _, f := range files {
		copyTestWriteFile(t, filepath.Join(root, filepath.FromSlash(f.path)), []byte(f.text))
	}
}

func copyTestOpen(t *testing.T, base string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatalf("OpenRoot(%q): %v", base, err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})
	return root
}

// copyTestSnapshot returns directories in walk order plus all file contents.
// Symlinks are skipped on purpose, they are never copied.
func copyTestSnapshot(t *testing.T, dir string) ([]string, map[string][]byte) {
	t.Helper()

	var dirs []string
	files := map[string][]byte{}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, filepath.ToSlash(rel))
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk %q: %v", dir, err)
	}
	return dirs, files
}

func copyTestKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// copyTestCompare asserts that dst mirrors src exactly.
func copyTestCompare(t *testing.T, src string, dst string) {
	t.Helper()

	wantDirs, wantFiles := copyTestSnapshot(t, src)
	gotDirs, gotFiles := copyTestSnapshot(t, dst)

	if len(gotDirs) != len(wantDirs) {
		t.Errorf("directory set differs: got %v want %v", gotDirs, wantDirs)
	} else {
		for i := range wantDirs {
			if gotDirs[i] != wantDirs[i] {
				t.Errorf("directory differs at index %d: got %q want %q", i, gotDirs[i], wantDirs[i])
			}
		}
	}

	if len(gotFiles) != len(wantFiles) {
		t.Errorf("file count differs: got %d %v want %d %v",
			len(gotFiles), copyTestKeys(gotFiles),
			len(wantFiles), copyTestKeys(wantFiles))
	}

	for name, want := range wantFiles {
		got, ok := gotFiles[name]
		if !ok {
			t.Errorf("missing copied file %q", name)
			continue
		}
		if !bytes.Equal(want, got) {
			t.Errorf("content differs for %q: got %d bytes want %d bytes", name, len(got), len(want))
		}
	}
}

// --- tests -----------------------------------------------------------------

// A directory copy must work no matter how the source path is spelled.
func TestFsCopyDirPathSpellings(t *testing.T) {
	base := t.TempDir()
	root := copyTestOpen(t, base)
	copyTestFixture(t, filepath.Join(base, "src"))

	cases := []struct {
		name string
		src  string
	}{
		{"clean", "src"},
		{"dot_prefix", "./src"},
		{"leading_slash", "/src"},
		{"trailing_slash", "src/"},
		{"dot_segment", "src/."},
		{"dotdot_detour", "./sub/../src"},
		{"mixed", "/./src"},
		{"double_slash", "src//"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := "out_" + tc.name

			hint, err, isErr := doFsCopy(context.Background(), root, tc.src, dst)
			if isErr || err != nil {
				t.Fatalf("doFsCopy(%q, %q) failed: isErr=%v err=%v hint=%v",
					tc.src, dst, isErr, err, hint)
			}
			copyTestCompare(t, filepath.Join(base, "src"), filepath.Join(base, dst))
		})
	}
}

// A single file copy must keep working, and the destination spelling is
// handled by the os.Root APIs which are more lenient than os.Root.FS().
func TestFsCopyFileSpelling(t *testing.T) {
	base := t.TempDir()
	root := copyTestOpen(t, base)
	copyTestFixture(t, filepath.Join(base, "src"))

	sources := []string{"src/a.txt", "./src/a.txt", "/src/a.txt"}
	for i, src := range sources {
		dst := "out_file" + string(rune('a'+i)) + ".txt"

		hint, err, isErr := doFsCopy(context.Background(), root, src, dst)
		if isErr || err != nil {
			t.Fatalf("doFsCopy(%q, %q) failed: isErr=%v err=%v hint=%v",
				src, dst, isErr, err, hint)
		}
		if !strings.HasPrefix(hint, "Copied file") {
			t.Errorf("unexpected hint for file copy: %v", hint)
		}

		got, err := os.ReadFile(filepath.Join(base, dst))
		if err != nil {
			t.Fatalf("read %q: %v", dst, err)
		}
		if string(got) != "root file A\n" {
			t.Errorf("content of %q = %q", dst, got)
		}
	}
}

// Trailing slash on the destination must not be mistaken for a missing parent.
func TestFsCopyDstTrailingSlash(t *testing.T) {
	base := t.TempDir()
	root := copyTestOpen(t, base)
	copyTestFixture(t, filepath.Join(base, "src"))

	hint, err, isErr := doFsCopy(context.Background(), root, "src", "out_slash/")
	if isErr || err != nil {
		t.Fatalf("copy with trailing-slash dst failed: isErr=%v err=%v hint=%v", isErr, err, hint)
	}
	copyTestCompare(t, filepath.Join(base, "src"), filepath.Join(base, "out_slash"))
}

// Nothing may be copied onto an existing path, into the source, or anywhere
// the destination parent does not exist.
func TestFsCopyGuards(t *testing.T) {
	base := t.TempDir()
	root := copyTestOpen(t, base)
	copyTestFixture(t, filepath.Join(base, "src"))
	copyTestMkdirAll(t, filepath.Join(base, "existing_dir"))
	copyTestWriteFile(t, filepath.Join(base, "a_file.txt"), []byte("x\n"))

	cases := []struct {
		name string
		src  string
		dst  string
	}{
		{"dst_exists_dir", "src", "existing_dir"},
		{"dst_exists_file", "src", "a_file.txt"},
		{"dst_exists_dir_dot", "./src", "./existing_dir"},
		{"dst_parent_missing", "src", "no_such_dir/out"},
		{"dst_parent_deep_missing", "src", "a/b/c/out"},
		{"dst_parent_is_file", "src", "a_file.txt/out"},
		{"dst_inside_src", "src", "src/inner"},
		{"dst_inside_src_deep", "./src", "./src/sub/inner"},
		{"dst_inside_src_trailing", "src", "src/inner/"},
		{"dst_equals_src", "src", "src"},
		{"src_is_root", ".", "out"},
		{"src_is_root_dot_slash", "./", "out"},
		{"src_is_root_slash", "/", "out"},
		{"src_missing", "no_such_src", "out"},
		{"src_missing_dot", "./no_such_src", "out"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint, err, isErr := doFsCopy(context.Background(), root, tc.src, tc.dst)
			if !isErr {
				t.Fatalf("expected failure for src=%q dst=%q, got success: hint=%v",
					tc.src, tc.dst, hint)
			}
			if err == nil {
				t.Errorf("isErr set without error for src=%q dst=%q", tc.src, tc.dst)
			}
		})
	}

	// the pre-existing tree must be untouched ...
	if _, err := os.Stat(filepath.Join(base, "existing_dir")); err != nil {
		t.Errorf("existing_dir damaged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "src", "sub", "deep", "c.txt")); err != nil {
		t.Errorf("source tree damaged: %v", err)
	}
	// ... and none of the rejected calls may have created its destination
	for _, name := range []string{"out", "no_such_dir", "a", "inner"} {
		if _, err := os.Lstat(filepath.Join(base, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("rejected copy created %q (lstat err=%v)", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "src", "inner")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rejected copy created src/inner (lstat err=%v)", err)
	}
}

// Symlinks inside a copied directory are skipped and reported, never followed;
// a symlink as the source itself is refused.
func TestFsCopySymlinks(t *testing.T) {
	base := t.TempDir()
	root := copyTestOpen(t, base)
	copyTestFixture(t, filepath.Join(base, "src"))

	if err := os.Symlink("a.txt", filepath.Join(base, "src", "link_to_file")); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	if err := os.Symlink("sub", filepath.Join(base, "src", "link_to_dir")); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	hint, err, isErr := doFsCopy(context.Background(), root, "./src", "out_symlink")
	if isErr || err != nil {
		t.Fatalf("copy with symlinks failed: isErr=%v err=%v hint=%v", isErr, err, hint)
	}
	if !strings.Contains(hint, "Skipped 2 symbolic link") {
		t.Errorf("expected the 2 skipped symlinks to be reported, hint=%v", hint)
	}

	for _, name := range []string{"link_to_file", "link_to_dir"} {
		if _, err := os.Lstat(filepath.Join(base, "out_symlink", name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s must not be copied (lstat err=%v)", name, err)
		}
	}
	// the symlinked directory must not have been pulled in either
	if _, err := os.Lstat(filepath.Join(base, "out_symlink", "link_to_dir", "deep")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("symlinked dir was followed (lstat err=%v)", err)
	}
	// regular content must survive
	if _, err := os.Stat(filepath.Join(base, "out_symlink", "sub2", "d.txt")); err != nil {
		t.Errorf("regular file lost: %v", err)
	}

	if _, _, isErr := doFsCopy(context.Background(), root, "./src/link_to_file", "out_symlink_src"); !isErr {
		t.Errorf("a symlink as source must be refused, not dereferenced")
	}
}

// The atomic-commit strategy must leave no temporary sibling behind, also when
// the very same copy is rejected on the second attempt.
func TestFsCopyNoTempLeftover(t *testing.T) {
	base := t.TempDir()
	root := copyTestOpen(t, base)
	copyTestFixture(t, filepath.Join(base, "src"))

	if _, _, isErr := doFsCopy(context.Background(), root, "./src", "./out_clean"); isErr {
		t.Fatalf("first copy failed")
	}
	// destination now exists => rejected
	if _, _, isErr := doFsCopy(context.Background(), root, "./src", "./out_clean"); !isErr {
		t.Fatalf("second copy should have been rejected")
	}
	// a copy whose source does not exist must not create anything either
	if _, _, isErr := doFsCopy(context.Background(), root, "./no_such_src", "./out_nothing"); !isErr {
		t.Fatalf("copy of missing source should have failed")
	}

	if _, err := os.Stat(filepath.Join(base, "out_clean", "sub", "b.txt")); err != nil {
		t.Errorf("out_clean incomplete: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "out_nothing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed copy created out_nothing (lstat err=%v)", err)
	}

	leftovers := []string{}
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(d.Name(), ".mcp-copy-") {
			rel, _ := filepath.Rel(base, p)
			leftovers = append(leftovers, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(leftovers) > 0 {
		t.Errorf("temporary leftovers: %v", leftovers)
	}
}

// Payload larger than copyBufSize (4 MiB) must survive the io.CopyBuffer loop.
func TestFsCopyLargeFile(t *testing.T) {
	base := t.TempDir()
	root := copyTestOpen(t, base)

	copyTestMkdirAll(t, filepath.Join(base, "big"))
	payload := bytes.Repeat([]byte("0123456789abcdef"), 5<<16+1) // 5 MiB + 16 B
	copyTestWriteFile(t, filepath.Join(base, "big", "payload.bin"), payload)

	hint, err, isErr := doFsCopy(context.Background(), root, "big", "out_big")
	if isErr || err != nil {
		t.Fatalf("copy failed: isErr=%v err=%v hint=%v", isErr, err, hint)
	}

	got, err := os.ReadFile(filepath.Join(base, "out_big", "payload.bin"))
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload corrupted: got %d bytes want %d bytes", len(got), len(payload))
	}
}

func TestToFsPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"x", "x"},
		{"./x", "x"},
		{"/x", "x"},
		{"x/", "x"},
		{"x/.", "x"},
		{"a//b", "a/b"},
		{"a/../b", "b"},
		{"./sub/../x", "x"},
		{"a/b", "a/b"},
		{".", "."},
		{"./", "."},
		{"/", "."},
		{"中文/檔 名", "中文/檔 名"},
	}

	for _, tc := range cases {
		got := toFsPath(tc.in)
		if got != tc.want {
			t.Errorf("toFsPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if !fs.ValidPath(got) {
			t.Errorf("toFsPath(%q) = %q is not fs.ValidPath", tc.in, got)
		}
		// must be idempotent, copyDir() normalizes a second time
		if again := toFsPath(got); again != got {
			t.Errorf("toFsPath not idempotent for %q: %q -> %q", tc.in, got, again)
		}
	}
}

// Known gap, not asserted on purpose: sub-directories are created with
// root.Mkdir(mode) only, which is masked by the process umask and never
// chmod'ed afterwards, so a 0777 source directory does not stay 0777. Fix that
// together with a mode assertion here (the top-level directory is already
// chmod'ed in doDirCopy, files are chmod'ed in copyFile).
func TestFsCopyDirModeIsLossy(t *testing.T) {
	t.Skip("known limitation: directory permission bits are not preserved faithfully")
}
