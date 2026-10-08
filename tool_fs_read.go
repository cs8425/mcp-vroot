package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"mcp-vroot/jsonschema"
)

var (
	ErrNoContent  = errors.New("no return data")
	ErrBinaryData = errors.New("binary data, not support")
)

type ToolStateReadFile struct {
	Root          string
	MaxLines      int
	MaxBufferSize int
}

type ToolReadFile struct {
	Path     string   `json:"path,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Offset   HexInt64 `json:"offset,omitempty"`
	Length   int64    `json:"length,omitempty"`
	ShowLine *bool    `json:"show_line,omitempty"`
	showLine bool

	buf bytes.Buffer
	sb  strings.Builder
}

func regToolFsReadFile(reg *Registry, conf *ToolStateReadFile) {
	const MaxBufferSize = 2 * 1024
	const MaxLines = 150

	if conf == nil {
		conf = &ToolStateReadFile{
			Root:          "",
			MaxLines:      MaxLines,
			MaxBufferSize: MaxBufferSize,
		}
	}
	if conf.MaxLines <= 0 {
		conf.MaxLines = MaxLines
	}
	if conf.MaxBufferSize <= 0 {
		conf.MaxBufferSize = MaxBufferSize
	}

	checkBin := func(out []byte, err error) error {
		if bytes.Contains(out, []byte{0x00}) {
			return ErrBinaryData
		}
		if len(out) == 0 && err != nil {
			return err
		}
		// if len(out) == 0 {
		// 	return ErrNoContent
		// }
		return nil
	}

	readAsByte := func(fd *os.File, offset int64, length int64, buf *bytes.Buffer) ([]ChatMessagePart, bool) {
		rd := io.NewSectionReader(fd, offset, length+1)
		buf.Reset()
		n, err := io.Copy(buf, rd)
		_ = n
		out := buf.Bytes()
		if err != nil {
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		if len(out) == 0 {
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", ErrNoContent.Error()),
				},
			}, true
		}
		// if err := checkBin(out, err); err != nil {
		// 	// return nil, err
		// 	return []ChatMessagePart{
		// 		{
		// 			Type: ChatMessagePartTypeText,
		// 			Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
		// 		},
		// 	}, true
		// }
		// if n != int64(len(out)) {
		// 	return nil, io.ErrShortBuffer
		// }
		if n > length {
			fmt.Fprintf(buf, "Warning: returned %v; continue with offset=%v\n", length, length+offset)
			out = out[:length]
		}
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				// Text: buf.String(),
				// Text: string(out), // len(out) == 0 ?
				Text: Dump(out, int(offset)),
			},
		}, false
	}

	readAsLine := func(fd *os.File, offset int64, length int64, showLine bool, buf *bytes.Buffer) ([]ChatMessagePart, bool) {
		baseOffset := -offset
		maxRetCount := length
		scanner := bufio.NewScanner(fd)
		found, write := int64(0), int64(0)
		var err error
		for scanner.Scan() {
			bufLine := scanner.Bytes()
			if bytes.Contains(bufLine, []byte{0x00}) {
				// skip, seems like binary file
				err = ErrBinaryData
				break
			}
			// line := string(bufLine)
			// fmt.Println(line)
			found += 1
			if found+baseOffset > 0 && write < maxRetCount {
				if showLine {
					fmt.Fprintf(buf, "% 3d | ", found)
				}
				buf.Write(bufLine)
				buf.Write([]byte{'\n'})
				write += 1
			}
			if write >= maxRetCount {
				break
			}
		}
		if write >= maxRetCount {
			buf.WriteString("Warning: reach maximum output limit\n")
		}
		out := buf.Bytes()
		if err := checkBin(out, err); err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		// if n != int64(len(out)) {
		// 	return nil, io.ErrShortBuffer
		// }
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				Text: buf.String(),
			},
		}, false
	}

	reg.Register(&ToolDefine{
		Name: "read_file",
		Description: fmt.Sprintf("以line或byte為單位，讀取指定路徑的檔案內容，單次回傳最大%v lines或%v bytes。可使用offset跟length讀取的內容。",
			conf.MaxLines,
			conf.MaxBufferSize,
		),
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"path": {
					Type: jsonschema.String,
				},
				"offset": {
					Type:        jsonschema.Integer,
					Description: "offset in lines or bytes from file beginning",
				},
				"length": {
					Type: jsonschema.Integer,
					Description: fmt.Sprintf("read only N lines or bytes data, max %v lines or %v bytes",
						conf.MaxLines,
						conf.MaxBufferSize,
					),
				},
				"mode": {
					Type:        jsonschema.String,
					Description: "read by line or read by byte",
					Enum: []string{
						"line",
						"hex",
					},
				},
				"show_line": {
					Type:        jsonschema.Boolean,
					Description: "show line number when using line mode, start from 1. (default: true)",
				},
			},
			Required: []string{"path"},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolReadFile)
		},
	}, conf, func(req *ToolRequest, out *ToolReadFile) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if out.Offset < 0 {
			return ErrBadParam
		}
		if out.Length < 0 {
			return ErrBadParam
		}
		switch out.Mode {
		default:
			out.Mode = "line"
			fallthrough
		case "line":
			if out.Length > MaxLines {
				return ErrBadParam
			}
			if out.Length == 0 {
				out.Length = MaxLines
			}
			if out.ShowLine == nil {
				defVal := true
				out.ShowLine = &defVal
			}
			out.showLine = *out.ShowLine
		case "hex":
			if out.Length > MaxBufferSize {
				return ErrBadParam
			}
			if out.Length == 0 {
				out.Length = MaxBufferSize
			}
		}
		if len(out.Path) <= 0 {
			return ErrBadParam
		}
		out.Path = toRootPath(out.Path)
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolReadFile, state *ToolStateReadFile) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][read_file]path=%v, mode=%v, skip=%v, length=%v\n", param.Path, param.Mode, param.Offset, param.Length)
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
		fd, err := root.Open(param.Path)
		if err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		defer fd.Close()
		// info, _ := fd.Stat()
		// fSz := info.Size()

		switch param.Mode {
		case "hex":
			return readAsByte(fd, int64(param.Offset), param.Length, &param.buf)
		default:
			fallthrough
		case "line":
			return readAsLine(fd, int64(param.Offset), param.Length, param.showLine, &param.buf)
		}
	})
}
