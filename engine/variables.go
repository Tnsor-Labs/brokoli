package engine

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

var varPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// VariableStore is the interface for resolving stored variables.
// This avoids importing the store package (which would create a cycle).
type VariableStore interface {
	GetVariableValue(workspaceID, key string) (value string, encrypted bool, err error)
}

// VariableContext holds the runtime context for variable resolution.
type VariableContext struct {
	Env    map[string]string // from os.Environ
	Params map[string]string // from pipeline run params
	Vars   VariableStore     // stored variables (${var.key})
	// WorkspaceID scopes ${var.*} to the pipeline's own workspace.
	// Empty means the default workspace, matching the store.
	WorkspaceID string
	RunID       string
	StartedAt   time.Time

	// IntervalStart/End are the run's data interval (ADR-028), resolvable
	// as ${interval.start} and ${interval.end} in RFC3339 UTC. Nil in a
	// run with no interval, in which case the references stay unresolved
	// -- the same visible-in-output behaviour every unknown variable has,
	// and validation warns about the combination up front.
	IntervalStart *time.Time
	IntervalEnd   *time.Time
}

// NewVariableContext creates a context from the current environment and params.
func NewVariableContext(params map[string]string, runID string, startedAt time.Time) *VariableContext {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}
	if params == nil {
		params = make(map[string]string)
	}
	return &VariableContext{
		Env:       env,
		Params:    params,
		RunID:     runID,
		StartedAt: startedAt,
	}
}

// Resolve replaces all ${...} variables in a string.
func (vc *VariableContext) Resolve(s string) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract the key between ${ and }
		key := match[2 : len(match)-1]
		return vc.resolveKey(key)
	})
}

// filterSep separates a reference from its filters inside ${...}, as in
// ${interval.start|shift:-1d|date:YYYYMMDD}. Filters apply left to right.
const filterSep = "|"

func (vc *VariableContext) resolveKey(key string) string {
	if !strings.Contains(key, filterSep) {
		return vc.resolveReference(key)
	}
	return vc.resolveFiltered(key)
}

func (vc *VariableContext) resolveReference(key string) string {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return "${" + key + "}" // unresolvedreturn as-is
	}

	prefix, name := parts[0], parts[1]
	switch prefix {
	case "interval":
		// ADR-028: the run's data interval, RFC3339 UTC. Unresolved (the
		// reference stays visible) when the run carries no interval --
		// an empty string here would quietly produce WHERE ts >= '',
		// which is the silent kind of wrong; the visible kind gets fixed.
		switch name {
		case "start":
			if vc.IntervalStart != nil {
				return vc.IntervalStart.UTC().Format(time.RFC3339)
			}
		case "end":
			if vc.IntervalEnd != nil {
				return vc.IntervalEnd.UTC().Format(time.RFC3339)
			}
		}
		return "${" + key + "}"
	case "env":
		// Deny by default. NewVariableContext copies the WHOLE server
		// environment into vc.Env, so before this gate ${env.NAME}
		// returned any of it -- including BROKOLI_ENCRYPTION_KEY, which
		// decrypts every stored credential, and BROKOLI_JWT_SECRET, which
		// mints any session. Authoring a pipeline is not supposed to be a
		// way to read the control plane's own secrets, and it was.
		//
		// A refused reference stays VISIBLE as ${env.NAME} rather than
		// resolving to empty, for the reason the interval case above
		// gives: an empty string is the silent kind of wrong, and the
		// visible kind gets fixed. A name that is allowed but genuinely
		// unset still resolves to empty, which is what it always did.
		if !pipelineEnvAllowed(name) {
			log.Printf("[variables] refusing ${env.%s}: not in %s. "+
				"Pipeline templating reads only environment variables an operator has "+
				"listed there; the server's own environment holds its database URL, "+
				"signing secret and encryption key.", name, pipelineEnvAllowEnv)
			return "${" + key + "}"
		}
		if v, ok := vc.Env[name]; ok {
			return v
		}
		return ""
	case "param":
		if v, ok := vc.Params[name]; ok {
			return v
		}
		return ""
	case "secret":
		// Secrets resolve from env vars with BROKED_SECRET_ prefix
		if v, ok := vc.Env["BROKED_SECRET_"+strings.ToUpper(name)]; ok {
			return v
		}
		return ""
	case "var":
		// Resolve from stored variables
		if vc.Vars != nil {
			// Scoped to the run's workspace. Reading by key alone
			// returned whichever workspace had written that name last,
			// and variables hold secrets.
			if val, _, err := vc.Vars.GetVariableValue(vc.WorkspaceID, name); err == nil {
				return val
			}
		}
		return ""
	case "run":
		switch name {
		case "id":
			return vc.RunID
		case "started_at":
			return vc.StartedAt.Format(time.RFC3339)
		case "date":
			return vc.StartedAt.Format("2006-01-02")
		case "timestamp":
			return fmt.Sprintf("%d", vc.StartedAt.Unix())
		}
	}
	return "${" + key + "}"
}

