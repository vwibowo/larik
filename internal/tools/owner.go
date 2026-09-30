package tools

import "context"

type ownerKey struct{}

// WithOwner marks ctx with the agent a tool call runs for: "" for the main
// agent, a unique id for each subagent. Tools that keep per-agent state
// (the browser's tabs) key it on this.
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerKey{}, owner)
}

// Owner is the agent ctx's tool call runs for; "" is the main agent.
func Owner(ctx context.Context) string {
	owner, _ := ctx.Value(ownerKey{}).(string)
	return owner
}
