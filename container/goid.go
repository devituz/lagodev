package container

import (
	"runtime"
	"strconv"
)

// goID returns the current goroutine's ID. It is used only to scope the
// cycle-detection resolve stack to the resolving goroutine. There is no
// stable public API for this, so it parses the goroutine header from a
// runtime stack sample. The cost is paid only on the first resolve of a
// dependency chain, not per cache hit.
//
// Header format: "goroutine 18 [running]:\n..." — the second field is the
// numeric ID.
func goID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	s := buf[:n]
	// Skip the leading "goroutine " prefix.
	const prefix = "goroutine "
	if len(s) < len(prefix) {
		return 0
	}
	s = s[len(prefix):]
	// The ID runs up to the first space.
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	id, err := strconv.ParseInt(string(s[:i]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
