package engine

// Mapping a source_api node over a collection (ADR-050 phase 1, #967).
//
// A source_api node with a `map` block makes its request once per item of
// the upstream node named in map.over, with ${item.column} references
// replaced by that item's values. Item values come from data, so each one
// is encoded for the place it lands and may only land where it cannot
// change where the request goes:
//
//   - in the url's path, percent-encoded as one path segment, so "/", "?"
//     and "#" in a value stay inside it; a value of "." or ".." is refused;
//   - in the url's query, percent-encoded as a query component;
//   - in a `params` or `headers` value, as text (a header value with a line
//     break is refused);
//   - in a JSON `body`, as a JSON value: a string that is exactly one
//     reference takes the item's value with its type, and a reference
//     inside a longer string becomes text.
//
// Anywhere else (the url's scheme, host, port or fragment, a header or
// parameter name, a text body, pagination or auth settings) a reference is
// refused, when the pipeline is saved and again when it runs.

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// itemReferenceRegexp matches one ${item...} reference.
var itemReferenceRegexp = regexp.MustCompile(`\$\{item\.[^}]*\}`)

// apiItemPlaces is how an error names where item references may appear.
const apiItemPlaces = "the url's path or query, a 'params' or 'headers' value, or a JSON 'body'"

// mappedAPIErrors checks a source_api node's `map` block and where its
// ${item...} references appear, when the pipeline is saved and when the
// node runs.
func mappedAPIErrors(n models.Node) []string {
	where := fmt.Sprintf("Node %q", n.Name)
	if !nodeIsMapped(n) {
		if path := firstItemReferencePath(n.Config, ""); path != "" {
			return []string{fmt.Sprintf("%s: ${item...} in '%s': item references are only available in a node with a 'map' block", where, path)}
		}
		return nil
	}
	var errs []string
	if _, err := parseMapConfig(n); err != nil {
		errs = append(errs, err.Error())
	}
	for _, msg := range apiItemPositionErrors(n.Config) {
		errs = append(errs, fmt.Sprintf("%s: %s", where, msg))
	}
	execCfg, _ := n.Config["execution"].(map[string]interface{})
	if _, ok := execCfg["checkpoint_every"]; ok {
		errs = append(errs, fmt.Sprintf("%s: 'execution.checkpoint_every' cannot be used with 'map': a checkpoint belongs to the node, and every item would overwrite it", where))
	}
	if response, _ := n.Config["response"].(string); response == "artifact" {
		errs = append(errs, fmt.Sprintf("%s: \"response\": \"artifact\" cannot be used with 'map': the items' responses would have to be combined, and artifacts cannot be", where))
	}
	if _, ok := execCfg["requests_per_second"]; ok {
		if cfg, err := parseMapConfig(n); err == nil && cfg.parallelism(false) != 1 {
			errs = append(errs, fmt.Sprintf("%s: 'execution.requests_per_second' paces one item's requests; with %d items at once the API would see up to %d times that rate. Set 'map.max_parallel' to 1, and the rate holds across items", where, cfg.parallelism(false), cfg.parallelism(false)))
		}
	}
	return errs
}

// apiItemPositionErrors names every ${item...} reference in cfg that sits
// where an item value could change where the request goes or how it is
// read.
func apiItemPositionErrors(cfg map[string]interface{}) []string {
	var errs []string
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := cfg[k]
		switch k {
		case "map":
			continue
		case "url":
			raw, _ := v.(string)
			if err := urlItemPositionError(raw); err != nil {
				errs = append(errs, err.Error())
			}
		case "params", "headers":
			m, _ := v.(map[string]interface{})
			for name, value := range m {
				if containsItemReference(name) {
					errs = append(errs, fmt.Sprintf("'%s' name %q: an item value cannot name a %s; it can only be a value", k, name, strings.TrimSuffix(k, "s")))
				}
				if path := firstItemReferencePath(value, k+"."+name); path != "" {
					if _, isText := value.(string); !isText {
						errs = append(errs, fmt.Sprintf("'%s': item references must be in a text value", path))
					}
				}
			}
		case "body":
			if text, ok := v.(string); ok && containsItemReference(text) {
				errs = append(errs, "'body' is text: an item value cannot be encoded safely inside text it does not know the format of. Give the body as a JSON object or array")
			}
			errs = append(errs, jsonBodyKeyErrors(v, "body")...)
		default:
			if path := firstItemReferencePath(v, k); path != "" {
				errs = append(errs, fmt.Sprintf("${item...} in '%s': item values can only appear in %s", path, apiItemPlaces))
			}
		}
	}
	return errs
}