// resolveFiltered resolves ${reference|filter|...}.
//
// Filters exist so a pipeline can name a file or a partition after the
// interval it covers -- orders-20240314.csv rather than
// orders-2024-03-14T00:00:00Z.csv -- and so it can ask for the day before
// without a second variable carrying it.
//
// Every refusal below returns the WHOLE reference visibly unresolved, for
// the reason the interval and env cases give above: a filter that quietly
// produced the wrong timestamp would name a file after a day that is not
// the day its data covers, and nobody would notice until the partner did.
func (vc *VariableContext) resolveFiltered(key string) string {
	parts := strings.Split(key, filterSep)
	visible := "${" + key + "}"

	t, ok := vc.timeValue(strings.TrimSpace(parts[0]))
	if !ok {
		// Two cases land here, and neither is logged. A filter on
		// something that is not a timestamp (${param.x|date:YYYY}) is an
		// authoring mistake the visible reference already reports. An
		// interval the run does not have is ordinary -- manual runs carry
		// none -- and ResolveConfig walks every string of every node on
		// every run, so logging it would print a line per config value.
		return visible
	}

	rendered := ""
	done := false
	for _, f := range parts[1:] {
		f = strings.TrimSpace(f)
		if done {
			return refuseFilter(key, fmt.Sprintf("filter %q comes after a date: that already rendered the value as text", f))
		}
		name, arg, found := strings.Cut(f, ":")
		if !found {
			return refuseFilter(key, fmt.Sprintf("filter %q takes an argument, as in shift:-1d or date:YYYYMMDD", f))
		}
		switch name {
		case "shift":
			d, ok := parseShift(strings.TrimSpace(arg))
			if !ok {
				return refuseFilter(key, fmt.Sprintf("%q is not a shift; write a signed count and one of s m h d w, as in shift:-1d", arg))
			}
			t = t.Add(d)
		case "date":
			s, ok := formatDateLayout(t, arg)
			if !ok {
				return refuseFilter(key, fmt.Sprintf("%q is not a date layout; the tokens are YYYY YY MM DD HH mm ss, and a literal may not contain the letters Y M D H S", arg))
			}
			rendered, done = s, true
		default:
			return refuseFilter(key, fmt.Sprintf("unknown filter %q", name))
		}
	}
	if done {
		return rendered
	}
	// Shifted but never formatted: render exactly as the bare reference
	// would, so ${interval.start|shift:0s} and ${interval.start} agree.
	return t.Format(time.RFC3339)
}

// timeValue returns the instant a reference names, for the references that
// name one.
//
// The zone matches what the bare reference prints, so a filter changes the
// format and never the clock: ${interval.*} is UTC because ADR-028 pins the
// interval in UTC, and ${run.started_at} keeps the zone it has always
// printed.
//
// ${run.date} is deliberately absent. It is already a rendered string, and
// the instant behind it is ${run.started_at} -- one timestamp per source,
// so there is no ${run.date|date:HHmm} to puzzle over.
func (vc *VariableContext) timeValue(ref string) (time.Time, bool) {
	switch ref {
	case "interval.start":
		if vc.IntervalStart != nil {
			return vc.IntervalStart.UTC(), true
		}
	case "interval.end":
		if vc.IntervalEnd != nil {
			return vc.IntervalEnd.UTC(), true
		}
	case "run.started_at":
		return vc.StartedAt, true
	}
	return time.Time{}, false
}

