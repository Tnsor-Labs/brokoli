package fetchers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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

// maxPaginationPages is a hard safety cap on the number of pages a
// pagination strategy will fetch, independent of any max_records/end_flag
// config. It exists purely to prevent an infinite loop against a
// misbehaving or misconfigured API (e.g. a next_link/cursor that never
// terminates) — legitimate paginated sources are expected to terminate via
// their own max_records/end_flag/total_pages/empty-page signal long before
// this is reached.
const maxPaginationPages = 100000

// defaultPageRetries is how many times a single failed page fetch is
// retried before the whole source_api node fails, when the node config
// doesn't specify max_retries. This is deliberately conservative — page-level
// retry here is a best-effort convenience, not a durable execution-attempt
// mechanism (see PR notes on scope).
const defaultPageRetries = 2

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
	Params  map[string]string
	Timeout time.Duration
}

func (f *RESTFetcher) Fetch(source string, options map[string]interface{}) (*common.DataSet, error) {
	if source == "" {
		return nil, ErrInvalidURL
	}

	f.ensureClientInitialized(options)

	requestOptions := f.extractRequestOptions(options)

	if paginationCfg, ok := asStringMap(options["pagination"]); ok && paginationCfg != nil {
		return f.fetchPaginated(source, requestOptions, paginationCfg, options)
	}

	responseBody, _, err := f.executeRequest(source, requestOptions)
	if err != nil {
		return nil, err
	}

	return parseResponseContract(responseBody, options)
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

	switch p := options["params"].(type) {
	case map[string]string:
		requestOptions.Params = p
	case map[string]interface{}:
		requestOptions.Params = make(map[string]string, len(p))
		for k, v := range p {
			requestOptions.Params[k] = fmt.Sprintf("%v", v)
		}
	}

	if timeout, ok := options["timeout"].(time.Duration); ok {
		requestOptions.Timeout = timeout
	}

	return requestOptions
}

// executeRequest performs a single HTTP request and returns the response
// body and headers. Headers are returned (rather than discarded) because
// link_header pagination needs to read the response's Link header.
func (f *RESTFetcher) executeRequest(rawURL string, options RequestOptions) ([]byte, http.Header, error) {
	// Resolve relative URLs (e.g. /api/samples/data/file.csv) against the
	// Brokoli server. A relative path is an implicit self-reference to the
	// trusted server, so the SSRF guard must not block it — in k8s/docker
	// deployments BROKOLI_SERVER_URL typically resolves to a private cluster
	// IP (10.x / 172.16.x / ClusterIP), which is exactly what isBlockedHost
	// rejects. Track whether the URL originated as relative so we can skip
	// the SSRF check for trusted self-references only.
	trustedSelfRef := strings.HasPrefix(rawURL, "/")
	resolvedURL := rawURL
	if trustedSelfRef {
		base := os.Getenv("BROKOLI_SERVER_URL")
		if base == "" {
			port := os.Getenv("PORT")
			if port == "" {
				port = "8080"
			}
			base = "http://127.0.0.1:" + port
		}
		resolvedURL = strings.TrimRight(base, "/") + resolvedURL
	}

	if len(options.Params) > 0 {
		parsed, err := url.Parse(resolvedURL)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
		}
		q := parsed.Query()
		for k, v := range options.Params {
			q.Set(k, v)
		}
		parsed.RawQuery = q.Encode()
		resolvedURL = parsed.String()
	}

	// SSRF protection: block requests to private networks and cloud metadata.
	// Skip for trusted self-references to the Brokoli server itself.
	if !trustedSelfRef {
		if err := isBlockedHost(resolvedURL); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrSSRFBlocked, err)
		}
	}

	req, err := http.NewRequest(options.Method, resolvedURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
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
		return nil, nil, fmt.Errorf("%w: %v", ErrHTTPRequestFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("%w: status code %d", ErrHTTPRequestFailed, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, nil, ErrEmptyResponse
	}

	return body, resp.Header, nil
}

// parseResponseContract interprets a single response body according to the
// source_api node's `response`/`records`/`value_path` config, and falls
// back to ParseJSONData's auto-detection when none of those fields are set
// — preserving exact pre-existing behavior for pipelines built before these
// fields existed (brokoli-sdk#1).
func parseResponseContract(responseBody []byte, options map[string]interface{}) (*common.DataSet, error) {
	responseType, hasResponse := options["response"].(string)
	recordsPath, hasRecords := options["records"].(string)
	valuePath, hasValuePath := options["value_path"].(string)

	switch {
	case hasResponse && responseType == "scalar":
		return scalarDataSet(responseBody, valuePath)
	case hasResponse && responseType == "artifact":
		return artifactDataSet(responseBody), nil
	case hasRecords && recordsPath != "":
		records, err := common.ExtractRecordsAtPath(responseBody, recordsPath)
		if err != nil {
			return nil, fmt.Errorf("extract records at %q: %w", recordsPath, err)
		}
		return common.ConvertToDataSet(records), nil
	default:
		// No response-contract fields set (or response=="dataset" with no
		// records path) — exact legacy behavior: auto-detect the shape.
		_ = hasValuePath // value_path only applies to response=="scalar"; SDK validation rejects other combos
		data, err := common.ParseJSONData(responseBody)
		if err != nil {
			return nil, fmt.Errorf("failed to parse JSON response: %w", err)
		}
		return common.ConvertToDataSet(data), nil
	}
}

func scalarDataSet(responseBody []byte, valuePath string) (*common.DataSet, error) {
	var root interface{}
	if err := json.Unmarshal(responseBody, &root); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}
	value, ok := common.ExtractPath(root, valuePath)
	if !ok {
		return nil, fmt.Errorf("value_path %q not found in response", valuePath)
	}
	return &common.DataSet{
		Columns: []string{"value"},
		Rows:    []common.DataRow{{"value": value}},
	}, nil
}

