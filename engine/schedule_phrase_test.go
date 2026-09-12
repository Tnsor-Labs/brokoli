package engine

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The accepted grammar, exactly as pinned in #552. Every row is a phrase
// someone types and the cron it must produce.
func TestCompilePhraseAcceptedGrammar(t *testing.T) {
	cases := map[string]string{
		// daily
		"every day at 9am":      "0 9 * * *",
		"daily at 09:00":        "0 9 * * *",
		"every day at 12am":     "0 0 * * *",
		"every day at 12pm":     "0 12 * * *",
		"every day at 6:30pm":   "30 18 * * *",
		"EVERY DAY AT 9AM":      "0 9 * * *",
		"  every   day at 9am ": "0 9 * * *",

		// weekday sets
		"every weekday at 9am":       "0 9 * * 1-5",
		"weekdays at 9am":            "0 9 * * 1-5",
		"every weekend at 10am":      "0 10 * * 0,6",
		"every monday at 6:30":       "30 6 * * 1",
		"every mon,wed,fri at 18:00": "0 18 * * 1,3,5",
		"every fri,mon at 9am":       "0 9 * * 1,5", // normalised to calendar order

		// intervals
		"every hour":        "0 * * * *",
		"every hour at :15": "15 * * * *",
		"every 15 minutes":  "*/15 * * * *",
		"every 30 mins":     "*/30 * * * *",
		"every 2 hours":     "0 */2 * * *",
		"every 1 hour":      "0 * * * *",

		// day of month
		"on the 1st at 3am":          "0 3 1 * *",
		"on the 1st and 15th at 3am": "0 3 1,15 * *",

		// cron passes through untouched
		"0 2 * * *":       "0 2 * * *",
		"*/5 * * * *":     "*/5 * * * *",
		"0 9 * * MON-FRI": "0 9 * * MON-FRI",
	}

	for phrase, want := range cases {
		t.Run(phrase, func(t *testing.T) {
			got, err := CompilePhrase(phrase)
			if err != nil {
				t.Fatalf("CompilePhrase(%q) refused: %v", phrase, err)
			}
			if got != want {
				t.Errorf("CompilePhrase(%q) = %q, want %q", phrase, got, want)
			}
			// Whatever the grammar produces must be something the
			// scheduler will actually accept.
			if _, err := scheduleFor(got, ""); err != nil {
				t.Errorf("produced %q, which the scheduler rejects: %v", got, err)
			}
		})
	}
}

// Refusals are the half that matters. A grammar tested only on what it
// accepts is indistinguishable from one that accepts everything and
// rounds.
func TestCompilePhraseRefusals(t *testing.T) {
	cases := map[string]struct {
		mustSay        string
		wantSuggestion bool
	}{
		"every 90 minutes":                     {"divide an hour evenly", true},
		"every 7 minutes":                      {"divide an hour evenly", true},
		"every 7 hours":                        {"drift", true},
		"every 5 hours":                        {"drift", true},
		"every other tuesday":                  {"fortnightly", true},
		"last day of the month at 9am":         {"cron's L", true},
		"every weekday except holidays at 9am": {"holidays", true},
		"tomorrow at 9am":                      {"single moment", true},
		"in 2 hours":                           {"single moment", true},
		"every day at 25:00":                   {"hour 25", false},
		"every day at 13pm":                    {"13 pm", false},
		"every day at 9:75":                    {"minute 75", false},
		"on the 45th at 3am":                   {"day 45", false},
		"purple monkey dishwasher":             {"not a schedule this grammar understands", true},
		"0 9 * * funday":                       {"not a valid cron expression", false},
		"":                                     {"a schedule is required", false},
	}

	for phrase, want := range cases {
		t.Run(phrase, func(t *testing.T) {
			got, err := CompilePhrase(phrase)
			if err == nil {
				t.Fatalf("CompilePhrase(%q) returned %q, want a refusal", phrase, got)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want.mustSay)) {
				t.Errorf("refusal for %q was %q, which does not mention %q", phrase, err, want.mustSay)
			}
			var pe *PhraseError
			if !errors.As(err, &pe) {
				t.Fatalf("refusal for %q is not a *PhraseError, so the UI cannot show a suggestion", phrase)
			}
			if want.wantSuggestion && pe.Suggestion == "" {
				t.Errorf("refusal for %q offers no suggestion", phrase)
			}
		})
	}
}

