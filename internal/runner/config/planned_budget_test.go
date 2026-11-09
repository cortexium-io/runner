package config

import (
	"errors"
	"strconv"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestPlannedDeliveryCanLowerItsTotalWithoutIncreasingTheCeiling(t *testing.T) {
	for _, file := range []string{
		"planned-frontend.json",
		"planned-retained-frontend.json",
		"planned-investigation.json",
		"planned-retained-investigation.json",
	} {
		t.Run(file, func(t *testing.T) {
			base, project := plannedFixture(t, "jacks-break", file)
			if base.MaxAttempts != 64 {
				t.Fatal("default planned allowance changed", base.MaxAttempts)
			}
			for _, total := range []int{1, 62, 64, 0, -1, 65, 129} {
				t.Run(strconv.Itoa(total), func(t *testing.T) {
					p := runner.Clone(base)
					p.MaxAttempts = total
					err := errors.Join(validateProcess(p, project), validateReleaseProcess(p, project))
					valid := total > 0 && total <= 64
					if (err == nil) != valid {
						t.Fatalf("planned total %d: %v", total, err)
					}
				})
			}
		})
	}
}
