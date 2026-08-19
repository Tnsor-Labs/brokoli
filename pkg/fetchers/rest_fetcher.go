package fetchers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
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

type RESTFetcher struct {
	client *http.Client

	// selfRefClient is used only for trustedSelfRef requests (see
	// executeRequestContext) -- the one legitimate case that needs to
	// reach a loopback/private-range address (the Brokoli server itself,
	// which in k8s/docker deployments is typically a 10.x/172.16.x/
	// ClusterIP address). Kept separate from client, which stays
	// loopback-blocked for every externally-supplied URL a pipeline
	// author configures.
	selfRefClient *http.Client

	// artifacts, when set, is where a response="artifact" body is stored so
	// the node can return a reference to it rather than the bytes
	// themselves. Nil in validation, dry runs and unit tests, where the
	// fetcher is constructed without an engine behind it — see
	// parseResponseContract for what happens then.
	artifacts ArtifactSink
}

// SetArtifactSink implements ArtifactAwareFetcher.
func (f *RESTFetcher) SetArtifactSink(sink ArtifactSink) { f.artifacts = sink }

type RequestOptions struct {
	Method  string
	Headers map[string]string
	Body    interface{}
	Params  map[string]string
	Timeout time.Duration
	// AuthUser/AuthPassword carry HTTP Basic Auth resolved from a
	// connection's login/password fields. Living on RequestOptions means
	// every request funnelled through executeRequest carries them — the
	// first page and every paginated page alike.
	AuthUser     string
	AuthPassword string
}

func (f *RESTFetcher) Fetch(source string, options map[string]interface{}) (*common.DataSet, error) {
	return f.FetchPaginatedResumable(source, options, nil, nil, nil)
}

// FetchPaginatedResumable implements CheckpointingFetcher. For a
// non-paginated source (no "pagination" in options), resume/resumeRecords/
// onCheckpoint are simply unused — this is exactly Fetch's original body.
func (f *RESTFetcher) FetchPaginatedResumable(source string, options map[string]interface{}, resume *PaginationCheckpoint, resumeRecords []map[string]interface{}, onCheckpoint CheckpointSaver) (*common.DataSet, error) {
	if source == "" {
		return nil, ErrInvalidURL
	}

	f.ensureClientInitialized(options)

	requestOptions := f.extractRequestOptions(options)

	if paginationCfg, ok := asStringMap(options["pagination"]); ok && paginationCfg != nil {
		return f.fetchPaginated(source, requestOptions, paginationCfg, options, resume, resumeRecords, onCheckpoint)
	}

	responseBody, headers, err := f.executeRequest(source, requestOptions)
	if err != nil {
		return nil, err
	}

	return f.parseResponseContract(responseBody, options, headers.Get("Content-Type"))
}

// FetchPage executes one pagination request without entering the fetcher's
// multi-page coordinator. It is the worker-side primitive for a source_api
// page WorkOrder; pageURL is used by link-based callers while pageParams are
// merged into the source node's configured params for offset/numbered pages.
func (f *RESTFetcher) FetchPage(source string, options map[string]interface{}, pageURL string, pageParams map[string]string) (*common.DataSet, error) {
	return f.FetchPageContext(context.Background(), source, options, pageURL, pageParams)
}

// FetchPageContext executes one pagination request while honoring ctx.
func (f *RESTFetcher) FetchPageContext(ctx context.Context, source string, options map[string]interface{}, pageURL string, pageParams map[string]string) (*common.DataSet, error) {
	if source == "" {
		return nil, ErrInvalidURL
	}

	f.ensureClientInitialized(options)
	requestOptions := f.extractRequestOptions(options)
	requestOptions.Params = mergeParams(requestOptions.Params, pageParams)
	if pageURL == "" {
		pageURL = source
	}

	responseBody, headers, err := f.executeRequestContext(ctx, pageURL, requestOptions)
	if err != nil {
		return nil, err
	}
	return f.parseResponseContract(responseBody, options, headers.Get("Content-Type"))
}

// outboundPolicy is the SSRF policy used for externally-supplied
// source_api URLs. A package var, not a hardcoded netguard.Default,
// solely so this package's own tests (which fetch from httptest.Server
// -- i.e. loopback -- to simulate an external API, not to exercise the
// SSRF guard itself) can relax it in TestMain; see fetchers_test.go.
// Production always runs with the netguard.Default this is initialized
// to.
var outboundPolicy = netguard.Default

