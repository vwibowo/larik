//go:build race

package agent

// Race instrumentation adds seconds to script runner startup. Scale both the
// script budget and permission waits so waits still exceed the whole budget.
const codeTimeScale = 4