// artifactDataSet wraps a raw, unparsed response body as a single-row,
// single-column dataset. There is no dedicated artifact/blob type on the Go
// side (unlike the SDK's ArtifactRef) — this is the minimal representation
// that fits the engine's existing DataSet-only node output contract.
func artifactDataSet(responseBody []byte) *common.DataSet {
	return &common.DataSet{
		Columns: []string{"value"},
		Rows:    []common.DataRow{{"value": string(responseBody)}},
	}
}

// extractDatasetRecords extracts the record list from a single paginated
// page's response body, honoring `records` when set and otherwise falling
// back to ParseJSONData's auto-detection — the same rule parseResponseContract
// uses for the dataset case. Pagination always requires response=="dataset"
// (enforced by SDK validation), so scalar/artifact handling doesn't apply here.
func extractDatasetRecords(responseBody []byte, options map[string]interface{}) ([]map[string]interface{}, error) {
	if recordsPath, ok := options["records"].(string); ok && recordsPath != "" {
		return common.ExtractRecordsAtPath(responseBody, recordsPath)
	}
	return common.ParseJSONData(responseBody)
}

// fetchPaginated drives the in-process, sequential page loop for a
// source_api node's `pagination` config. It supports the five strategies
// brokoli-sdk's brokoli/pagination.py compiles config for: offset, cursor,
// numbered, next_link, and link_header.
//
// Scope note: this runs entirely in-process and sequentially — pages are
// fetched one at a time, in order, by this single goroutine. `max_concurrency`
// and `checkpoint_every` from the SDK's execution() policy are NOT
// implemented (see runSourceAPI, which logs a warning when they're set) —
// they require distributed/concurrent page dispatch and crash-recoverable
// checkpointing, which is out of scope for this first version.
// `requests_per_second` (simple inter-request delay) and page-level retry
// (retry a single failed page a few times, using max_retries/retry_backoff)
// ARE implemented below.
func (f *RESTFetcher) fetchPaginated(source string, baseOptions RequestOptions, paginationCfg map[string]interface{}, fullOptions map[string]interface{}) (*common.DataSet, error) {
	strategy, _ := paginationCfg["strategy"].(string)

	execCfg, _ := asStringMap(fullOptions["execution"])
	minInterval := requestInterval(execCfg)

	maxRetries := intOpt(fullOptions, "max_retries", defaultPageRetries)
	retryBackoff := stringOpt(fullOptions, "retry_backoff", "exponential")

	var lastRequestAt time.Time
	fetchPage := func(pageURL string, opts RequestOptions) ([]byte, http.Header, error) {
		if minInterval > 0 && !lastRequestAt.IsZero() {
			if elapsed := time.Since(lastRequestAt); elapsed < minInterval {
				time.Sleep(minInterval - elapsed)
			}
		}

		var body []byte
		var headers http.Header
		var err error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			body, headers, err = f.executeRequest(pageURL, opts)
			lastRequestAt = time.Now()
			if err == nil {
				return body, headers, nil
			}
			if attempt < maxRetries {
				time.Sleep(pageRetryDelay(retryBackoff, attempt))
			}
		}
		return nil, nil, err
	}

	maxRecords := intOpt(paginationCfg, "max_records", 0)

	var allRecords []map[string]interface{}
	appendPage := func(body []byte) (stop bool, err error) {
		records, err := extractDatasetRecords(body, fullOptions)
		if err != nil {
			return false, err
		}
		allRecords = append(allRecords, records...)
		if maxRecords > 0 && len(allRecords) >= maxRecords {
			allRecords = allRecords[:maxRecords]
			return true, nil
		}
		return len(records) == 0, nil
	}

	switch strategy {
	case "offset":
		if err := f.paginateOffset(source, baseOptions, paginationCfg, fetchPage, appendPage); err != nil {
			return nil, err
		}
	case "cursor":
		if err := f.paginateCursor(source, baseOptions, paginationCfg, fetchPage, appendPage); err != nil {
			return nil, err
		}
	case "numbered":
		if err := f.paginateNumbered(source, baseOptions, paginationCfg, fetchPage, appendPage); err != nil {
			return nil, err
		}
	case "next_link":
		if err := f.paginateNextLink(source, baseOptions, paginationCfg, fetchPage, appendPage); err != nil {
			return nil, err
		}
	case "link_header":
		if err := f.paginateLinkHeader(source, baseOptions, paginationCfg, fetchPage, appendPage); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported pagination strategy %q", strategy)
	}

	return common.ConvertToDataSet(allRecords), nil
}

