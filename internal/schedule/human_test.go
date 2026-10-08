package schedule

import "testing"

func TestHuman(t *testing.T) {
	cases := map[string]string{
		"0 19 * * *":    "Every day at 19:00",
		"30 17 * * 1-5": "Weekdays at 17:30",
		"0 7 * * 1":     "Mondays at 07:00",
		"0 7 * * 1,4":   "Mondays and Thursdays at 07:00",
		"0 7 * * 1,3,5": "Mondays, Wednesdays and Fridays at 07:00",
		"45 8 1 * *":    "The 1st of every month at 08:45",
		"0 8 22 * *":    "The 22nd of every month at 08:00",
		"*/15 * * * *":  "*/15 * * * *", // not phrasable; raw is honest
		"0 8 1 1 *":     "0 8 1 1 *",
	}
	for expr, want := range cases {
		s, err := Parse(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got := s.Human(); got != want {
			t.Errorf("%s: got %q, want %q", expr, got, want)
		}
	}
}
