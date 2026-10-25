//go:build race

package tools

// The race detector slows the script runner's startup by seconds, which
// eats a 1-second limit before the script runs. Timing tests scale their
// limits and waits by timeScale together.
const timeScale = 4
