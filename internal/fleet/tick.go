package fleet

import "time"

// tick paces the reconnect watcher. Small enough that a latched e-stop is
// re-asserted long before a human could select the vehicle and command it.
func tick() <-chan time.Time { return time.After(100 * time.Millisecond) }
