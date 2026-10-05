package cliproxy

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFinishUsageSavesOnlyAfterTheQueueDrains(t *testing.T) {
	var steps []string
	drain := func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("drain context has no deadline; a blocked plugin would hang shutdown")
		}
		steps = append(steps, "drain")
		return nil
	}
	flush := func() error {
		steps = append(steps, "flush")
		return nil
	}

	finishUsage(context.Background(), drain, flush)

	if want := []string{"drain", "flush"}; !reflect.DeepEqual(steps, want) {
		t.Fatalf("shutdown steps = %v, want %v", steps, want)
	}
}

func TestFinishUsageStillSavesWhenTheDrainTimesOut(t *testing.T) {
	flushed := false
	drain := func(context.Context) error { return context.DeadlineExceeded }
	flush := func() error {
		flushed = true
		return errors.New("disk full")
	}

	finishUsage(context.Background(), drain, flush)

	if !flushed {
		t.Fatalf("usage stats were not saved after the drain timed out")
	}
}
