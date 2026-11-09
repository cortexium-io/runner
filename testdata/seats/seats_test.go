package seats

import "testing"

// TestParseSeats is the supplied contract: ordinary positive counts and one
// error outcome for invalid or non-positive input. The worker adds seats.go.
func TestParseSeats(t *testing.T) {
	cases := []struct {
		input string
		want  int
		bad   bool
	}{
		{"1", 1, false}, {"12", 12, false}, {"", 0, true},
		{"many", 0, true}, {"0", 0, true}, {"-1", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseSeats(tc.input)
			if got != tc.want || (err != nil) != tc.bad {
				t.Fatalf("ParseSeats(%q) = (%d, %v); want (%d, error=%v)", tc.input, got, err, tc.want, tc.bad)
			}
		})
	}
}
