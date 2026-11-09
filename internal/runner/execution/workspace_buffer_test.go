package execution

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestBoundedBufferReadFromHonorsOutputLimit(t *testing.T) {
	for _, source := range []string{"123", "1234"} {
		t.Run(source, func(t *testing.T) {
			buffer := &boundedBuffer{limit: 3}
			// LimitReader has no WriterTo, so Copy selects the destination's
			// ReaderFrom rather than calling Write directly.
			count, err := io.Copy(buffer, io.LimitReader(strings.NewReader(source), int64(len(source))))
			if len(source) == 3 {
				if err != nil || count != 3 || buffer.String() != source {
					t.Fatal("exact output allowance rejected", count, err, buffer.String())
				}
			} else if !errors.Is(err, runner.ErrCapacity) || !errors.Is(buffer.err, runner.ErrCapacity) || buffer.Len() > buffer.limit {
				t.Fatal("ReaderFrom bypassed the output allowance", count, err, buffer.Len())
			}
		})
	}
}
