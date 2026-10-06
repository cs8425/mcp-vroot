package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"os"
	"sync"

	"mcp-vroot/jsonschema"
)

type ToolListState struct {
	Root    string
	MaxLmit int
}

type ToolList struct {
	Path    string `json:"path,omitempty"`
	Depth   int    `json:"depth,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func regToolFsList(reg *Registry, conf *ToolListState) {
	if conf == nil {
		conf = &ToolListState{
			Root:    "",
			MaxLmit: 250,
		}
	}
	if conf.MaxLmit <= 0 {
		conf.MaxLmit = 250
	}

	reg.Register(&ToolDefine{
		Name:        "list",
		Description: "列出或尋找指定路徑的底下N層內，符合條件名稱的檔案，預設2層。",
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"path": {
					Type: jsonschema.String,
				},
				"pattern": {
					Type:        jsonschema.String,
					Description: "需要尋找的關鍵字或正則表達式模式。支援完整的 Regex 語法，用於匹配特定的檔名字串結構或組合。",
				},
				"depth": {
					Type:        jsonschema.Integer,
					Description: "recursively find files up to N layers deep, default 2, negative number (-1) means no limit",
				},
				"offset": {
					Type:        jsonschema.Integer,
					Description: `offset from the beginning of match results`,
				},
				"limit": {
					Type: jsonschema.Integer,
					Description: fmt.Sprintf(`how many results should return, max %v results`,
						conf.MaxLmit,
					),
				},
			},
			Required: []string{
				"path",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolList)
		},
	}, conf, func(req *ToolRequest, out *ToolList) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if out.Pattern == "" { // set default only for no pattern
			if out.Depth == 0 {
				out.Depth = 2
			}
		}
		if out.Offset < 0 {
			return ErrBadParam
		}
		if out.Limit == 0 {
			out.Limit = conf.MaxLmit
		}
		if out.Limit < 0 {
			return ErrBadParam
		}
		if out.Limit > conf.MaxLmit {
			return ErrBadParam
		}
		if len(out.Path) <= 0 {
			return ErrBadParam
		}
		out.Path = toRootPath(out.Path)
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolList, state *ToolListState) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][list]path=%v, depth=%v, pattern=%v, limit=%v, offset=%v\n", param.Path, param.Depth, param.Pattern, param.Limit, param.Offset)
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
		out, err := fsFind(root, param.Path, param.Pattern, param.Depth, param.Limit, param.Offset, nil)
		Vln(4, "[fs]list", len(out), err)
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

// maxDepth <= 0: no depth limit
func fsFind(root *os.Root, fp string, pattern string, maxDepth int, limit int, offset int, sb *strings.Builder) (string, error) {
	if fp == "" {
		fp = "."
	}
	fp, _ = strings.CutSuffix(fp, "/")

	var rx *regexp.Regexp
	var err error
	if pattern != "" {
		rx, err = regexp.Compile(pattern)
		if err != nil {
			return "", err
		}
	}

	fp = filepath.Join(".", fp)
	fp = filepath.ToSlash(fp)

	if sb == nil {
		sb = &strings.Builder{}
	}
	foundCount := 0   // 匹配成功的總計數（用於 Offset 判斷）
	writtenCount := 0 // 實際寫入輸出的計數（用於 Limit 判斷）

	err = fs.WalkDir(root.FS(), fp, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			Vln(4, "[find]", root, path, err)
			return err
		}

		rel, _ := filepath.Rel(fp, path)
		depth := 0
		if rel != "." {
			depth = len(strings.Split(rel, string(filepath.Separator)))
		}

		match := true
		if rx != nil {
			match = rx.MatchString(path)
		}

		if match {
			foundCount++
			if foundCount > offset {
				if writtenCount < limit {
					if d.IsDir() {
						fmt.Fprintf(sb, "%v/\n", path)
					} else {
						sz := int64(-1)
						if info, err := d.Info(); err == nil {
							sz = info.Size()
						}
						// fmt.Fprintf(sb, "%v\n", path)
						if sz >= 0 {
							fmt.Fprintf(sb, "%v (%vbytes)\n", path, sz)
						} else {
							fmt.Fprintf(sb, "%v (???bytes)\n", path)
						}
					}
					writtenCount++
				}
				if writtenCount >= limit {
					fmt.Fprintf(sb, "return early due to reach max limit, using offset to get more line\n")
					return fs.SkipAll
				}
			}
		}

		if d.IsDir() {
			if path != fp && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			if maxDepth > 0 && depth >= maxDepth {
				return fs.SkipDir
			}
		}
		return nil
	})
	return sb.String(), err
}
