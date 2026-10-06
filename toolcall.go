package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"mcp-vroot/jsonschema"
)

var (
	ErrBadParam  = errors.New("bad parameter")
	ErrDuplicate = errors.New("duplicate tool id")
)

type Registry struct {
	// mu           sync.RWMutex
	list         []*ToolDefine
	toolHandlers map[string]HandlerFunc
	// tools        map[string]*Tool

	toolPrefix string
}

func NewRegistry(toolPrefix string) *Registry {
	return &Registry{
		toolPrefix: toolPrefix,

		// tools:        make(map[string]*Tool),
		list:         make([]*ToolDefine, 0, 32),
		toolHandlers: make(map[string]HandlerFunc),
	}
}

type Tool struct {
	Def     *ToolDefine
	Handler HandlerFunc
}

type ToolDefine struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// InputSchema map[string]any `json:"inputSchema"`
	InputSchema jsonschema.Definition `json:"inputSchema"`
}

type ToolRequest struct {
	Meta      requestMeta     `json:"_meta"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// Arguments map[string]any `json:"arguments,omitempty"`
}

type ToolResult struct {
	Content []ChatMessagePart `json:"content,omitempty"`
	IsError bool              `json:"isError,omitempty"`
}

type HandlerFunc func(context.Context, *ToolRequest) ([]ChatMessagePart, bool)

func (reg *Registry) Register[T any, R any](
	def *ToolDefine,
	pool *sync.Pool,
	state *R,
	parser func(*ToolRequest, *T) error,
	handler func(context.Context, *ToolRequest, *T, *R) ([]ChatMessagePart, bool),
) error {
	if def == nil || pool == nil || parser == nil || handler == nil {
		return ErrBadParam
	}

	// reg.mu.Lock()
	// defer reg.mu.Unlock()

	// update prefix
	def.Name = reg.toolPrefix + def.Name

	id := def.Name
	if _, exists := reg.toolHandlers[id]; exists {
		// panic(fmt.Sprintf("duplicate tool id: %v", id))
		return ErrDuplicate
	}
	reg.list = append(reg.list, def)

	reg.toolHandlers[id] = func(ctx context.Context, req *ToolRequest) ([]ChatMessagePart, bool) {
		obj := pool.Get().(*T)

		// reset
		var zero T
		*obj = zero

		err := parser(req, obj)
		if err != nil {
			pool.Put(obj)
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf("parsing arguments failed: %v", err.Error()),
				},
			}, true
		}

		out, isErr := handler(ctx, req, obj, state)

		pool.Put(obj)
		return out, isErr
	}
	return nil
}

func (reg *Registry) Dispatch(ctx context.Context, req *ToolRequest) (*ToolResult, error) {
	// reg.mu.RLock()
	// defer reg.mu.RUnlock()

	h, ok := reg.toolHandlers[req.Name]
	if !ok {
		return nil, fmt.Errorf("unknown tool id: %v", req.Name)
	}
	res, isErr := h(ctx, req)
	return &ToolResult{
		Content: res,
		IsError: isErr,
	}, nil
}

func (reg *Registry) GetToolList() []*ToolDefine {
	return reg.list
}
