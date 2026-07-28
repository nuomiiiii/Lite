package utils

import "testing"

func TestReturnRouteProbeInFlightLifecycle(t *testing.T) {
	const taskID = uint(987654)
	FinishReturnRouteProbe(taskID)
	if ReturnRouteProbeInFlight(taskID) {
		t.Fatal("probe should not be in flight before it starts")
	}
	StartReturnRouteProbe(taskID)
	if !ReturnRouteProbeInFlight(taskID) {
		t.Fatal("started probe should be in flight")
	}
	FinishReturnRouteProbe(taskID)
	if ReturnRouteProbeInFlight(taskID) {
		t.Fatal("finished probe should not remain in flight")
	}
}
