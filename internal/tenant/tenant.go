package tenant

import "context"

type tenantKey struct{}

// ContextWithTenant returns a copy of ctx that carries the tenant identifier.
// The tenant is used to scope physical resources (e.g. git repositories) in
// multi-tenant deployments.
func ContextWithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// FromContext extracts the tenant identifier from ctx.
func FromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenantKey{}).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}
