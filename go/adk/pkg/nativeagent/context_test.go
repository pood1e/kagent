package nativeagent

import (
	"testing"
	"time"
)

func TestExecutionStoppedIncludesElapsedDeadlineBeforeDone(t *testing.T) {
	active := make(chan struct{})
	canceled := make(chan struct{})
	close(canceled)
	for _, test := range []struct {
		name     string
		done     <-chan struct{}
		deadline time.Time
		want     bool
	}{
		{name: "unlimited", done: active},
		{name: "future", done: active, deadline: time.Now().Add(time.Minute)},
		{name: "elapsed before timer callback", done: active, deadline: time.Now().Add(-time.Minute), want: true},
		{name: "canceled", done: canceled, deadline: time.Now().Add(time.Minute), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := execution{done: test.done, deadline: test.deadline}
			if got := parent.stopped(); got != test.want {
				t.Fatalf("stopped() = %v, want %v", got, test.want)
			}
		})
	}
}
