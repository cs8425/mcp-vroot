package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// modify from "github.com/sashabaranov/go-openai"
// LICENSE Apache License 2.0
type ChatMessagePartType string

const (
	ChatMessagePartTypeText     ChatMessagePartType = "text"
	ChatMessagePartTypeImageURL ChatMessagePartType = "image_url"

	// for mcp return
	ChatMessagePartTypeMcpImage ChatMessagePartType = "image"
)

type ChatMessagePart struct {
	Type     ChatMessagePartType `json:"type,omitempty"`
	Text     string              `json:"text,omitempty"`
	ImageURL string              `json:"image_url,omitempty"`

	// for mcp return
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

func (cp *ChatMessagePart) MarshalJSONTo(enc *jsontext.Encoder) error {
	switch cp.Type {
	case ChatMessagePartTypeText:
	case ChatMessagePartTypeImageURL:
	case ChatMessagePartTypeMcpImage:
	default:
		return ErrBadParam
	}
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	if err := enc.WriteToken(jsontext.String("type")); err != nil {
		return err
	}
	if err := enc.WriteToken(jsontext.String(string(cp.Type))); err != nil {
		return err
	}
	switch cp.Type {
	case ChatMessagePartTypeText:
		if err := enc.WriteToken(jsontext.String("text")); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String(cp.Text)); err != nil {
			return err
		}
	case ChatMessagePartTypeImageURL:
		if err := enc.WriteToken(jsontext.String("image_url")); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.BeginObject); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String("url")); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String(cp.ImageURL)); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.EndObject); err != nil {
			return err
		}
	case ChatMessagePartTypeMcpImage:
		if err := enc.WriteToken(jsontext.String("mimeType")); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String(cp.MimeType)); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String("data")); err != nil {
			return err
		}
		if err := enc.WriteToken(jsontext.String(cp.Data)); err != nil {
			return err
		}
	default:
	}
	if err := enc.WriteToken(jsontext.EndObject); err != nil {
		return err
	}
	return nil
}

func buildImgMsg(imgBuf []byte, mimeType string) ChatMessagePart {
	if len(mimeType) == 0 {
		mimeType = http.DetectContentType(imgBuf)
	}
	base64Encoded := base64.StdEncoding.EncodeToString(imgBuf)
	return ChatMessagePart{
		Type:     ChatMessagePartTypeImageURL,
		ImageURL: fmt.Sprintf("data:%s;base64,%s", mimeType, base64Encoded),
	}
}

func buildMcpImgMsg(imgBuf []byte, mimeType string) ChatMessagePart {
	if len(mimeType) == 0 {
		mimeType = http.DetectContentType(imgBuf)
	}
	base64Encoded := base64.StdEncoding.EncodeToString(imgBuf)
	return ChatMessagePart{
		Type:     ChatMessagePartTypeMcpImage,
		Data:     base64Encoded,
		MimeType: mimeType,
	}
}

func buildImgMsgByStream(imgFd io.Reader, mimeType string, sb *strings.Builder) (*ChatMessagePart, error) {
	if sb == nil {
		sb = &strings.Builder{}
	}

	var reader io.Reader = imgFd

	// peak mime
	if len(mimeType) == 0 {
		prefix := make([]byte, 512)
		n, err := imgFd.Read(prefix)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("failed to read prefix: %w", err)
		}

		if n > 0 {
			mimeType = http.DetectContentType(prefix[:n])
			reader = io.MultiReader(bytes.NewReader(prefix[:n]), imgFd)
		} else {
			reader = io.MultiReader(bytes.NewReader([]byte{}), imgFd)
		}
	}
	_, err := fmt.Fprintf(sb, "data:%s;base64,", mimeType)
	if err != nil {
		return nil, err
	}
	encoder := base64.NewEncoder(base64.StdEncoding, sb)
	_, err = io.Copy(encoder, reader)
	if err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return &ChatMessagePart{
		Type:     ChatMessagePartTypeImageURL,
		ImageURL: sb.String(),
	}, nil
}
