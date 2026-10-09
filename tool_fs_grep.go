package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"mcp-vroot/jsonschema"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
)

type ToolStateGrep struct {
	Root          string
	MaxReturnLine int
	GetPdfPool    func() pdfium.Pool
}

type ToolGrep struct {
	Pattern string `json:"pattern,omitempty"`
	Path    string `json:"path,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func regToolFsGrep(reg *Registry, conf *ToolStateGrep) {
	if conf == nil {
		conf = &ToolStateGrep{
			Root:          "",
			MaxReturnLine: 150,
		}
	}
	if conf.MaxReturnLine <= 0 {
		conf.MaxReturnLine = 150
	}

	reg.Register(&ToolDefine{
		Name: "grep",
		// Description: "a tool like unix command 'grep', find any file contains keyword in a given file path",
		Description: "用正規表達式 (Regex)，在指定路徑及其所有子目錄中，遞迴搜尋符合pattern的檔案內容。pdf檔(as text mode)會回傳符合的頁數。",
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"pattern": {
					Type: jsonschema.String,
					// Description: "check if text contains in file, e.g. glTF\nsupport regex",
					Description: "需要尋找的字串或regex。支援完整的 Regex 語法，用於匹配特定的字串結構。最多回傳limit筆，預設100筆。",
				},
				"path": {
					Type:        jsonschema.String,
					Description: `find in sub path`,
					// Description: "搜尋的起始目錄路徑。系統將從此目錄開始，並會自動遍歷所有子目錄。",
				},
				"offset": {
					Type:        jsonschema.Integer,
					Description: `offset in lines from the beginning of match results`,
				},
				"limit": {
					Type:        jsonschema.Integer,
					Description: fmt.Sprintf(`how many lines should return, max %v lines`, conf.MaxReturnLine),
				},
			},
			Required: []string{"pattern"},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolGrep)
		},
	}, conf, func(req *ToolRequest, out *ToolGrep) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if out.Offset < 0 {
			return ErrBadParam
		}
		if out.Limit == 0 {
			out.Limit = 100 // conf.MaxReturnLine
		}
		if out.Limit < 0 {
			return ErrBadParam
		}
		if out.Limit > conf.MaxReturnLine {
			return ErrBadParam
		}
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolGrep, state *ToolStateGrep) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][grep]pattern=%v path=%v\n", param.Pattern, param.Path)
		idx := strings.Index(param.Path, "*")
		if idx >= 0 {
			param.Path = param.Path[:idx]
		}
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
		out, err := grep(root, param.Path, param.Pattern, param.Limit, param.Offset, state.GetPdfPool())
		Vln(4, "[fs]grep", len(out), err)
		if len(out) == 0 && err != nil {
			// return nil, err
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		if len(out) == 0 {
			// return nil, ErrNoContent
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: "tool return no data",
				},
			}, false
		}
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				Text: out,
			},
		}, false
	})
}

func grep(root *os.Root, fp string, pattern string, maxLine int, offsetLine int, pdfPool pdfium.Pool) (string, error) {
	if fp == "" {
		fp = "."
	}
	fp, _ = strings.CutSuffix(fp, "/")
	rx, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	_ = rx
	fp = filepath.Join(".", fp)
	fp = filepath.ToSlash(fp)
	idx := -offsetLine
	retLine := 0
	var sb strings.Builder
	err = fs.WalkDir(root.FS(), fp, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			Vln(4, "[grep]", root, path, err)
			return err
		}
		if d.IsDir() {
			// skip hidden files, eg: ".git/"
			if path != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		// Vln(4, "[grep]walk", path, err)
		var found, write int
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch ext {
		case ".pdf":
			found, write, err = grepPdfFile(pdfPool, root, path, rx, &sb, idx, maxLine-retLine)
		default:
			found, write, err = grepFile(root, path, rx, &sb, idx, maxLine-retLine)
		}
		// found, write, err := grepFile(root, path, rx, &sb, idx, maxLine-retLine)
		if err != nil {
			// Vln(4, "[grep]grepFile", root, fp, err)
			// return err
			fmt.Fprintf(&sb, "grep: %v err: %v\n", path, err)
			return nil // skip
		}
		idx += found
		retLine += write
		if retLine >= maxLine {
			fmt.Fprintf(&sb, "return early due to reach max line\n")
			return fs.SkipAll
		}
		return nil
	})
	return sb.String(), err
}

func grepFile(root *os.Root, fp string, rx *regexp.Regexp, sb *strings.Builder, baseOffset int, maxRetCount int) (int, int, error) {
	fp = filepath.ToSlash(fp)
	fd, err := root.Open(fp)
	if err != nil {
		Vln(4, "[grepFile]", root, fp, err)
		return 0, 0, err
	}
	defer fd.Close()
	scanner := bufio.NewScanner(fd)
	idx := 1
	found, write := 0, 0
	for scanner.Scan() {
		buf := scanner.Bytes()
		if bytes.Contains(buf, []byte{0x00}) {
			if rx.MatchString(string(buf)) {
				found += 1
				if found+baseOffset > 0 && write < maxRetCount {
					fmt.Fprintf(sb, "%v:binary match\n", fp)
					write += 1
				}
			}
			// skip, seems like binary file
			return found, write, nil
		}
		line := string(buf)
		if rx.MatchString(line) {
			// fmt.Println(line)
			found += 1
			if found+baseOffset > 0 && write < maxRetCount {
				fmt.Fprintf(sb, "%v:%v:%v\n", fp, idx, line)
				write += 1
			}
		}
		idx += 1
	}
	return found, write, nil
}

func grepPdfFile(pdfPool pdfium.Pool, root *os.Root, fp string, rx *regexp.Regexp, sb *strings.Builder, baseOffset int, maxRetCount int) (int, int, error) {
	instance, doc, clsFn, err := openPdf(pdfPool, root, fp)
	if err != nil {
		Vln(4, "[grepPdfFile]", root, fp, err)
		return 0, 0, err
	}
	defer clsFn()

	pageCount, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return 0, 0, err
	}
	maxPage := pageCount.PageCount

	found, write := 0, 0
	req := &requests.GetPageText{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{
				Document: doc.Document,
				Index:    0,
			},
		}, // The page to render, 0-indexed.
	}
	for i := 0; i < maxPage; i += 1 {
		req.Page.ByIndex.Index = i
		pageText, err := instance.GetPageText(req)
		if err != nil {
			fmt.Fprintf(sb, "%v:p.%v:%v\n", fp, i+1, err)
			continue
		}
		for line := range strings.Lines(pageText.Text) {
			// 'line' includes the trailing newline character
			if rx.MatchString(line) {
				found += 1
				if found+baseOffset > 0 && write < maxRetCount {
					fmt.Fprintf(sb, "%v:p.%v:%v\n", fp, pageText.Page+1, strings.TrimRight(line, "\r\n"))
					write += 1
				}
			}
			if write >= maxRetCount {
				break
			}
		}
		if write >= maxRetCount {
			break
		}
	}
	return found, write, nil
}