// urlItemPositionError refuses an item reference in a url's scheme, host,
// port, user or fragment.
func urlItemPositionError(raw string) error {
	if !containsItemReference(raw) {
		return nil
	}
	scheme := strings.Index(raw, "://")
	if scheme < 0 {
		return fmt.Errorf("'url' has ${item...} references but no scheme and host: write the full url, such as https://api.example.com/customers/${item.id}")
	}
	authorityEnd := len(raw)
	if i := strings.IndexAny(raw[scheme+3:], "/?#"); i >= 0 {
		authorityEnd = scheme + 3 + i
	}
	if containsItemReference(raw[:authorityEnd]) {
		return fmt.Errorf("'url': an item value cannot choose the scheme, host or port of the request; it can only be in the path or query")
	}
	if hash := strings.IndexByte(raw[authorityEnd:], '#'); hash >= 0 && containsItemReference(raw[authorityEnd+hash:]) {
		return fmt.Errorf("'url': an item value cannot be in the fragment, which is never sent")
	}
	return nil
}

// jsonBodyKeyErrors refuses item references in the names of a JSON
// body's fields.
func jsonBodyKeyErrors(v interface{}, path string) []string {
	var errs []string
	switch x := v.(type) {
	case map[string]interface{}:
		for k, child := range x {
			if containsItemReference(k) {
				errs = append(errs, fmt.Sprintf("'%s' field name %q: an item value cannot name a field; it can only be a value", path, k))
			}
			errs = append(errs, jsonBodyKeyErrors(child, path+"."+k)...)
		}
	case []interface{}:
		for i, child := range x {
			errs = append(errs, jsonBodyKeyErrors(child, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return errs
}

// bindAPIItem returns a copy of cfg with every ${item...} reference
// replaced by item's value, encoded for where it is. It refuses a
// reference in a place apiItemPositionErrors refuses, a reference to a
// column the item lacks or holds null, and a path value of "." or "..".
func bindAPIItem(cfg map[string]interface{}, item common.DataRow) (map[string]interface{}, error) {
	if errs := apiItemPositionErrors(cfg); len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		out[k] = v
	}
	if raw, ok := cfg["url"].(string); ok && containsItemReference(raw) {
		bound, err := bindURLItem(raw, item)
		if err != nil {
			return nil, err
		}
		out["url"] = bound
	}
	for _, k := range []string{"params", "headers"} {
		m, ok := cfg[k].(map[string]interface{})
		if !ok {
			continue
		}
		bound := make(map[string]interface{}, len(m))
		for name, value := range m {
			text, isText := value.(string)
			if !isText || !containsItemReference(text) {
				bound[name] = value
				continue
			}
			s, err := substituteItemText(text, item, func(s string) string { return s })
			if err != nil {
				return nil, fmt.Errorf("'%s.%s': %w", k, name, err)
			}
			if k == "headers" && strings.ContainsAny(s, "\r\n\x00") {
				return nil, fmt.Errorf("'headers.%s': the item's value has a line break or NUL, which a header cannot hold", name)
			}
			bound[name] = s
		}
		out[k] = bound
	}
	if body, ok := cfg["body"]; ok {
		bound, err := bindJSONItem(body, item, "body")
		if err != nil {
			return nil, err
		}
		out["body"] = bound
	}
	return out, nil
}

// bindURLItem substitutes item values into a url's path and query, each
// percent-encoded for its part.
func bindURLItem(raw string, item common.DataRow) (string, error) {
	scheme := strings.Index(raw, "://")
	pathStart := len(raw)
	if i := strings.IndexAny(raw[scheme+3:], "/?#"); i >= 0 {
		pathStart = scheme + 3 + i
	}
	queryStart := len(raw)
	if i := strings.IndexAny(raw[pathStart:], "?#"); i >= 0 {
		queryStart = pathStart + i
	}
	path, err := substituteItemText(raw[pathStart:queryStart], item, func(s string) string { return url.PathEscape(s) })
	if err != nil {
		return "", fmt.Errorf("'url': %w", err)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("'url': an item value made the path segment %q, which would move the request to another path", segment)
		}
	}
	query, err := substituteItemText(raw[queryStart:], item, url.QueryEscape)
	if err != nil {
		return "", fmt.Errorf("'url': %w", err)
	}
	return raw[:pathStart] + path + query, nil
}

// bindJSONItem substitutes item values into a JSON body: a string that is
// exactly one reference becomes the item's value with its type, and a
// reference inside a longer string becomes text.
func bindJSONItem(v interface{}, item common.DataRow, path string) (interface{}, error) {
	switch x := v.(type) {
	case string:
		if !containsItemReference(x) {
			return x, nil
		}
		if refs := itemReferences(x); len(refs) == 1 && refs[0] == x {
			value, err := itemValue(x, item)
			if err != nil {
				return nil, fmt.Errorf("'%s': %w", path, err)
			}
			return value, nil
		}
		s, err := substituteItemText(x, item, func(s string) string { return s })
		if err != nil {
			return nil, fmt.Errorf("'%s': %w", path, err)
		}
		return s, nil
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, child := range x {
			bound, err := bindJSONItem(child, item, path+"."+k)
			if err != nil {
				return nil, err
			}
			out[k] = bound
		}
		return out, nil
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, child := range x {
			bound, err := bindJSONItem(child, item, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = bound
		}
		return out, nil
	}
	return v, nil
}

// substituteItemText replaces each reference in s with encode(the item's
// value as text).
func substituteItemText(s string, item common.DataRow, encode func(string) string) (string, error) {
	var b strings.Builder
	rest := s
	for {
		loc := itemReferenceRegexp.FindStringIndex(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String(), nil
		}
		value, err := itemValue(rest[loc[0]:loc[1]], item)
		if err != nil {
			return "", err
		}
		b.WriteString(rest[:loc[0]])
		b.WriteString(encode(itemValueText(value)))
		rest = rest[loc[1]:]
	}
}

// itemValue is the item's value for one ${item.column} reference, refusing
// filters, a missing column and null.
func itemValue(ref string, item common.DataRow) (interface{}, error) {
	if err := itemReferenceErrors(ref, item); err != nil {
		return nil, err
	}
	return item[strings.TrimPrefix(ref[2:len(ref)-1], "item.")], nil
}

func containsItemReference(s string) bool { return itemReferenceRegexp.MatchString(s) }

func itemReferences(s string) []string { return itemReferenceRegexp.FindAllString(s, -1) }

// firstItemReferencePath returns the config path of the first ${item...}
// reference in v, or "".
func firstItemReferencePath(v interface{}, path string) string {
	switch x := v.(type) {
	case string:
		if containsItemReference(x) {
			return path
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := k
			if path != "" {
				child = path + "." + k
			}
			if containsItemReference(k) {
				return child
			}
			if p := firstItemReferencePath(x[k], child); p != "" {
				return p
			}
		}
	case []interface{}:
		for i, c := range x {
			if p := firstItemReferencePath(c, fmt.Sprintf("%s[%d]", path, i)); p != "" {
				return p
			}
		}
	}
	return ""
}

// runMappedSourceAPI runs a mapped source_api node: one request (or one
// paginated fetch) per item, through fanOut, each item its own instance.
// Items run on this runner, not as remote pagination instances, whose
// instance keys belong to the node.
func (r *Runner) runMappedSourceAPI(ctx context.Context, node models.Node, edgeInputsByFrom map[string]*common.DataSet, nodeAttempt int) (nodeExecutionResult, error) {
	if errs := mappedAPIErrors(node); len(errs) > 0 {
		return nodeExecutionResult{}, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	cfg, err := parseMapConfig(node)
	if err != nil {
		return nodeExecutionResult{}, err
	}
	items, _, err := resolveExpansionItems(node, cfg, edgeInputsByFrom)
	if err != nil {
		return nodeExecutionResult{}, err
	}
	if r.dryRun && len(items) > dryRunMappedItems {
		r.log(node.ID, models.LogLevelInfo, "dry run: mapping the first %d of %d item(s)", dryRunMappedItems, len(items))
		items = items[:dryRunMappedItems]
	}
	timeoutSec := defaultMappedSourceTimeoutSec
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	// With requests_per_second (and so one item at a time) the pause
	// between requests also separates the last request of one item from
	// the first of the next, so the rate holds across items.
	var pause time.Duration
	if execCfg, ok := node.Config["execution"].(map[string]interface{}); ok {
		if rps, ok := numberOption(execCfg["requests_per_second"]); ok && rps > 0 {
			pause = time.Duration(float64(time.Second) / rps)
		}
	}
	output, err := r.fanOut(ctx, node, cfg, items, nodeAttempt, false, func(ctx context.Context, i int, key string) (*common.DataSet, error) {
		if pause > 0 && i > 0 {
			select {
			case <-time.After(pause):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return r.executeMappedInstance(ctx, node, nodeAttempt, i, key, len(items), timeoutSec, false, func(ctx context.Context, _ int64, _ bool) (*common.DataSet, string, error) {
			ds, err := r.runSourceAPI(withMapItem(ctx, items[i]), node)
			return ds, "", err
		})
	})
	if err != nil {
		return nodeExecutionResult{}, err
	}
	return nodeExecutionResult{output: output}, nil
}

// numberOption reads a numeric config value decoded from JSON.
func numberOption(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}
