package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotInAllowList  = errors.New("domain not in allow list")
	ErrTooManyRedirect = errors.New("too many redirect, stopped after 10 redirects")
)

// Request describes a download request.
//
// Output options, in priority order:
//
//  1. OutputFile: preferred when you already have *os.File.
//     The file must be opened for writing. This package does not close it.
//  2. OutputPath: this package creates the file and closes it after download.
//  3. OutputFD: raw fd, for example int(f.Fd()) from *os.File.
//     The fd must be > 0. This package does not close it.
type DownloadeRequest struct {
	Url      string
	OutputFd *os.File

	AllowList []string // nil == all allow

	IgnoreSelfSignedCert bool
	Headers              http.Header
	Timeout              time.Duration

	// HTTPClient is optional. If provided, this package clones basic client
	// fields and does not mutate the original client.
	HTTPClient *http.Client
}

// Downloader is the protocol extension interface.
// Implement this interface to add schemes such as sftp://.
type Downloader interface {
	Download(ctx context.Context, req *DownloadeRequest) (Checksums, string, error)
}

var (
	dlSchemeMu        sync.RWMutex
	dlSchemeFactories = map[string]func(*DownloadeRequest) (Downloader, error){
		"http":  newHTTPDownloader,
		"https": newHTTPDownloader,
	}
)

// RegisterScheme registers a new protocol scheme, for example "sftp".
func RegisterScheme(scheme string, factory func(*DownloadeRequest) (Downloader, error)) {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" || factory == nil {
		return
	}

	dlSchemeMu.Lock()
	defer dlSchemeMu.Unlock()
	dlSchemeFactories[scheme] = factory
}

// Download dispatches the request by URL scheme.
//
// It returns:
//   - sums:   checksums, meaningful only when err == nil.
//   - status: human-readable execution result or error summary.
//   - err:    programmatic error.
func Download(ctx context.Context, req *DownloadeRequest) (Checksums, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req == nil {
		return Checksums{}, "nil request", ErrBadParam
	}

	u, err := url.Parse(req.Url)
	if err != nil {
		return Checksums{}, fmt.Sprintf("invalid url: %v", err), err
	}

	scheme := strings.ToLower(u.Scheme)

	dlSchemeMu.RLock()
	factory, ok := dlSchemeFactories[scheme]
	dlSchemeMu.RUnlock()

	if !ok {
		err := fmt.Errorf("unsupported scheme %q", u.Scheme)
		return Checksums{}, "unsupported scheme: " + u.Scheme, err
	}

	d, err := factory(req)
	if err != nil {
		return Checksums{}, "init downloader failed: " + err.Error(), err
	}

	return d.Download(ctx, req)
}

func newHTTPDownloader(_ *DownloadeRequest) (Downloader, error) {
	return &httpDownloader{}, nil
}

// httpDownloader implements HTTP/HTTPS downloads.
type httpDownloader struct{}

// Download performs an HTTP/HTTPS GET request and streams the body to output.
func (h *httpDownloader) Download(ctx context.Context, req *DownloadeRequest) (Checksums, string, error) {
	client, err := buildHTTPClient(req)
	if err != nil {
		return Checksums{}, "build http client failed: " + err.Error(), err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.Url, nil)
	if err != nil {
		return Checksums{}, "build request failed: " + err.Error(), err
	}
	if req.AllowList != nil {
		if !slices.Contains(req.AllowList, httpReq.URL.Host) {
			return Checksums{}, ErrNotInAllowList.Error(), ErrNotInAllowList
		}
	}

	// Set a default User-Agent if the caller did not provide one.
	if _, ok := httpReq.Header["User-Agent"]; !ok {
		httpReq.Header.Set("User-Agent", "go-downloader/1.0")
	}

	// Apply caller-provided headers.
	for k, vs := range req.Headers {
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}

	// Record redirects for the final status string.
	var redirects []string
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return ErrTooManyRedirect
		}
		if req.AllowList != nil {
			if !slices.Contains(req.AllowList, r.URL.Host) {
				return ErrNotInAllowList
			}
		}
		if len(via) == 0 {
			redirects = append(redirects, "redirect to "+r.URL.String())
		} else {
			redirects = append(redirects, fmt.Sprintf("redirect %d to %s", len(via), r.URL.String()))
		}
		return nil
	}
	Vf(5, "[httpDownloader][Download]req=%v\n", httpReq)
	resp, err := client.Do(httpReq)
	if err != nil {
		return Checksums{}, "request failed: " + err.Error(), err
	}
	defer resp.Body.Close()
	Vf(5, "[httpDownloader][Download]res=%v\n", resp)

	// Treat non-2xx responses as errors.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("unexpected status %s", resp.Status)
		return Checksums{}, "unexpected status: " + resp.Status, err
	}

	// Prepare output writer.
	outFd := req.OutputFd

	// Stream data to output while computing hashes.
	sc := NewStreamingChecksums(outFd)

	n, err := io.Copy(sc, resp.Body)
	if err != nil {
		return Checksums{}, "write output failed: " + err.Error(), err
	}

	sums := sc.Sum()

	// status := fmt.Sprintf(
	// 	"ok: wrote %d bytes, final_url=%s, status=%s",
	// 	n,
	// 	resp.Request.URL,
	// 	resp.Status,
	// )
	// if len(redirects) > 0 {
	// 	status += "; " + strings.Join(redirects, "; ")
	// }
	ct := resp.Header.Get("Content-Type")
	if len(ct) == 0 {
		ct = "(no Content-Type header)"
	}

	status := fmt.Sprintf(`
url: %v
Content-Type: %v
size: %v bytes

sha256: %v
SRI: %v`,
		req.Url,
		ct,
		n,
		sums.SHA256Hex,
		sums.SHA384Integrity,
	)

	return sums, status, nil
}

// buildHTTPClient builds an HTTP client from request options.
func buildHTTPClient(req *DownloadeRequest) (*http.Client, error) {
	var client *http.Client

	if req.HTTPClient != nil {
		// Clone basic fields to avoid mutating the caller's client.
		client = &http.Client{
			Transport: req.HTTPClient.Transport,
			Timeout:   req.HTTPClient.Timeout,
			Jar:       req.HTTPClient.Jar,
		}
	} else {
		client = &http.Client{}
	}

	// Apply request timeout if the client does not already have one.
	if req.Timeout > 0 && client.Timeout == 0 {
		client.Timeout = req.Timeout
	}

	// Configure TLS when self-signed certificates should be accepted.
	if req.IgnoreSelfSignedCert {
		var tr *http.Transport

		switch t := client.Transport.(type) {
		case *http.Transport:
			tr = t.Clone()
		case nil:
			tr = &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				MaxIdleConns:    100,
				IdleConnTimeout: 90 * time.Second,
			}
		default:
			return nil, fmt.Errorf(
				"IgnoreSelfSignedCert only supports nil or *http.Transport, got %T",
				client.Transport,
			)
		}

		if tr.TLSClientConfig == nil {
			tr.TLSClientConfig = &tls.Config{}
		}
		tr.TLSClientConfig.InsecureSkipVerify = true
		client.Transport = tr
	}

	return client, nil
}
