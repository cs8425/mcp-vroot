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

type ToolStateMove struct {
	Root string
}

type ToolMove struct {
	SrcPath string `json:"src,omitempty"`
	DstPath string `json:"dst,omitempty"`
}

func regToolFsMove(reg *Registry, conf *ToolStateMove) {
	reg.Register(&ToolDefine{
		Name:        "move",
		Description: "move or rename files and directories.",
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"src": {
					Type: jsonschema.String,
				},
				"dst": {
					Type: jsonschema.String,
				},
			},
			Required: []string{
				"src",
				"dst",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolMove)
		},
	}, conf, func(req *ToolRequest, out *ToolMove) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if len(out.SrcPath) <= 0 {
			return ErrBadParam
		}
		out.SrcPath = toRootPath(out.SrcPath)
		if len(out.DstPath) <= 0 {
			return ErrBadParam
		}
		out.DstPath = toRootPath(out.DstPath)
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolMove, state *ToolStateMove) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][mv]src=%v, dst=%v\n", param.SrcPath, param.DstPath)
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

		// Rename renames (moves) oldpath to newpath.
		// If newpath already exists and is not a directory, Rename replaces it. << lost data
		// If newpath already exists and is a directory, Rename returns an error.
		// so check newpath first
		_, err = root.Lstat(param.DstPath)
		if err == nil {
			// exist => error
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: destination already exists: %v", param.DstPath),
				},
			}, true
		}
		if !os.IsNotExist(err) {
			// not "not exist" => error
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("tool failed with the following error: %v", err.Error()),
				},
			}, true
		}
		err = root.Rename(param.SrcPath, param.DstPath)
		if err != nil {
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
				Text: fmt.Sprintf("rename %v => %v successfully.", param.SrcPath, param.DstPath),
			},
		}, false
	})
}
