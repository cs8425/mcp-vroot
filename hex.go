package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type HexInt64 int64

func (h HexInt64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(h), 10)), nil
}

func (h *HexInt64) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 {
		return fmt.Errorf("HexInt64: empty value")
	}

	// eg: 0x10
	if n, err, ok := parseHexInt64(string(raw)); ok {
		if err != nil {
			return err
		}
		*h = HexInt64(n)
		return nil
	}

	// eg: "0x10"
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}

		s = strings.TrimSpace(s)

		if n, err, ok := parseHexInt64(s); ok {
			if err != nil {
				return err
			}
			*h = HexInt64(n)
			return nil
		}
	}

	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return fmt.Errorf("HexInt64: %w", err)
	}

	*h = HexInt64(n)
	return nil
}

func parseHexInt64(s string) (int64, error, bool) {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}

	if !hasHexPrefix(s) {
		return 0, nil, false
	}

	n, err := strconv.ParseInt(s[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("HexInt64: invalid hex %q: %w", s, err), true
	}

	if neg {
		n = -n
	}

	return n, nil, true
}

func hasHexPrefix(s string) bool {
	if len(s) < 2 || s[0] != '0' {
		return false
	}
	return s[1] == 'x' || s[1] == 'X'
}

// modify from golang encoding/hex
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Dump returns a string that contains a hex dump of the given data. The format
// of the hex dump matches the output of `hexdump -C` on the command line.
func Dump(data []byte, base int) string {
	if len(data) == 0 {
		return ""
	}

	var buf strings.Builder
	// Dumper will write 79 bytes per complete 16 byte chunk, and at least
	// 64 bytes for whatever remains. Round the allocation up, since only a
	// maximum of 15 bytes will be wasted.
	buf.Grow((1 + ((len(data) - 1) / 16)) * 79)

	dumper := Dumper(&buf, base)
	dumper.Write(data)
	dumper.Close()
	return buf.String()
}

// Dumper returns a [io.WriteCloser] that writes a hex dump of all written data to
// w. The format of the dump matches the output of `hexdump -C` on the command
// line.
func Dumper(w io.Writer, base int) io.WriteCloser {
	return &dumper{w: w, base: base}
}

type dumper struct {
	w          io.Writer
	rightChars [18]byte
	buf        [14]byte
	used       int  // number of bytes in the current line
	n          uint // number of bytes, total
	closed     bool
	base       int
}

func toChar(b byte) byte {
	if b < 32 || b > 126 {
		return '.'
	}
	return b
}

func (h *dumper) Write(data []byte) (n int, err error) {
	if h.closed {
		return 0, errors.New("encoding/hex: dumper closed")
	}

	// Output lines look like:
	// 00000010  2e 2f 30 31 32 33 34 35  36 37 38 39 3a 3b 3c 3d  |./0123456789:;<=|
	// ^ offset                          ^ extra space              ^ ASCII of line.
	for i := range data {
		if h.used == 0 {
			// At the beginning of a line we print the current
			// offset in hex.
			off := h.n + uint(h.base)
			h.buf[0] = byte(off >> 24)
			h.buf[1] = byte(off >> 16)
			h.buf[2] = byte(off >> 8)
			h.buf[3] = byte(off)
			hex.Encode(h.buf[4:], h.buf[:4])
			h.buf[12] = ' '
			h.buf[13] = ' '
			_, err = h.w.Write(h.buf[4:])
			if err != nil {
				return
			}
		}
		hex.Encode(h.buf[:], data[i:i+1])
		h.buf[2] = ' '
		l := 3
		if h.used == 7 {
			// There's an additional space after the 8th byte.
			h.buf[3] = ' '
			l = 4
		} else if h.used == 15 {
			// At the end of the line there's an extra space and
			// the bar for the right column.
			h.buf[3] = ' '
			h.buf[4] = '|'
			l = 5
		}
		_, err = h.w.Write(h.buf[:l])
		if err != nil {
			return
		}
		n++
		h.rightChars[h.used] = toChar(data[i])
		h.used++
		h.n++
		if h.used == 16 {
			h.rightChars[16] = '|'
			h.rightChars[17] = '\n'
			_, err = h.w.Write(h.rightChars[:])
			if err != nil {
				return
			}
			h.used = 0
		}
	}
	return
}

func (h *dumper) Close() (err error) {
	// See the comments in Write() for the details of this format.
	if h.closed {
		return
	}
	h.closed = true
	if h.used == 0 {
		return
	}
	h.buf[0] = ' '
	h.buf[1] = ' '
	h.buf[2] = ' '
	h.buf[3] = ' '
	h.buf[4] = '|'
	nBytes := h.used
	for h.used < 16 {
		l := 3
		if h.used == 7 {
			l = 4
		} else if h.used == 15 {
			l = 5
		}
		_, err = h.w.Write(h.buf[:l])
		if err != nil {
			return
		}
		h.used++
	}
	h.rightChars[nBytes] = '|'
	h.rightChars[nBytes+1] = '\n'
	_, err = h.w.Write(h.rightChars[:nBytes+2])
	return
}
