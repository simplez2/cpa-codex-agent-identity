// Package egress carries trusted, request-scoped outbound routing decisions.
package egress

import "context"

type proxyKey struct{}

// WithProxy must only receive configuration resolved from authenticated management
// requests or CPA auth files, never a public request header.
func WithProxy(ctx context.Context, proxy string) context.Context {
	return context.WithValue(ctx, proxyKey{}, proxy)
}

func Proxy(ctx context.Context) string {
	v, _ := ctx.Value(proxyKey{}).(string)
	return v
}
