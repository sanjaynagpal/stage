package elevate

import "testing"

// IsElevated's actual result depends on how the test binary was launched, so
// this just proves the call succeeds without panicking rather than
// asserting a specific value.
func TestIsElevatedDoesNotPanic(t *testing.T) {
	_ = IsElevated()
}
