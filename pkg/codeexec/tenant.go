package codeexec

import (
	"context"

	"github.com/Tnsor-Labs/brokoli/pkg/runas"
)

// WithTenant names whose code runs under ctx. A warm worker keeps whatever
// the code it ran left in its process (sys.modules, patched builtins, a
// thread still running), so the pool hands a worker only to the tenant it
// first ran for: one tenant's script must never run in a process another
// tenant's script has had. With BROKOLI_USER_CODE_UIDS set, the tenant also
// decides the user the worker runs as (pkg/runas).
func WithTenant(ctx context.Context, tenant string) context.Context {
	return runas.WithTenant(ctx, tenant)
}

// TenantFrom is the tenant WithTenant set, or "" for none (a single-tenant
// install, where every script is the same tenant's).
func TenantFrom(ctx context.Context) string {
	return runas.TenantFrom(ctx)
}
