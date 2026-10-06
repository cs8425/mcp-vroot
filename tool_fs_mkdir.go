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

type ToolStateMkDir struct {
	Root string
}

type ToolMkDir struct {
	Path string `json:"path,omitempty"`
}

func regToolFsMkDir(reg *Registry, conf *ToolStateMkDir) {
	reg.Register(&ToolDefine{
		Name:        "mkdir",
		Description: "Create a new directory or ensure a directory exists.",
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"path": {
					Type: jsonschema.String,
				},
			},
			Required: []string{
				"path",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolMkDir)
		},
	}, conf, func(req *ToolRequest, out *ToolMkDir) error {
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
	}, func(ctx context.Context, req *ToolRequest, param *ToolMkDir, state *ToolStateMkDir) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][mkdir]path=%v\n", param.Path)
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

		// TODO: config for file perm
		if err := root.MkdirAll(param.Path, 0o755); err != nil {
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
				Text: fmt.Sprintf("directory %v create successfully or already exists.", param.Path),
			},
		}, false
	})
}
