// SPDX-License-Identifier: AGPL-3.0-only

package web

import "time"

// SetSSEHeartbeat shortens the job stream heartbeat for tests and returns
// the function that restores it. Call it before the server starts.
func SetSSEHeartbeat(d time.Duration) func() {
	old := sseHeartbeat
	sseHeartbeat = d
	return func() { sseHeartbeat = old }
}
