package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mcp-vroot/jsonschema"
)

var (
	ErrNoTextContent    = errors.New("no return data, maybe image only")
	ErrNoToC            = errors.New("no ToC")
	ErrNotSupportFormat = errors.New("data format not support")
)

type ToolStateViewFile struct {
	Root string
}

type ToolViewFile struct {
	Path  string `json:"path,omitempty"`
	Page  int    `json:"page,omitempty"`
	Mode  string `json:"mode,omitempty"`
	Limit int    `json:"limit,omitempty"`

	// TODO: move to state and pool
	buf      bytes.Buffer
	sb       strings.Builder
	checkBuf []byte
}

func regToolFsViewFile(reg *Registry, conf *ToolStateViewFile) {
	returnImg := func(root *os.Root, fp string, ct string) ([]ChatMessagePart, bool) {
		img, err := root.ReadFile(fp)
		if err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		msg := buildMcpImgMsg(img, ct)
		return []ChatMessagePart{msg}, false
	}

	returnAsPng := func(root *os.Root, fp string) ([]ChatMessagePart, bool) {
		fd, err := root.Open(fp)
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
		img, _, err := image.Decode(fd)
		if err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		// encode to png
		var buf bytes.Buffer
		enc := base64.NewEncoder(base64.StdEncoding, &buf)
		if err := png.Encode(enc, img); err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		enc.Close()
		return []ChatMessagePart{
			{
				Type:     ChatMessagePartTypeMcpImage,
				Data:     buf.String(),
				MimeType: "image/png",
			},
		}, false
	}

	procPdf := func(root *os.Root, param *ToolViewFile) ([]ChatMessagePart, bool) {
		// for toc
		if param.Page == 0 {
			param.sb.Reset()
			err := readPdfToc(root, param.Path, &param.sb)
			out := param.sb.String()
			if len(out) == 0 {
				// out = "Warning: not bookmark/ToC in pdf\n"
				// return nil, ErrNoToC
				return []ChatMessagePart{
					{
						Type: ChatMessagePartTypeText,
						Text: "tool return Warning: not bookmark/ToC in pdf",
					},
				}, true
			}
			Vln(4, "[tool]ToolReadPdf - toc", len(out), err)
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: out,
				},
			}, false
		}

		param.Page -= 1 // 1-indexed => 0-indexed
		switch param.Mode {
		default:
			fallthrough
		case "text":
			param.sb.Reset()
			pageCount, err := readPdfAsText(root, param.Path, &param.sb, param.Page, param.Limit)
			Vln(4, "[tool]ToolReadPdf text", param.sb.Len(), pageCount, err)
			if param.sb.Len() == 0 {
				// out = " "
				// return nil, ErrNoTextContent
				return []ChatMessagePart{
					{
						Type: ChatMessagePartTypeText,
						Text: "tool return Warning: no data in this page, maybe image only",
					},
				}, false
			}
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("file=%v, page=%v, totalPage=%v", param.Path, param.Page+1, pageCount),
				},
				{
					Type: ChatMessagePartTypeText,
					Text: param.sb.String(),
				},
			}, false
		case "image":
			param.buf.Reset()
			enc := base64.NewEncoder(base64.StdEncoding, &param.buf)
			pageCount, err := readPdfAsImg(root, param.Path, enc, param.Page, param.Limit)
			enc.Close()
			Vln(4, "[tool]ToolReadPdf img", param.buf.Len(), pageCount, err)
			if param.buf.Len() == 0 {
				// out = " "
				// return nil, ErrNoContent
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
					Text: fmt.Sprintf("file=%v, page=%v, totalPage=%v", param.Path, param.Page+1, pageCount),
				},
				{
					Type:     ChatMessagePartTypeMcpImage,
					Data:     param.buf.String(),
					MimeType: "image/png",
				},
			}, err != nil
		}
	}

	reg.Register(&ToolDefine{
		Name: "view",
		Description: `讀取指定路徑的圖檔或pdf檔案內容。
圖檔會以多模態方式(multimodal)讀取
pdf檔:
page=0回傳ToC(目錄)。
指定頁數後會以指定的模式讀取，page從1開始計算。
遇到表格或圖片，建議以text跟image模式格讀取一次。`,
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"path": {
					Type: jsonschema.String,
				},
				"page": {
					Type:        jsonschema.Integer,
					Description: "the page to read, 1-indexed, 0 for ToC, for pdf only",
				},
				"mode": {
					Type:        jsonschema.String,
					Description: "for pdf files, retrieval text only or retrieval as image (multimodal) for table, figure, or image",
					Enum: []string{
						"text",
						"image",
					},
				},
			},
			Required: []string{"path"},
		},
	}, &sync.Pool{
		New: func() any {
			return &ToolViewFile{
				// checkBuf: make([]byte, 512),
			}
		},
	}, conf, func(req *ToolRequest, out *ToolViewFile) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if out.Page < 0 {
			return ErrBadParam
		}
		out.Path = toRootPath(out.Path)
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolViewFile, state *ToolStateViewFile) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][view_file]path=%v\n", param.Path)
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
		info, _ := fd.Stat()
		fSz := info.Size()

		ext := filepath.Ext(strings.ToLower(param.Path))
		isImg, _, isText := checkFileType(ext)
		if isImg && !isText {
			return returnImg(root, param.Path, "")
		}
		// read header for file type check
		var checkBuf [512]byte
		n, err := fd.Read(checkBuf[:512])
		if err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		ct := http.DetectContentType(checkBuf[:n])
		Vln(4, "[fs]view_file", fSz, ct, err)
		switch ct {
		case "image/bmp":
			fallthrough
		case "image/gif":
			fallthrough
		case "image/png":
			fallthrough
		case "image/jpeg":
			return returnImg(root, param.Path, ct)
		case "image/webp":
			return returnAsPng(root, param.Path)

		case "application/pdf":
			return procPdf(root, param)

			// TODO: convert ?
			// case "image/svg+xml":
		}
		// return nil, ErrBinaryData
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				Text: fmt.Sprintf("tool failed with the following error: %v", ErrNotSupportFormat.Error()),
			},
		}, true
	})
}
