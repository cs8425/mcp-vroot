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

type ToolStateRemove struct {
	Root string
}

type ToolRemove struct {
	Path string `json:"path,omitempty"`
}

func regToolFsRemove(reg *Registry, conf *ToolStateRemove) {
	reg.Register(&ToolDefine{
		Name:        "remove",
		Description: "remove a file or remove directory recursively.",
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
			return new(ToolRemove)
		},
	}, conf, func(req *ToolRequest, out *ToolRemove) error {
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
	}, func(ctx context.Context, req *ToolRequest, param *ToolRemove, state *ToolStateRemove) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][rm]path=%v\n", param.Path)
		root, err := os.OpenRoot(state.Root)
		if err != nil {
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		defer root.Close()

		if err := root.RemoveAll(param.Path); err != nil {
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
				Text: fmt.Sprintf("tool remove %v successfully.", param.Path),
			},
		}, false
	})
}