type fetchPageFunc func(pageURL string, opts RequestOptions) ([]byte, http.Header, error)
type appendPageFunc func(body []byte) (stop bool, err error)

func (f *RESTFetcher) paginateOffset(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc) error {
	pageSize := intOpt(cfg, "page_size", 0)
	if pageSize <= 0 {
		return fmt.Errorf("pagination strategy \"offset\" requires a positive page_size")
	}
	offsetParam := stringOpt(cfg, "offset_param", "offset")
	limitParam := stringOpt(cfg, "limit_param", "limit")
	endFlagPath, _ := cfg["end_flag"].(string)

	offset := 0
	for i := 0; i < maxPaginationPages; i++ {
		opts := baseOptions
		opts.Params = mergeParams(baseOptions.Params, map[string]string{
			offsetParam: strconv.Itoa(offset),
			limitParam:  strconv.Itoa(pageSize),
		})

		body, _, err := fetchPage(source, opts)
		if err != nil {
			return fmt.Errorf("pagination page at offset %d: %w", offset, err)
		}

		stop, err := appendPage(body)
		if err != nil {
			return fmt.Errorf("pagination page at offset %d: %w", offset, err)
		}

		endFlag := false
		if endFlagPath != "" {
			var root interface{}
			if jsonErr := json.Unmarshal(body, &root); jsonErr == nil {
				if v, ok := common.ExtractPath(root, endFlagPath); ok {
					endFlag, _ = v.(bool)
				}
			}
		}

		if stop || endFlag {
			return nil
		}
		offset += pageSize
	}
	return nil
}

func (f *RESTFetcher) paginateNumbered(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc) error {
	pageParam := stringOpt(cfg, "page_param", "page")
	start := intOpt(cfg, "start", 1)
	totalPagesPath, _ := cfg["total_pages_path"].(string)

	page := start
	pagesFetched := 0
	for i := 0; i < maxPaginationPages; i++ {
		opts := baseOptions
		opts.Params = mergeParams(baseOptions.Params, map[string]string{pageParam: strconv.Itoa(page)})

		body, _, err := fetchPage(source, opts)
		if err != nil {
			return fmt.Errorf("pagination page %d: %w", page, err)
		}

		stop, err := appendPage(body)
		if err != nil {
			return fmt.Errorf("pagination page %d: %w", page, err)
		}
		pagesFetched++

		totalPages := -1
		if totalPagesPath != "" {
			var root interface{}
			if jsonErr := json.Unmarshal(body, &root); jsonErr == nil {
				if v, ok := common.ExtractPath(root, totalPagesPath); ok {
					if n, ok := toFloat(v); ok {
						totalPages = int(n)
					}
				}
			}
		}

		if stop {
			return nil
		}
		if totalPages >= 0 {
			if pagesFetched >= totalPages {
				return nil
			}
		}
		page++
	}
	return nil
}

