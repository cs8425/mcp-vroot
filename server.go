package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"slices"
)

type Config struct {
	Bind        string           `json:"bind,omitempty"`
	AllowOrigin []string         `json:"allow-origin,omitempty"`
	WorkSpace   *WorkSpaceConfig `json:"fs,omitempty"`

	DownloadAllowDomain []string `json:"download-allow-domain,omitempty"`

	ServerName string `json:"server-name,omitempty"`
	ToolPrefix string `json:"tool-prefix,omitempty"`
}

type WorkSpaceConfig struct {
	Path       string   `json:"path,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	AllowTools []string `json:"allow-tools,omitempty"`
	DenyTools  []string `json:"deny-tools,omitempty"`
}

const (
	ServerVersion = "0.1.0"
)

var (
	configFile = flag.String("c", "config.json", "config file")
	address    = flag.String("l", "", "bind streamable http mcp server at address")
)

func parseConfig(fp string) (*Config, error) {
	fd, err := os.Open(fp)
	if err != nil {
		return nil, err
	}
	defer fd.Close()

	conf := &Config{}
	err = json.NewDecoder(fd).Decode(&conf)
	if err != nil {
		return nil, err
	}
	return conf, nil
}

func main() {
	flag.Parse()

	conf, err := parseConfig(*configFile)
	if err != nil {
		conf = &Config{
			Bind: "127.0.0.1:8765",
			AllowOrigin: []string{
				"*", // default allow all
			},
			WorkSpace: &WorkSpaceConfig{
				Path: "./workspace",
				Mode: "ro",
			},
		}
	}
	if conf.ServerName == "" {
		conf.ServerName = "mcp-vroot"
	}
	if conf.DownloadAllowDomain == nil { // only not set, empty means don't have any allowed
		// set default
		conf.DownloadAllowDomain = []string{
			"cdn.jsdelivr.net",
			"unpkg.com",
			"esm.unpkg.com",
			"cdnjs.cloudflare.com",
			"esm.sh",
		}
	}
	if *address != "" || conf.Bind == "" {
		conf.Bind = *address
	}

	// Adjust this config to the actual origin of your llama.cpp WebUI.
	allowOrigin := make(map[string]bool, len(conf.AllowOrigin))
	for _, origin := range conf.AllowOrigin {
		allowOrigin[origin] = true
	}

	Vln(2, "[conf]", conf, allowOrigin, conf.WorkSpace)

	reg := NewRegistry(conf.ToolPrefix)

	// register tools
	// regToolEcho(reg)
	regToolFsList(reg, &ToolListState{
		Root:    conf.WorkSpace.Path,
		MaxLmit: 250,
	})
	regToolFsGrep(reg, &ToolStateGrep{
		Root:          conf.WorkSpace.Path,
		MaxReturnLine: 250,
	})
	regToolFsReadFile(reg, &ToolStateReadFile{
		Root:          conf.WorkSpace.Path,
		MaxLines:      150,
		MaxBufferSize: 1024,
	})
	regToolFsViewFile(reg, &ToolStateViewFile{
		Root: conf.WorkSpace.Path,
	})

	// write
	if conf.WorkSpace.Mode == "rw" {
		regToolFsWriteFile(reg, &ToolStateWriteFile{
			Root: conf.WorkSpace.Path,
		})
		regToolFsEditFile(reg, &ToolStateEditFile{
			Root: conf.WorkSpace.Path,
		})
		regToolFsMkDir(reg, &ToolStateMkDir{
			Root: conf.WorkSpace.Path,
		})
		regToolFsCopy(reg, &ToolStateCopy{
			Root: conf.WorkSpace.Path,
		})
		regToolFsMove(reg, &ToolStateMove{
			Root: conf.WorkSpace.Path,
		})
		regToolFsRemove(reg, &ToolStateRemove{
			Root: conf.WorkSpace.Path,
		})

		// TODO: more flag?
		allowDomains := conf.DownloadAllowDomain
		if slices.Contains(conf.DownloadAllowDomain, "*") {
			allowDomains = nil
		}
		regToolFsDownload(reg, &ToolStateDownload{
			Root:      conf.WorkSpace.Path,
			AllowList: allowDomains,
		})
	}

	s := &McpServer{
		Registry: reg,

		Name:    conf.ServerName,
		Version: ServerVersion,

		Origin: allowOrigin,
	}

	Vln(2, "[bind]", conf.Bind)
	// For a local setup, keep the MCP endpoint on localhost unless you explicitly need network access.
	if err := http.ListenAndServe(conf.Bind, s.Handler()); err != nil {
		Vln(2, "[bind]err", err)
	}
	Vln(2, "[exit]", conf.Bind)
}
