package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"mcp-vroot/jsonschema"
)

type EchoState struct {
}

type ToolEcho struct {
	Text string `json:"text,omitempty"`
}

func regToolEcho(reg *Registry) {
	reg.Register(&ToolDefine{
		Name:        "echo",
		Description: "Return the supplied text unchanged.",
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"text": {
					Type: jsonschema.String,
				},
			},
			Required: []string{
				"text",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolEcho)
		},
	}, &EchoState{}, func(req *ToolRequest, out *ToolEcho) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolEcho, state *EchoState) ([]ChatMessagePart, bool) {
		Vf(4, "[echo]pattern=%v\n", param.Text)
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				Text: param.Text,
			},
		}, false
	})
}