func (f *RESTFetcher) paginateCursor(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc) error {
	cursorPath, _ := cfg["cursor_path"].(string)
	cursorParam, _ := cfg["cursor_param"].(string)
	if cursorPath == "" || cursorParam == "" {
		return fmt.Errorf("pagination strategy \"cursor\" requires cursor_path and cursor_param")
	}

	cursor := ""
	for i := 0; i < maxPaginationPages; i++ {
		opts := baseOptions
		if cursor != "" {
			opts.Params = mergeParams(baseOptions.Params, map[string]string{cursorParam: cursor})
		}

		body, _, err := fetchPage(source, opts)
		if err != nil {
			return fmt.Errorf("pagination cursor page: %w", err)
		}

		stop, err := appendPage(body)
		if err != nil {
			return fmt.Errorf("pagination cursor page: %w", err)
		}
		if stop {
			return nil
		}

		nextCursor := ""
		var root interface{}
		if jsonErr := json.Unmarshal(body, &root); jsonErr == nil {
			if v, ok := common.ExtractPath(root, cursorPath); ok {
				nextCursor = stringifyScalar(v)
			}
		}
		if nextCursor == "" {
			return nil
		}
		cursor = nextCursor
	}
	return nil
}

func (f *RESTFetcher) paginateNextLink(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc) error {
	nextPath, _ := cfg["next_path"].(string)
	if nextPath == "" {
		return fmt.Errorf("pagination strategy \"next_link\" requires next_path")
	}

	nextURL := source
	opts := baseOptions
	for i := 0; i < maxPaginationPages; i++ {
		body, _, err := fetchPage(nextURL, opts)
		if err != nil {
			return fmt.Errorf("pagination next_link page: %w", err)
		}

		stop, err := appendPage(body)
		if err != nil {
			return fmt.Errorf("pagination next_link page: %w", err)
		}
		if stop {
			return nil
		}

		next := ""
		var root interface{}
		if jsonErr := json.Unmarshal(body, &root); jsonErr == nil {
			if v, ok := common.ExtractPath(root, nextPath); ok {
				next = stringifyScalar(v)
			}
		}
		if next == "" {
			return nil
		}
		// The next-page URL is expected to be a complete, ready-to-use URL
		// (that's the point of next_link pagination) — subsequent requests
		// send no extra query params of their own.
		nextURL = next
		opts.Params = nil
	}
	return nil
}

func (f *RESTFetcher) paginateLinkHeader(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc) error {
	rel := stringOpt(cfg, "rel", "next")

	nextURL := source
	opts := baseOptions
	for i := 0; i < maxPaginationPages; i++ {
		body, headers, err := fetchPage(nextURL, opts)
		if err != nil {
			return fmt.Errorf("pagination link_header page: %w", err)
		}

		stop, err := appendPage(body)
		if err != nil {
			return fmt.Errorf("pagination link_header page: %w", err)
		}
		if stop {
			return nil
		}

		next := parseLinkHeader(headers.Get("Link"), rel)
		if next == "" {
			return nil
		}
		nextURL = next
		opts.Params = nil
	}
	return nil
}

// parseLinkHeader extracts the URL for the given rel value out of an RFC
// 8288 Link header, e.g. `<https://api.example.com/x?page=2>; rel="next"`.
func parseLinkHeader(header, rel string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(part, ";")
		if len(segments) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		linkURL := strings.TrimSuffix(strings.TrimPrefix(urlPart, "<"), ">")
		for _, seg := range segments[1:] {
			seg = strings.TrimSpace(seg)
			if !strings.HasPrefix(seg, "rel=") {
				continue
			}
			relVal := strings.Trim(strings.TrimPrefix(seg, "rel="), `"`)
			if relVal == rel {
				return linkURL
			}
		}
	}
	return ""
}

// requestInterval converts an execution config's requests_per_second into
// the minimum delay to enforce between successive page requests.
func requestInterval(execCfg map[string]interface{}) time.Duration {
	rps, ok := numOpt(execCfg, "requests_per_second")
	if !ok || rps <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / rps)
}

// pageRetryDelay returns the backoff delay before retry attempt `attempt`
// (0-indexed) of a failed page fetch.
func pageRetryDelay(backoff string, attempt int) time.Duration {
	base := 100 * time.Millisecond
	if backoff == "linear" {
		return base * time.Duration(attempt+1)
	}
	// Default/"exponential"
	delay := base
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func mergeParams(base map[string]string, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

func asStringMap(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})
	return m, ok
}

func numOpt(m map[string]interface{}, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func intOpt(m map[string]interface{}, key string, def int) int {
	if v, ok := numOpt(m, key); ok {
		return int(v)
	}
	return def
}

func stringOpt(m map[string]interface{}, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// stringifyScalar renders a JSON scalar value (string/number/bool) as a
// string suitable for use as a query-param value, e.g. a cursor token.
func stringifyScalar(v interface{}) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}
