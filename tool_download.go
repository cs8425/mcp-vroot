package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mcp-vroot/jsonschema"
	"os"
	"sync"
)

type ToolStateDownload struct {
	Root      string
	AllowList []string
}

type ToolDownload struct {
	Url     string `json:"url,omitempty"`
	DstPath string `json:"dst,omitempty"`
}

func regToolFsDownload(reg *Registry, conf *ToolStateDownload) {
	reg.Register(&ToolDefine{
		Name: "download",
		Description: `Download a file from an allowed HTTP/HTTPS URL and save it to the specified filesystem path.

Only domains configured in the allowlist may be accessed.
The downloaded content is written exactly as received; no text encoding
or content transformation is performed.

The result reports when success:
- the downloaded byte count
- SHA-256 hexadecimal checksum
- SHA-384 Subresource Integrity (SRI) value suitable for use in HTML <script> or <link> elements.

No partial file left if download failed.`,
		InputSchema: jsonschema.Definition{
			Type: jsonschema.Object,
			Properties: map[string]jsonschema.Definition{
				"url": {
					Type:        jsonschema.String,
					Description: "HTTPS URL of the file to download.",
				},
				"dst": {
					Type:        jsonschema.String,
					Description: "Destination path in the configured filesystem root.",
				},
			},
			Required: []string{
				"url",
				"dst",
			},
		},
	}, &sync.Pool{
		New: func() any {
			return new(ToolDownload)
		},
	}, conf, func(req *ToolRequest, out *ToolDownload) error {
		rd := bytes.NewBuffer(req.Arguments)
		err := json.NewDecoder(rd).Decode(out)
		if err != nil {
			return err
		}
		if len(out.Url) <= 0 {
			return ErrBadParam
		}
		if len(out.DstPath) <= 0 {
			return ErrBadParam
		}
		out.DstPath = toRootPath(out.DstPath)
		return nil
	}, func(ctx context.Context, req *ToolRequest, param *ToolDownload, state *ToolStateDownload) ([]ChatMessagePart, bool) {
		Vf(4, "[fs][download]path=%v, url=%v\n", param.DstPath, param.Url)
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

		var ck Checksums
		var hints string
		var dlErr error
		err = atomicWriteFile(root, param.DstPath, 0o644, func(fd *os.File) error {
			ck, hints, dlErr = Download(ctx, &DownloadeRequest{
				Url:       param.Url,
				OutputFd:  fd,
				AllowList: state.AllowList,
			})
			if dlErr != nil {
				return dlErr
			}
			return nil
		})
		// Vf(4, "[fs][download]dlErr=%v, err=%v, ck=%v, hints=%v\n", dlErr, err, ck, hints)
		if err != nil {
			return []ChatMessagePart{
				{
					Type: ChatMessagePartTypeText,
					Text: fmt.Sprintf(`%v => %v download failed: %v`, param.Url, param.DstPath, hints),
				},
			}, true
		}
		_ = ck
		return []ChatMessagePart{
			{
				Type: ChatMessagePartTypeText,
				Text: fmt.Sprintf("Save to `%v` successfully.%v",
					param.DstPath,
					hints,
				),
			},
		}, false
	})
}
