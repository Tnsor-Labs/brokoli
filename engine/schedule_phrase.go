package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Plain-language schedules, compiled to cron.
//
// Cron stays the stored format: the scheduler, backfill, catch-up and
// ADR-028's data intervals all consume it, so a phrase is an input
// method and never a persisted value (#552).
//
// The grammar is a closed set. Anything outside it is refused with a
// reason, rather than rounded to the nearest expressible schedule.
// Rounding would mean a pipeline running on a cadence nobody asked for,
// discoverable only by watching it, which is the failure mode this
// codebase keeps finding.

// PhraseError is a refusal, carrying why and what to try instead.
//
// Suggestion is empty when there is no near miss worth naming: for
// "weekdays except holidays" there is no cron that comes close, and
// inventing one would be the rounding this grammar exists to avoid.
type PhraseError struct {
	Reason     string
	Suggestion string
}

func (e *PhraseError) Error() string { return e.Reason }

func refuse(reason, suggestion string) error {
	return &PhraseError{Reason: reason, Suggestion: suggestion}
}

var (
	// "at 9am", "at 9:30 am", "at 09:00", "at 18:00"
	timeRe = regexp.MustCompile(`(?i)\bat\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b`)
	// "every 15 minutes", "every 2 hours"
	everyNRe = regexp.MustCompile(`(?i)^every\s+(\d+)\s*(minute|minutes|min|mins|hour|hours|hr|hrs)$`)
	// "every hour at :15"
	hourlyAtRe = regexp.MustCompile(`(?i)^every\s+hour(?:\s+at\s+:?(\d{1,2}))?$`)
	// "on the 1st", "on the 1st and 15th", "on the 1st, 15th and 28th"
	ordinalRe = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)\b`)
)

var dayNumbers = map[string]int{
	"sunday": 0, "sun": 0,
	"monday": 1, "mon": 1,
	"tuesday": 2, "tue": 2, "tues": 2,
	"wednesday": 3, "wed": 3,
	"thursday": 4, "thu": 4, "thur": 4, "thurs": 4,
	"friday": 5, "fri": 5,
	"saturday": 6, "sat": 6,
}

var dayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// CompilePhrase turns a plain-language schedule into a cron expression.
//
// An input that is already valid cron is returned unchanged, so the
// caller does not have to know which form the user typed.
func CompilePhrase(input string) (string, error) {
	s := strings.ToLower(strings.Join(strings.Fields(input), " "))
	if s == "" {
		return "", refuse("a schedule is required", "")
	}

	// Already cron? The scheduler's own parser decides, so this can never
	// disagree with what will actually be registered.
	if _, err := scheduleFor(input, ""); err == nil {
		return strings.Join(strings.Fields(input), " "), nil
	}

	if cronExpr, err := compileKnownPhrase(s); err != nil || cronExpr != "" {
		return cronExpr, err
	}

	// Not cron, and nothing in the grammar matched. If it had the shape of
	// cron, the cron parser's own complaint is the more useful one.
	if len(strings.Fields(input)) == 5 {
		_, cronErr := scheduleFor(input, "")
		return "", refuse(fmt.Sprintf("not a valid cron expression: %v", cronErr), "")
	}
	return "", refuse(
		fmt.Sprintf("%q is not a schedule this grammar understands", strings.TrimSpace(input)),
		`try "every day at 9am", "every weekday at 6:30", "every 15 minutes", or a cron expression`)
}

// compileKnownPhrase returns ("", nil) when nothing matched, so the
// caller can tell "not understood" from "understood and refused".
func compileKnownPhrase(s string) (string, error) {
	if err := refuseKnownImpossibles(s); err != nil {
		return "", err
	}

	// every N minutes / every N hours
	if m := everyNRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := strings.TrimSuffix(strings.TrimSuffix(m[2], "s"), "e")
		switch {
		case strings.HasPrefix(unit, "min"):
			return everyNMinutes(n)
		default:
			return everyNHours(n)
		}
	}

	// every hour [at :MM]
	if m := hourlyAtRe.FindStringSubmatch(s); m != nil {
		minute := 0
		if m[1] != "" {
			minute, _ = strconv.Atoi(m[1])
			if minute > 59 {
				return "", refuse(fmt.Sprintf("there is no minute %d in an hour", minute), "")
			}
		}
		return fmt.Sprintf("%d * * * *", minute), nil
	}

	// Everything below needs a time of day.
	hour, minute, hasTime, err := parseTimeOfDay(s)
	if err != nil {
		return "", err
	}

	// on the 1st [and 15th] at TIME
	if strings.HasPrefix(s, "on the ") || strings.HasPrefix(s, "monthly") ||
		strings.Contains(s, "of the month") {
		days := ordinalRe.FindAllStringSubmatch(s, -1)
		if len(days) == 0 {
			return "", refuse("no day of the month was given", `try "on the 1st at 3am"`)
		}
		if !hasTime {
			return "", refuse("a time of day is required", `try "on the 1st at 3am"`)
		}
		nums := make([]string, 0, len(days))
		for _, d := range days {
			n, _ := strconv.Atoi(d[1])
			if n < 1 || n > 31 {
				return "", refuse(fmt.Sprintf("there is no day %d in a month", n), "")
			}
			nums = append(nums, strconv.Itoa(n))
		}
		return fmt.Sprintf("%d %d %s * *", minute, hour, strings.Join(nums, ",")), nil
	}

	if !hasTime {
		return "", nil
	}

	switch {
	case strings.HasPrefix(s, "every day"), strings.HasPrefix(s, "daily"):
		return fmt.Sprintf("%d %d * * *", minute, hour), nil
	case strings.HasPrefix(s, "every weekday"), strings.HasPrefix(s, "weekdays"):
		return fmt.Sprintf("%d %d * * 1-5", minute, hour), nil
	case strings.HasPrefix(s, "every weekend"), strings.HasPrefix(s, "weekends"):
		return fmt.Sprintf("%d %d * * 0,6", minute, hour), nil
	}

	// every <day>[,<day>...] at TIME
	if days := parseDayList(s); len(days) > 0 {
		parts := make([]string, len(days))
		for i, d := range days {
			parts[i] = strconv.Itoa(d)
		}
		return fmt.Sprintf("%d %d * * %s", minute, hour, strings.Join(parts, ",")), nil
	}

	return "", nil
}

// refuseKnownImpossibles names the phrases someone will reasonably type
// that cron cannot express. Each is refused with what it would have
// meant, because "not understood" reads as a parser limitation while
// "cron cannot do this" is the actual truth.
func refuseKnownImpossibles(s string) error {
	switch {
	case strings.Contains(s, "other ") && strings.HasPrefix(s, "every"):
		return refuse("cron repeats every week, so it cannot express a fortnightly schedule",
			`use "every tuesday" and skip alternate runs in the pipeline`)
	case strings.Contains(s, "last day"), strings.Contains(s, "last weekday"):
		return refuse("the last day of a month needs cron's L, which this scheduler does not accept",
			`try "on the 28th at 3am", or check the date inside the pipeline`)
	case strings.Contains(s, "holiday"):
		return refuse("cron has no calendar, so holidays cannot be excluded in a schedule",
			"gate the run on a holiday check inside the pipeline")
	case strings.HasPrefix(s, "tomorrow"), strings.HasPrefix(s, "today"),
		strings.HasPrefix(s, "in "), strings.HasPrefix(s, "next "):
		return refuse("a schedule repeats; this describes a single moment",
			"use Run for a one-off, or describe the recurrence")
	}
	return nil
}

func everyNMinutes(n int) (string, error) {
	if n <= 0 {
		return "", refuse("an interval must be at least one minute", "")
	}
	if n >= 60 || 60%n != 0 {
		return "", refuse(
			fmt.Sprintf("every %d minutes cannot be expressed as a cron schedule, because it does not divide an hour evenly", n),
			fmt.Sprintf("the nearest options are %q and %q", nearestMinuteDivisor(n, -1), nearestMinuteDivisor(n, 1)))
	}
	return fmt.Sprintf("*/%d * * * *", n), nil
}

func everyNHours(n int) (string, error) {
	if n <= 0 {
		return "", refuse("an interval must be at least one hour", "")
	}
	if n == 1 {
		return "0 * * * *", nil
	}
	if n >= 24 || 24%n != 0 {
		return "", refuse(
			fmt.Sprintf("every %d hours cannot be expressed as a cron schedule, because it does not divide a day evenly and would drift", n),
			fmt.Sprintf("the nearest options are %q and %q", nearestHourDivisor(n, -1), nearestHourDivisor(n, 1)))
	}
	return fmt.Sprintf("0 */%d * * *", n), nil
}

func nearestMinuteDivisor(n, dir int) string {
	divisors := []int{1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30}
	return nearestFrom(divisors, n, dir, "every %d minutes", "every hour")
}

func nearestHourDivisor(n, dir int) string {
	divisors := []int{1, 2, 3, 4, 6, 8, 12}
	return nearestFrom(divisors, n, dir, "every %d hours", "every day at 00:00")
}

// nearestFrom picks the closest divisor below (dir<0) or above (dir>0)
// n, falling back to overflow when nothing in the set is on that side.
func nearestFrom(divisors []int, n, dir int, format, overflow string) string {
	best := 0
	for _, d := range divisors {
		if dir < 0 && d < n && d > best {
			best = d
		}
		if dir > 0 && d > n && (best == 0 || d < best) {
			best = d
		}
	}
	if best == 0 {
		return overflow
	}
	if best == 1 && strings.Contains(format, "hour") {
		return "every hour"
	}
	return fmt.Sprintf(format, best)
}

// parseTimeOfDay reads "at 9am" / "at 09:00" and reports whether one was
// present at all, so callers can distinguish "no time given" from
// midnight.
func parseTimeOfDay(s string) (hour, minute int, ok bool, err error) {
	m := timeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false, nil
	}
	hour, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	meridiem := strings.ToLower(m[3])

	switch {
	case meridiem == "am":
		if hour < 1 || hour > 12 {
			return 0, 0, false, refuse(fmt.Sprintf("there is no %d am", hour), "")
		}
		if hour == 12 {
			hour = 0
		}
	case meridiem == "pm":
		if hour < 1 || hour > 12 {
			return 0, 0, false, refuse(fmt.Sprintf("there is no %d pm", hour), "")
		}
		if hour != 12 {
			hour += 12
		}
	default:
		if hour > 23 {
			return 0, 0, false, refuse(fmt.Sprintf("there is no hour %d in a day", hour), "")
		}
	}
	if minute > 59 {
		return 0, 0, false, refuse(fmt.Sprintf("there is no minute %d in an hour", minute), "")
	}
	return hour, minute, true, nil
}

// parseDayList reads the weekday names in a phrase, in calendar order
// and without duplicates, so "every fri,mon at 9am" and "every mon,fri
// at 9am" compile to the same expression.
func parseDayList(s string) []int {
	head := s
	if i := timeRe.FindStringIndex(s); i != nil {
		head = s[:i[0]]
	}
	head = strings.TrimPrefix(head, "every ")

	seen := map[int]bool{}
	for _, tok := range strings.FieldsFunc(head, func(r rune) bool {
		return r == ',' || r == ' ' || r == '/'
	}) {
		tok = strings.TrimSpace(tok)
		if tok == "and" || tok == "" {
			continue
		}
		n, ok := dayNumbers[tok]
		if !ok {
			return nil // an unknown word means this is not a day list
		}
		seen[n] = true
	}
	out := make([]int, 0, len(seen))
	for d := 0; d <= 6; d++ {
		if seen[d] {
			out = append(out, d)
		}
	}
	return out
}

// DescribeCron renders a cron expression as the sentence the grammar
// would have accepted, or "" when it is outside the closed set.
//
// Empty rather than a guess: the echo shows the raw expression when this
// returns nothing, which is honest. A wrong description would be worse
// than none, because the whole point of the echo is to be trusted.
func DescribeCron(expr string) string {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return ""
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	if month != "*" {
		return ""
	}

	// every N minutes
	if strings.HasPrefix(minute, "*/") && hour == "*" && dom == "*" && dow == "*" {
		return "Every " + strings.TrimPrefix(minute, "*/") + " minutes"
	}
	// hourly, on the hour or at :MM
	if hour == "*" && dom == "*" && dow == "*" {
		if m, err := strconv.Atoi(minute); err == nil {
			if m == 0 {
				return "Every hour"
			}
			return fmt.Sprintf("Every hour at :%02d", m)
		}
		return ""
	}
	// every N hours
	if strings.HasPrefix(hour, "*/") && dom == "*" && dow == "*" {
		if m, err := strconv.Atoi(minute); err == nil && m == 0 {
			return "Every " + strings.TrimPrefix(hour, "*/") + " hours"
		}
		return ""
	}

	at, ok := clockText(minute, hour)
	if !ok {
		return ""
	}
	switch {
	case dom == "*" && dow == "*":
		return "Every day at " + at
	case dom == "*" && dow == "1-5":
		return "Weekdays at " + at
	case dom == "*" && (dow == "0,6" || dow == "6,0"):
		return "Weekends at " + at
	case dom == "*":
		names := weekdayNames(dow)
		if names == "" {
			return ""
		}
		return names + " at " + at
	case dow == "*":
		days := strings.Split(dom, ",")
		for _, d := range days {
			if _, err := strconv.Atoi(d); err != nil {
				return ""
			}
		}
		return "Monthly on the " + joinOrdinals(days) + " at " + at
	}
	return ""
}

func clockText(minute, hour string) (string, bool) {
	m, err1 := strconv.Atoi(minute)
	h, err2 := strconv.Atoi(hour)
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

func weekdayNames(dow string) string {
	parts := strings.Split(dow, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 6 {
			return ""
		}
		names = append(names, dayNames[n])
	}
	return strings.Join(names, ", ")
}

func joinOrdinals(days []string) string {
	out := make([]string, len(days))
	for i, d := range days {
		n, _ := strconv.Atoi(d)
		out[i] = ordinal(n)
	}
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// NextOccurrences returns the next n fire times for an expression in a
// timezone, using the scheduler's own parser and its own timezone
// handling. A preview computed any other way could disagree with what
// actually runs, which would make it worse than no preview.
func NextOccurrences(expr, timezone string, n int, from time.Time) ([]time.Time, error) {
	sched, err := scheduleFor(expr, timezone)
	if err != nil {
		return nil, err
	}
	loc := time.UTC
	if timezone != "" {
		if l, lerr := time.LoadLocation(timezone); lerr == nil {
			loc = l
		}
	}
	out := make([]time.Time, 0, n)
	t := from
	for i := 0; i < n; i++ {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t.In(loc))
	}
	return out, nil
}