// A phrase that compiles must describe back to the same schedule, or the
// echo is telling the user something the scheduler will not do.
func TestDescribeCronRoundTrip(t *testing.T) {
	phrases := []string{
		"every day at 9am",
		"every weekday at 9am",
		"every weekend at 10am",
		"every monday at 6:30",
		"every mon,wed,fri at 18:00",
		"every hour",
		"every hour at :15",
		"every 15 minutes",
		"every 2 hours",
		"on the 1st at 3am",
		"on the 1st and 15th at 3am",
	}
	for _, p := range phrases {
		t.Run(p, func(t *testing.T) {
			expr, err := CompilePhrase(p)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			desc := DescribeCron(expr)
			if desc == "" {
				t.Fatalf("the grammar produced %q but cannot describe it back; the echo would show nothing", expr)
			}
			// Describing then recompiling must land on the same cron.
			back, err := CompilePhrase(strings.ToLower(desc))
			if err != nil {
				t.Skipf("description %q is prose the grammar does not re-accept, which is allowed", desc)
			}
			if back != expr {
				t.Errorf("round trip drifted: %q -> %q -> %q -> %q", p, expr, desc, back)
			}
		})
	}
}

// An expression outside the closed set must describe as nothing at all,
// so the echo falls back to showing the raw cron rather than a guess.
func TestDescribeCronRefusesToGuess(t *testing.T) {
	for _, expr := range []string{
		"0 9 1 1 *",       // a specific month
		"0 9 * * MON-FRI", // named days, valid cron but outside the set
		"0 9,17 * * *",    // two hours
		"not cron",
		"0 9 * *",
	} {
		if got := DescribeCron(expr); got != "" {
			t.Errorf("DescribeCron(%q) = %q, want \"\" so the echo shows the expression itself", expr, got)
		}
	}
}

func TestNextOccurrencesUsesTheTimezone(t *testing.T) {
	from := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

	utc, err := NextOccurrences("0 9 * * *", "", 3, from)
	if err != nil {
		t.Fatalf("utc: %v", err)
	}
	maputo, err := NextOccurrences("0 9 * * *", "Africa/Maputo", 3, from)
	if err != nil {
		t.Fatalf("maputo: %v", err)
	}
	if len(utc) != 3 || len(maputo) != 3 {
		t.Fatalf("wanted 3 occurrences each, got %d and %d", len(utc), len(maputo))
	}
	// 09:00 in Maputo is 07:00 UTC, so the two must not coincide.
	if utc[0].Equal(maputo[0]) {
		t.Errorf("the timezone was ignored: both first runs are %v", utc[0])
	}
	if h := maputo[0].Hour(); h != 9 {
		t.Errorf("first Maputo run is at %02d:00 local, want 09:00", h)
	}
}

// Across a spring-forward boundary the local clock time is what holds,
// which is the behaviour the scheduler already has. The preview has to
// show that rather than a UTC-shifted guess.
func TestNextOccurrencesAcrossDST(t *testing.T) {
	// Lisbon moves to summer time on 29 March 2026.
	from := time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)
	got, err := NextOccurrences("0 9 * * *", "Europe/Lisbon", 4, from)
	if err != nil {
		t.Fatalf("NextOccurrences: %v", err)
	}
	for _, ts := range got {
		if ts.Hour() != 9 {
			t.Errorf("run at %s is not 09:00 local; the local hour must hold across the DST change",
				ts.Format(time.RFC3339))
		}
	}
}

func TestNextOccurrencesRejectsBadInput(t *testing.T) {
	if _, err := NextOccurrences("0 9 * * funday", "", 3, time.Now()); err == nil {
		t.Error("an invalid expression must not produce occurrences")
	}
}
