package fetchers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

var (
	ErrInvalidURL        = errors.New("invalid URL provided")
	ErrHTTPRequestFailed = errors.New("HTTP request failed")
	ErrEmptyResponse     = errors.New("empty response received")
	ErrSSRFBlocked       = errors.New("request to private/internal network blocked")
)

// isBlockedHost returns true if the host resolves to a private, loopback, or
// cloud metadata IP range. Prevents SSRF attacks that probe internal networks
// or steal cloud credentials via the metadata endpoint.
func isBlockedHost(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ErrInvalidURL
	}
	host := u.Hostname()

	// Block cloud metadata endpoints by hostname.
	blockedHosts := []string{
		"169.254.169.254",
		"metadata.google.internal",
		"metadata.internal",
	}
	for _, b := range blockedHosts {
		if strings.EqualFold(host, b) {
			return ErrSSRFBlocked
		}
	}

	// Resolve and check IP ranges.
	ips, err := net.LookupHost(host)
	if err != nil {
		return nil // DNS failure will be caught by the HTTP client
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		// Block private ranges (10.x, 172.16-31.x, 192.168.x) and link-local.
		// Allow loopback (127.x) because the fetcher uses it for self-referencing
		// sample data URLs resolved via BROKOLI_SERVER_URL.
		if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return ErrSSRFBlocked
		}
	}
	return nil
}

type RESTFetcher struct {
	client *http.Client
}

type RequestOptions struct {
	Method  string
	Headers map[string]string
	Body    interface{}
	Timeout time.Duration
}

func (f *RESTFetcher) Fetch(source string, options map[string]interface{}) (*common.DataSet, error) {
	if source == "" {
		return nil, ErrInvalidURL
	}

	f.ensureClientInitialized(options)

	requestOptions := f.extractRequestOptions(options)

	responseBody, err := f.executeRequest(source, requestOptions)
	if err != nil {
		return nil, err
	}

	data, err := common.ParseJSONData(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return common.ConvertToDataSet(data), nil
}

func (f *RESTFetcher) ensureClientInitialized(options map[string]interface{}) {
	if f.client == nil {
		f.client = &http.Client{
			Timeout: 30 * time.Second, // Default timeout
		}
	}

	if timeout, ok := options["timeout"].(time.Duration); ok {
		f.client.Timeout = timeout
	}
}

func (f *RESTFetcher) extractRequestOptions(options map[string]interface{}) RequestOptions {
	requestOptions := RequestOptions{
		Method: "GET", // Default method
	}

	if methodOpt, ok := options["method"].(string); ok && methodOpt != "" {
		requestOptions.Method = methodOpt
	}

	switch h := options["headers"].(type) {
	case map[string]string:
		requestOptions.Headers = h
	case map[string]interface{}:
		requestOptions.Headers = make(map[string]string, len(h))
		for k, v := range h {
			requestOptions.Headers[k] = fmt.Sprintf("%v", v)
		}
	}

	if body, ok := options["body"]; ok {
		requestOptions.Body = body
	}

	if timeout, ok := options["timeout"].(time.Duration); ok {
		requestOptions.Timeout = timeout
	}

	return requestOptions
}

func (f *RESTFetcher) executeRequest(url string, options RequestOptions) ([]byte, error) {
	// Resolve relative URLs (e.g. /api/samples/data/file.csv) against the server
	if strings.HasPrefix(url, "/") {
		base := os.Getenv("BROKOLI_SERVER_URL")
		if base == "" {
			port := os.Getenv("PORT")
			if port == "" {
				port = "8080"
			}
			base = "http://127.0.0.1:" + port
		}
		url = strings.TrimRight(base, "/") + url
	}

	// SSRF protection: block requests to private networks and cloud metadata.
	if !strings.HasPrefix(url, "/") {
		if err := isBlockedHost(url); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSSRFBlocked, err)
		}
	}

	req, err := http.NewRequest(options.Method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	if options.Body != nil {
		switch b := options.Body.(type) {
		case string:
			req.Body = io.NopCloser(strings.NewReader(b))
		case []byte:
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
	}

	if options.Headers != nil {
		for key, value := range options.Headers {
			req.Header.Add(key, value)
		}
	}

	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrHTTPRequestFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status code %d", ErrHTTPRequestFailed, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, ErrEmptyResponse
	}

	return body, nil
}