// parseShift parses a signed offset: a count and one of s m h d w.
//
// No months or years. They are not fixed durations, and "the 31st minus one
// month" has no answer this resolver could give without inventing a
// convention the author never chose. Chain shifts for a compound offset:
// |shift:-1d|shift:-12h.
func parseShift(arg string) (time.Duration, bool) {
	neg := false
	switch {
	case strings.HasPrefix(arg, "-"):
		neg, arg = true, arg[1:]
	case strings.HasPrefix(arg, "+"):
		arg = arg[1:]
	}
	if len(arg) < 2 {
		return 0, false
	}
	var unit time.Duration
	switch arg[len(arg)-1] {
	case 's':
		unit = time.Second
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		return 0, false
	}
	n, err := strconv.ParseInt(arg[:len(arg)-1], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	d := time.Duration(n) * unit
	if n != 0 && d/unit != time.Duration(n) {
		return 0, false // overflowed int64 nanoseconds
	}
	if neg {
		d = -d
	}
	return d, true
}

// dateTokenLetters are the letters a token is built from, and so the
// letters a literal may not contain.
//
// "YYYY-mm-DD" is a plausible typo for the month token, and rendering it
// would produce 2024-30-14: a real-looking date with the minutes in the
// middle of it. Refusing is visible, and that is not.
const dateTokenLetters = "YyMmDdHhSs"

// formatDateLayout renders t through a layout of tokens -- YYYY YY MM DD HH
// mm ss -- copying every other byte literally. Tokens match greedily from
// the left, so YYYY wins over YY.
//
// There is no escape for a literal date letter, and there does not need to
// be one: the text around a reference is already literal, so write
// orders-DD-${interval.start|date:YYYYMMDD}.csv and keep the D outside.
func formatDateLayout(t time.Time, layout string) (string, bool) {
	if layout == "" {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(layout); {
		if strings.HasPrefix(layout[i:], "YYYY") {
			fmt.Fprintf(&b, "%04d", t.Year())
			i += 4
			continue
		}
		if i+2 <= len(layout) {
			matched := true
			switch layout[i : i+2] {
			case "YY":
				fmt.Fprintf(&b, "%02d", t.Year()%100)
			case "MM":
				fmt.Fprintf(&b, "%02d", int(t.Month()))
			case "DD":
				fmt.Fprintf(&b, "%02d", t.Day())
			case "HH":
				fmt.Fprintf(&b, "%02d", t.Hour())
			case "mm":
				fmt.Fprintf(&b, "%02d", t.Minute())
			case "ss":
				fmt.Fprintf(&b, "%02d", t.Second())
			default:
				matched = false
			}
			if matched {
				i += 2
				continue
			}
		}
		if strings.IndexByte(dateTokenLetters, layout[i]) >= 0 {
			return "", false
		}
		b.WriteByte(layout[i])
		i++
	}
	return b.String(), true
}

// refuseFilter logs a malformed filter and hands back the visible
// reference, in the voice the ${env.*} refusal above uses.
func refuseFilter(key, reason string) string {
	log.Printf("[variables] refusing ${%s}: %s. Filters read the run's own "+
		"timestamps -- ${interval.start}, ${interval.end}, ${run.started_at} -- "+
		"and a reference this resolver cannot satisfy stays visible in the output "+
		"rather than naming a file after the wrong day.", key, reason)
	return "${" + key + "}"
}

// ResolveConfig deep-resolves all string values in a config map.
func (vc *VariableContext) ResolveConfig(config map[string]interface{}) map[string]interface{} {
	resolved := make(map[string]interface{}, len(config))
	for k, v := range config {
		resolved[k] = vc.resolveValue(v)
	}
	return resolved
}

func (vc *VariableContext) resolveValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return vc.Resolve(val)
	case map[string]interface{}:
		return vc.ResolveConfig(val)
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = vc.resolveValue(item)
		}
		return result
	default:
		return v
	}
}

// pipelineEnvAllowEnv names the operator's allowlist for ${env.*}.
//
// An allowlist rather than a denylist, because a denylist has to
// enumerate every secret anyone will ever put in the environment and is
// wrong the first time someone adds one. This is wrong only in the
// direction of refusing something harmless, which is visible and takes
// one setting to fix.
const pipelineEnvAllowEnv = "BROKOLI_PIPELINE_ENV_ALLOW"

// pipelineEnvAllowed reports whether a pipeline may read this environment
// variable through ${env.*}.
func pipelineEnvAllowed(name string) bool {
	// The never-readable floor lives in pkg/secrets so the three
	// mechanisms that can reach the server's environment -- this,
	// env:// references and a code node's inherited environment -- cannot
	// drift apart on which names are fatal.
	if name == "" || secrets.AlwaysDeniedEnvName(name) {
		return false
	}
	for _, allowed := range strings.Split(os.Getenv(pipelineEnvAllowEnv), ",") {
		if allowed = strings.TrimSpace(allowed); allowed != "" && allowed == name {
			return true
		}
	}
	return false
}