// SetOutboundPolicyForTesting overrides the SSRF policy RESTFetcher uses
// for external URLs, for the lifetime of a test binary. Exported (not
// package-private) because Go's global state doesn't cross package
// boundaries: a package like engine, whose own tests build pipelines
// that exercise a real RESTFetcher against an httptest.Server -- i.e.
// loopback, to simulate an external API rather than to test the SSRF
// guard itself -- needs this from its own TestMain, not just this
// package's. Not for use outside tests; production always runs with
// this var's real initializer (netguard.Default).
func SetOutboundPolicyForTesting(p netguard.Policy) (restore func()) {
	prev := outboundPolicy
	outboundPolicy = p
	return func() { outboundPolicy = prev }
}

func (f *RESTFetcher) ensureClientInitialized(options map[string]interface{}) {
	timeout := 30 * time.Second // Default timeout
	if t, ok := options["timeout"].(time.Duration); ok {
		timeout = t
	}
	if f.client == nil {
		f.client = outboundPolicy.Client(timeout)
	} else {
		f.client.Timeout = timeout
	}
	if f.selfRefClient == nil {
		f.selfRefClient = netguard.Policy{AllowLoopback: true, AllowPrivate: true}.Client(timeout)
	} else {
		f.selfRefClient.Timeout = timeout
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

	// Basic Auth injected by the connection resolver (auth_user/auth_password
	// from a connection's login/password). These were resolved into the node
	// config but never read here, so reads went out unauthenticated while
	// writes worked — the far-from-the-cause 401 of Tnsor-Labs/brokoli#58.
	if user, ok := options["auth_user"].(string); ok && user != "" {
		requestOptions.AuthUser = user
		requestOptions.AuthPassword, _ = options["auth_password"].(string)
	}

	return requestOptions
}

// executeRequest performs a single HTTP request and returns the response
// body and headers. Headers are returned (rather than discarded) because
// link_header pagination needs to read the response's Link header.
func (f *RESTFetcher) executeRequest(rawURL string, options RequestOptions) ([]byte, http.Header, error) {
	return f.executeRequestContext(context.Background(), rawURL, options)
}

func (f *RESTFetcher) executeRequestContext(ctx context.Context, rawURL string, options RequestOptions) ([]byte, http.Header, error) {
	// Resolve relative URLs (e.g. /api/samples/data/file.csv) against the
	// Brokoli server. A relative path is an implicit self-reference to the
	// trusted server — in k8s/docker deployments BROKOLI_SERVER_URL
	// typically resolves to a private cluster IP (10.x / 172.16.x /
	// ClusterIP) or loopback, which netguard.Default would otherwise
	// block. Track whether the URL originated as relative so the request
	// below can use selfRefClient (AllowLoopback: true) instead of the
	// loopback-blocked client every externally-supplied URL goes through.
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

	req, err := http.NewRequest(options.Method, resolvedURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req = req.WithContext(ctx)

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

	// Applied after the headers loop, so connection credentials win over a
	// handwritten Authorization header — the same precedence runSinkAPI
	// gives the write path. The connection is the authoritative credential
	// source; a node that needs a different scheme should not attach a
	// basic-auth connection.
	if options.AuthUser != "" {
		req.SetBasicAuth(options.AuthUser, options.AuthPassword)
	}

	client := f.client
	if trustedSelfRef {
		client = f.selfRefClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrBlockedTarget) {
			return nil, nil, fmt.Errorf("%w: %v", ErrSSRFBlocked, err)
		}
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
func (f *RESTFetcher) parseResponseContract(responseBody []byte, options map[string]interface{}, contentType string) (*common.DataSet, error) {
	responseType, hasResponse := options["response"].(string)
	recordsPath, hasRecords := options["records"].(string)
	valuePath, hasValuePath := options["value_path"].(string)

	switch {
	case hasResponse && responseType == "scalar":
		return scalarDataSet(responseBody, valuePath)
	case hasResponse && responseType == "artifact":
		return f.artifactResult(responseBody, contentType)
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

// artifactResult turns a raw, unparsed response body into this node's
// output for response="artifact".
//
// With an artifact sink wired, the body is stored and the node returns a
// reference to it: a row naming the URI, media type, size and checksum. That
// is what response="artifact" was always meant to mean, and what the SDK's
// ArtifactRef describes on the authoring side.
//
// Without a sink the body is inlined as before. That path is not a fallback
// for convenience — it is what validation, dry runs and any caller
// constructing a bare RESTFetcher get, and changing it would break them for
// no gain. Storing by reference needs somewhere to store to; when there is
// nowhere, the honest result is the body itself.
//
// A sink that fails is an error, not a reason to inline. Silently returning
// a multi-gigabyte body because the store was unavailable would defeat the
// point of asking for an artifact in the first place.
func (f *RESTFetcher) artifactResult(responseBody []byte, contentType string) (*common.DataSet, error) {
	if f.artifacts == nil {
		return artifactDataSet(responseBody), nil
	}
	// The upstream server's Content-Type is the best statement available of
	// what these bytes are, and the reference exists so a reader never has
	// to guess. Recorded verbatim (parameters like charset included); only
	// an absent header falls back to the opaque default.
	mediaType := contentType
	if mediaType == "" {
		mediaType = artifact.MediaTypeOctetStream
	}
	ref, err := f.artifacts.PutArtifact(context.Background(), bytes.NewReader(responseBody), mediaType)
	if err != nil {
		return nil, fmt.Errorf("store artifact response: %w", err)
	}
	return artifactRefDataSet(ref), nil
}

// artifactDataSet wraps a raw, unparsed response body as a single-row,
// single-column dataset — the representation used when no artifact sink is
// available to store the body by reference.
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
//
// A bare empty array ("[]") is special-cased to zero records here, ahead of
// ParseJSONData: unlike a one-shot Fetch (where an empty array response is
// arguably a sign something's wrong upstream — see TestRESTFetcher_Fetch's
// "empty response" case, which deliberately keeps treating it as an error),
// an empty page is a normal, common way for a paginated API to say "no more
// results" when the pipeline author hasn't configured records/end_flag/
// total_pages_path — and fetchPaginated's own stop condition already
// expects to see it as "0 records", not an error (see appendPage's
// `len(records) == 0` check). Deliberately narrower than changing
// ParseJSONData itself, which stays as-is for its other two callers.
func extractDatasetRecords(responseBody []byte, options map[string]interface{}) ([]map[string]interface{}, error) {
	if recordsPath, ok := options["records"].(string); ok && recordsPath != "" {
		return common.ExtractRecordsAtPath(responseBody, recordsPath)
	}
	if bytes.Equal(bytes.TrimSpace(responseBody), []byte("[]")) {
		return []map[string]interface{}{}, nil
	}
	return common.ParseJSONData(responseBody)
}

// fetchPaginated drives the in-process page loop for a source_api node's
// `pagination` config. It supports the five strategies brokoli-sdk's
// brokoli/pagination.py compiles config for: offset, cursor, numbered,
// next_link, and link_header.
//
// Concurrency: `max_concurrency` from the SDK's execution() policy bounds
// how many pages are in flight at once, but only for the "offset" and
// "numbered" strategies — those are the only two where a page's request
// can be computed without seeing the previous page's response, so a batch
// of upcoming pages can be dispatched together. "cursor", "next_link", and
// "link_header" each derive the next request from the current response
// (a cursor token, a next-page URL) and stay strictly sequential regardless
// of max_concurrency — see runSourceAPI in engine/node_handlers.go, which
// warns when max_concurrency is set on one of those strategies. Within a
// batch, pages are appended in request order (not completion order), and a
// stop signal (end_flag/total_pages/empty page/max_records) found anywhere
// in the batch halts further dispatch — identical semantics to the
// sequential loop, just with the batch's HTTP round-trips overlapped.
// max_concurrency unset (or <= 1) reproduces the exact sequential loop this
// function always used.
//
// checkpoint_every from the SDK's execution() policy persists progress
// periodically via onCheckpoint (see checkpointFunc below) and resume/
// resumeRecords let a later call pick back up from a prior checkpoint
// instead of re-fetching from page one — see runSourceAPI in
// engine/node_handlers.go and engine/pagination_checkpoint_store.go for the
// durable side of this (issue #41 M2). checkpoint_every unset, or
// onCheckpoint nil, disables all of this — pages are fetched exactly as
// before with zero extra overhead.
//
// `requests_per_second` (simple inter-request delay) and page-level retry
// ARE implemented below, and are safe to combine with max_concurrency > 1:
// the rate limiter serializes *when* a request is allowed to start, while
// the actual request/response work for concurrently-started pages proceeds
// in parallel.
//
// Page-level retry count/backoff prefer execution().page_max_retries /
// execution().page_retry_backoff, falling back to the node's top-level
// max_retries/retry_backoff when unset — see issue #47. Those top-level
// fields are also read by executeNode (engine/runner.go) for *node*-level
// retry (re-running the whole node, not just one page), which is a
// different, coarser-grained retry loop; sharing one key between the two
// meant a pipeline author couldn't configure them independently. Falling
// back to the shared key keeps existing pipelines' behavior unchanged —
// this is purely additive.
func (f *RESTFetcher) fetchPaginated(source string, baseOptions RequestOptions, paginationCfg map[string]interface{}, fullOptions map[string]interface{}, resume *PaginationCheckpoint, resumeRecords []map[string]interface{}, onCheckpoint CheckpointSaver) (*common.DataSet, error) {
	strategy, _ := paginationCfg["strategy"].(string)

	execCfg, _ := asStringMap(fullOptions["execution"])
	minInterval := requestInterval(execCfg)

	maxConcurrency := intOpt(execCfg, "max_concurrency", 1)
	if maxConcurrency < 1 {
		maxConcurrency = 1
	}

	checkpointEvery := intOpt(execCfg, "checkpoint_every", 0)

	// A checkpoint from a different strategy (e.g. the pipeline was edited
	// between attempts) can't be resumed from — treat it as no checkpoint
	// rather than misinterpreting its position fields.
	if resume != nil && resume.Strategy != "" && resume.Strategy != strategy {
		resume = nil
		resumeRecords = nil
	}

	maxRetries := intOpt(execCfg, "page_max_retries", intOpt(fullOptions, "max_retries", defaultPageRetries))
	retryBackoff := stringOpt(execCfg, "page_retry_backoff", stringOpt(fullOptions, "retry_backoff", "exponential"))

	var lastRequestAt time.Time
	var rateMu sync.Mutex
	waitForRateSlot := func() {
		if minInterval <= 0 {
			return
		}
		rateMu.Lock()
		defer rateMu.Unlock()
		if !lastRequestAt.IsZero() {
			if elapsed := time.Since(lastRequestAt); elapsed < minInterval {
				time.Sleep(minInterval - elapsed)
			}
		}
		lastRequestAt = time.Now()
	}

	// fetchPage is safe to call concurrently from multiple goroutines: the
	// only shared mutable state (the rate limiter's lastRequestAt) is
	// guarded by rateMu above.
	fetchPage := func(pageURL string, opts RequestOptions) ([]byte, http.Header, error) {
		var body []byte
		var headers http.Header
		var err error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			waitForRateSlot()
			body, headers, err = f.executeRequest(pageURL, opts)
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
	if len(resumeRecords) > 0 {
		allRecords = append(allRecords, resumeRecords...)
	}
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

	// checkpoint hands onCheckpoint only the records added since the last
	// checkpoint (or since resume, for the first one) — not a full
	// allRecords snapshot — so a store can append rather than rewrite its
	// entire records file on every save (issue #52). lastCheckpointedCount
	// only advances on a successful save: a failed save's records are
	// folded into the next checkpoint's (larger) delta instead of being
	// lost. A save failure is the caller's concern, not a fetch failure —
	// see CheckpointSaver's doc comment.
	lastCheckpointedCount := len(resumeRecords)
	checkpoint := func(pagesFetched int, pos PaginationCheckpoint) {
		if checkpointEvery <= 0 || onCheckpoint == nil {
			return
		}
		if pagesFetched == 0 || pagesFetched%checkpointEvery != 0 {
			return
		}
		pos.Strategy = strategy
		pos.PagesFetched = pagesFetched
		pos.RecordCount = len(allRecords)
		delta := make([]map[string]interface{}, len(allRecords)-lastCheckpointedCount)
		copy(delta, allRecords[lastCheckpointedCount:])
		if err := onCheckpoint(pos, delta); err == nil {
			lastCheckpointedCount = len(allRecords)
		}
	}

	var resumeErr error
	switch strategy {
	case "offset":
		resumeErr = f.paginateOffset(source, baseOptions, paginationCfg, fetchPage, appendPage, maxConcurrency, resume, checkpoint)
	case "cursor":
		resumeErr = f.paginateCursor(source, baseOptions, paginationCfg, fetchPage, appendPage, resume, checkpoint)
	case "numbered":
		resumeErr = f.paginateNumbered(source, baseOptions, paginationCfg, fetchPage, appendPage, maxConcurrency, resume, checkpoint)
	case "next_link":
		resumeErr = f.paginateNextLink(source, baseOptions, paginationCfg, fetchPage, appendPage, resume, checkpoint)
	case "link_header":
		resumeErr = f.paginateLinkHeader(source, baseOptions, paginationCfg, fetchPage, appendPage, resume, checkpoint)
	default:
		return nil, fmt.Errorf("unsupported pagination strategy %q", strategy)
	}
	if resumeErr != nil {
		return nil, resumeErr
	}

	return common.ConvertToDataSet(allRecords), nil
}

// checkpointFunc is called after a page is successfully appended, with the
// running page count and that strategy's current position. It internally
// no-ops unless checkpoint_every and onCheckpoint are both set — see
// fetchPaginated's checkpoint closure.
type checkpointFunc func(pagesFetched int, pos PaginationCheckpoint)

// pageFetchResult holds the outcome of one page fetch within a concurrent
// batch, keyed by its position so results can be processed in request
// order regardless of which goroutine finished first.
type pageFetchResult struct {
	body []byte
	err  error
}

// fetchBatch dispatches fetchPage for each of the given page URLs. With
// batch size 1 it calls fetchPage directly (no goroutine overhead — this is
// the max_concurrency-unset path and must behave identically to a plain
// sequential call). With a larger batch it runs them concurrently and waits
// for all to finish, preserving each result's index.
func fetchBatch(fetchPage fetchPageFunc, source string, opts []RequestOptions) []pageFetchResult {
	results := make([]pageFetchResult, len(opts))
	if len(opts) == 1 {
		body, _, err := fetchPage(source, opts[0])
		results[0] = pageFetchResult{body, err}
		return results
	}
	var wg sync.WaitGroup
	for i := range opts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body, _, err := fetchPage(source, opts[i])
			results[i] = pageFetchResult{body, err}
		}(i)
	}
	wg.Wait()
	return results
}

type fetchPageFunc func(pageURL string, opts RequestOptions) ([]byte, http.Header, error)
type appendPageFunc func(body []byte) (stop bool, err error)

func (f *RESTFetcher) paginateOffset(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc, maxConcurrency int, resume *PaginationCheckpoint, checkpoint checkpointFunc) error {
	pageSize := intOpt(cfg, "page_size", 0)
	if pageSize <= 0 {
		return fmt.Errorf("pagination strategy \"offset\" requires a positive page_size")
	}
	offsetParam := stringOpt(cfg, "offset_param", "offset")
	limitParam := stringOpt(cfg, "limit_param", "limit")
	endFlagPath, _ := cfg["end_flag"].(string)

	buildOpts := func(offset int) RequestOptions {
		opts := baseOptions
		opts.Params = mergeParams(baseOptions.Params, map[string]string{
			offsetParam: strconv.Itoa(offset),
			limitParam:  strconv.Itoa(pageSize),
		})
		return opts
	}

	hasEndFlag := func(body []byte) bool {
		if endFlagPath == "" {
			return false
		}
		var root interface{}
		if jsonErr := json.Unmarshal(body, &root); jsonErr == nil {
			if v, ok := common.ExtractPath(root, endFlagPath); ok {
				endFlag, _ := v.(bool)
				return endFlag
			}
		}
		return false
	}

	offset := 0
	pagesFetched := 0
	if resume != nil {
		offset = resume.Offset
		pagesFetched = resume.PagesFetched
	}
	for pagesFetched < maxPaginationPages {
		batch := min(maxConcurrency, maxPaginationPages-pagesFetched)

		offsets := make([]int, batch)
		opts := make([]RequestOptions, batch)
		for i := 0; i < batch; i++ {
			offsets[i] = offset + i*pageSize
			opts[i] = buildOpts(offsets[i])
		}

		results := fetchBatch(fetchPage, source, opts)
		for i, res := range results {
			if res.err != nil {
				return fmt.Errorf("pagination page at offset %d: %w", offsets[i], res.err)
			}
			stop, err := appendPage(res.body)
			if err != nil {
				return fmt.Errorf("pagination page at offset %d: %w", offsets[i], err)
			}
			pagesFetched++
			if stop || hasEndFlag(res.body) {
				return nil
			}
			checkpoint(pagesFetched, PaginationCheckpoint{Offset: offsets[i] + pageSize})
		}
		offset += batch * pageSize
	}
	return nil
}

func (f *RESTFetcher) paginateNumbered(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc, maxConcurrency int, resume *PaginationCheckpoint, checkpoint checkpointFunc) error {
	pageParam := stringOpt(cfg, "page_param", "page")
	start := intOpt(cfg, "start", 1)
	totalPagesPath, _ := cfg["total_pages_path"].(string)

	buildOpts := func(page int) RequestOptions {
		opts := baseOptions
		opts.Params = mergeParams(baseOptions.Params, map[string]string{pageParam: strconv.Itoa(page)})
		return opts
	}

	readTotalPages := func(body []byte) int {
		if totalPagesPath == "" {
			return -1
		}
		var root interface{}
		if jsonErr := json.Unmarshal(body, &root); jsonErr == nil {
			if v, ok := common.ExtractPath(root, totalPagesPath); ok {
				if n, ok := toFloat(v); ok {
					return int(n)
				}
			}
		}
		return -1
	}

	page := start
	pagesFetched := 0
	if resume != nil {
		page = resume.Page
		pagesFetched = resume.PagesFetched
	}
	for pagesFetched < maxPaginationPages {
		batch := min(maxConcurrency, maxPaginationPages-pagesFetched)

		pages := make([]int, batch)
		opts := make([]RequestOptions, batch)
		for i := 0; i < batch; i++ {
			pages[i] = page + i
			opts[i] = buildOpts(pages[i])
		}

		results := fetchBatch(fetchPage, source, opts)
		for i, res := range results {
			if res.err != nil {
				return fmt.Errorf("pagination page %d: %w", pages[i], res.err)
			}
			stop, err := appendPage(res.body)
			if err != nil {
				return fmt.Errorf("pagination page %d: %w", pages[i], err)
			}
			pagesFetched++

			if stop {
				return nil
			}
			if totalPages := readTotalPages(res.body); totalPages >= 0 && pagesFetched >= totalPages {
				return nil
			}
			checkpoint(pagesFetched, PaginationCheckpoint{Page: pages[i] + 1})
		}
		page += batch
	}
	return nil
}

func (f *RESTFetcher) paginateCursor(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc, resume *PaginationCheckpoint, checkpoint checkpointFunc) error {
	cursorPath, _ := cfg["cursor_path"].(string)
	cursorParam, _ := cfg["cursor_param"].(string)
	if cursorPath == "" || cursorParam == "" {
		return fmt.Errorf("pagination strategy \"cursor\" requires cursor_path and cursor_param")
	}

	cursor := ""
	pagesFetched := 0
	if resume != nil {
		cursor = resume.Cursor
		pagesFetched = resume.PagesFetched
	}
	for ; pagesFetched < maxPaginationPages; pagesFetched++ {
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
		checkpoint(pagesFetched+1, PaginationCheckpoint{Cursor: cursor})
	}
	return nil
}

func (f *RESTFetcher) paginateNextLink(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc, resume *PaginationCheckpoint, checkpoint checkpointFunc) error {
	nextPath, _ := cfg["next_path"].(string)
	if nextPath == "" {
		return fmt.Errorf("pagination strategy \"next_link\" requires next_path")
	}

	nextURL := source
	pagesFetched := 0
	if resume != nil && resume.NextURL != "" {
		nextURL = resume.NextURL
		pagesFetched = resume.PagesFetched
	}
	opts := baseOptions
	for ; pagesFetched < maxPaginationPages; pagesFetched++ {
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
		checkpoint(pagesFetched+1, PaginationCheckpoint{NextURL: nextURL})
	}
	return nil
}

func (f *RESTFetcher) paginateLinkHeader(source string, baseOptions RequestOptions, cfg map[string]interface{}, fetchPage fetchPageFunc, appendPage appendPageFunc, resume *PaginationCheckpoint, checkpoint checkpointFunc) error {
	rel := stringOpt(cfg, "rel", "next")

	nextURL := source
	pagesFetched := 0
	if resume != nil && resume.NextURL != "" {
		nextURL = resume.NextURL
		pagesFetched = resume.PagesFetched
	}
	opts := baseOptions
	for ; pagesFetched < maxPaginationPages; pagesFetched++ {
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
		checkpoint(pagesFetched+1, PaginationCheckpoint{NextURL: nextURL})
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
