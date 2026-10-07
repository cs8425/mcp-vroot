package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log"
	"os"
)

var (
	verbosity = flag.Int("v", 3, "verbosity")
)

func Vf(level int, format string, v ...interface{}) {
	if level <= *verbosity {
		log.Printf(format, v...)
	}
}
func V(level int, v ...interface{}) {
	if level <= *verbosity {
		log.Print(v...)
	}
}
func Vln(level int, v ...interface{}) {
	if level <= *verbosity {
		log.Println(v...)
	}
}

func randomSuffix() (string, error) {
	var b [15]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// for os.Root API, file path should not in absolute
func toRootPath(fp string) string {
	if len(fp) >= 1 && fp[:1] == "/" {
		return "." + fp // "/abs-path/to-file" => "./abs-path/to-file"
	}
	return fp
}

func openFd(root *os.Root, fp string) (*os.File, int64, error) {
	fd, err := root.Open(fp)
	if err != nil {
		return nil, -1, err
	}
	fInfo, err := fd.Stat()
	if err != nil {
		return nil, -1, err
	}
	return fd, fInfo.Size(), nil
}

func getFileSha256Size(root *os.Root, fp string, h256 hash.Hash, buf []byte) ([]byte, int64, error) {
	fd, err := root.Open(fp)
	if err != nil {
		return nil, 0, err
	}
	defer fd.Close()
	h256.Reset()
	sz, err := io.CopyBuffer(h256, fd, buf)
	if err != nil {
		return nil, 0, err
	}
	return h256.Sum(nil), sz, nil
}

func atomicWriteFile(root *os.Root, fp string, prem fs.FileMode, procFn func(fd *os.File) error) error {
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}

	// Temp file must be in the same directory for atomic rename.
	tmpPath := fp + "." + suffix + ".tmp"
	tmp, err := root.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}

	// pass tmp file to process function
	werr := procFn(tmp)

	_ = tmp.Chmod(prem)
	cerr := tmp.Close()

	if werr != nil || cerr != nil {
		_ = root.Remove(tmpPath)
		if werr != nil {
			return werr
		}
		return cerr
	}

	if err := root.Rename(tmpPath, fp); err != nil {
		_ = root.Remove(tmpPath)
		return err
	}

	// Restore permission bits. Failure should not fail the already-successful write.
	_ = root.Chmod(fp, prem)
	return nil
}

func atomicReplaceFile(root *os.Root, fp string, content string) error {
	info, err := root.Stat(fp)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.ErrInvalid
	}

	return atomicWriteFile(root, fp, info.Mode().Perm(), func(fd *os.File) error {
		_, werr := fd.WriteString(content)
		return werr
	})
}

func checkFileType(ext string) (isImg bool, mime string, isText bool) {
	isText = true
	isImg = false
	mime = ""
	switch ext {
	case ".md":
	case ".txt":
	case ".html":
	case ".js", ".ts":
	case ".css":
	// case ".svg":
	// 	isImg = true
	// 	isText = false
	// 	mime = "image/svg+xml"
	// case ".webp", ".avif": // not support
	case ".png", ".jpg", ".jpeg", ".gif":
		isImg = true
		isText = false
	default:
		isText = false
	}
	return
}

func ptr2String[T any](v *T) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v", *v)
}

type MaxConcurrent struct {
	pool  chan struct{}
	limit int
}

func (mc *MaxConcurrent) Limit() int {
	return mc.limit
}

func (mc *MaxConcurrent) Done() {
	<-mc.pool
}

func (mc *MaxConcurrent) Use(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		// abort
		return false
	case mc.pool <- struct{}{}:
		return true
	}
}

func NewMaxConcurrent(limit int) *MaxConcurrent {
	return &MaxConcurrent{
		pool:  make(chan struct{}, limit),
		limit: limit,
	}
}

type NoConcurrentLimit struct{}

func (mc *NoConcurrentLimit) Limit() int {
	return 1 << 16
}
func (mc *NoConcurrentLimit) Done() {}
func (mc *NoConcurrentLimit) Use(ctx context.Context) bool {
	return true
}
