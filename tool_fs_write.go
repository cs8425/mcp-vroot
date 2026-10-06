package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"mcp-vroot/jsonschema"
)

type ToolWriteBehavior string

const (
	ToolWriteReplace    ToolWriteBehavior = "replace"
	ToolWriteAppend     ToolWriteBehavior = "append"
	ToolWriteCreateOnly ToolWriteBehavior = "create"
)

type ToolStateWriteFile struct {
	Root string
}

type ToolWriteFile struct {
	Path          string            `json:"path,omitempty"`
	Content       string            `json:"content,omitempty"`
	ContentFormat string            `json:"content_format,omitempty"`
	WriteBehavior ToolWriteBehavior `json:"write_behavior,omitempty"`
}

func regToolFsWriteFile(reg *Registry, conf *ToolStateWriteFile) {
	reg.Register(&ToolDefine{
		Name:        "write",
		Description: "write data to a file",
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"path": {
					Type: jsonschema.String,
				},
				"content": {
					Type: jsonschema.String,
				},
				"write_behavior": {
					Type: jsonschema.String,
					Description: `How to write the file. (default: replace)
replace:
	create if absent, otherwise replace

append:
	create if absent, otherwise append

create:
	create only, fail if exists`,
					Enum: []string{
						"replace",
						"append",
						"create",
					},
				},
				// "content_format": {
				// 	Type:        jsonschema.String,
				// 	Description: "How content is represented. text is UTF-8 text; hex represent raw binary data. default: text",
				// 	Enum: []string{
				// 		"text",
				// 		"hex",
				// 	},
				// },
			},
			Required: []string{
				"path",
				"content",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolWriteFile)
		},
	}, conf, func(req *ToolRequest, out *ToolWriteFile) error {
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
	}, func(ctx context.Context, req *ToolRequest, param *ToolWriteFile, state *ToolStateWriteFile) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][write_file]path=%v, format=%v, behavior=%v, length=%v\n", param.Path, param.ContentFormat, param.WriteBehavior, len(param.Content))
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

		flags := os.O_WRONLY
		switch param.WriteBehavior {
		default:
			fallthrough
		case ToolWriteReplace:
			// TODO: write to tmp file and replace when finish
			flags |= os.O_CREATE | os.O_TRUNC
		case ToolWriteAppend:
			flags |= os.O_CREATE | os.O_APPEND
		case ToolWriteCreateOnly:
			flags |= os.O_CREATE | os.O_EXCL
		}

		// TODO: config for file perm
		fd, err := root.OpenFile(param.Path, flags, 0o644)
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

		var n64 int64
		switch param.ContentFormat {
		// case "hex":
		// 	rd := bytes.NewBufferString(param.Content)
		// 	dec := hex.NewDecoder(rd)
		// 	n64, err = io.Copy(fd, dec)
		default:
			fallthrough
		case "text":
			n, err0 := fd.WriteString(param.Content)
			n64 = int64(n)
			err = err0
		}

		if err != nil {
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool write %v bytes but failed with the following error: %v", n64, err.Error()),
				},
			}, true
		}
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				Text: fmt.Sprintf("tool write %v bytes successfully.", n64),
			},
		}, false
	})
}
