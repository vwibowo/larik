package tools

import "context"

type writeCheckKey struct{}

// WithWriteCheck installs a destination-policy preflight for tools that discover
// additional write targets only after starting, such as LSP workspace edits.
func WithWriteCheck(ctx context.Context, check func(string) error) context.Context {
	return context.WithValue(ctx, writeCheckKey{}, check)
}

// CheckWrite checks a discovered destination against the caller's explicit
// restrictions. Ordinary freshness and checkpoint checks still run at write time.
func CheckWrite(ctx context.Context, path string) error {
	if check, ok := ctx.Value(writeCheckKey{}).(func(string) error); ok {
		return check(path)
	}
	return nil
}
