package scheduler

import (
	"testing"
	"time"
)

func TestValidateCron(t *testing.T) {
	for _, expr := range []string{"*/15 * * * *", "0 */6 * * *", "0 3 * * *", "0 8 * * 1"} {
		next, err := ValidateCron(expr, "Europe/Berlin", 3)
		if err != nil || len(next) != 3 {
			t.Errorf("%s: %v %v", expr, next, err)
		}
	}
	for _, expr := range []string{"", "* * * *", "0 0 * * * *", "61 * * * *", "TZ=UTC * * * * *"} {
		if _, err := ValidateCron(expr, "UTC", 1); err == nil {
			t.Errorf("%q: expected error", expr)
		}
	}
	if _, err := ValidateCron("* * * * *", "Mars/Olympus", 1); err == nil {
		t.Error("expected timezone error")
	}
	next, _ := ValidateCron("0 3 * * *", "Asia/Tokyo", 1)
	if h := next[0].In(mustLoad("Asia/Tokyo")).Hour(); h != 3 {
		t.Errorf("timezone not applied: hour %d", h)
	}
}

func mustLoad(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}
