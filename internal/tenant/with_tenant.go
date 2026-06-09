package tenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TxStarter is the minimal surface WithTenant needs from a
// *pgxpool.Pool or a transaction. We accept an interface
// here so tests can pass a fake that records the SET LOCAL
// calls without spinning up Postgres.
type TxStarter interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// PoolFromDB is a small adapter that lets callers wrap a
// *pgxpool.Pool into a TxStarter. The pgxpool.Pool type
// already satisfies the interface, so this is identity —
// kept as a named helper for documentation purposes.
func PoolFromDB(p *pgxpool.Pool) TxStarter { return p }

// WithTenant opens a transaction, sets the RLS GUC
// (current_setting('app.tenant_id', true)) to tenantID,
// and runs fn inside that tx. When fn returns nil the tx
// commits; otherwise the tx is rolled back and the error
// propagated. The GUC is scoped via SET LOCAL so it
// disappears at COMMIT/ROLLBACK; the underlying
// connection can be returned to the pool without
// leaking state.
//
// Errors:
//   - ErrEmpty: tenantID is uuid.Nil
//   - any tx / SET / fn error
func WithTenant(ctx context.Context, db TxStarter, tenantID uuid.UUID, fn func(pgx.Tx) error) (err error) {
	if tenantID == uuid.Nil {
		return errors.New("tenant.WithTenant: empty tenant id")
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("tenant.WithTenant: begin: %w", err)
	}
	// Defer-rollback is a no-op after a successful commit,
	// so it's the standard "ensure cleanup" idiom. We
	// shadow `err` in the deferred closure so a panic
	// during fn doesn't get swallowed.
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		if cErr := tx.Commit(ctx); cErr != nil {
			err = fmt.Errorf("tenant.WithTenant: commit: %w", cErr)
		}
	}()

	// SET LOCAL app.tenant_id. The GUC name is a project
	// convention shared with the RLS policy in
	// internal/db/migrations/000009_tenants.sql; if you
	// rename the GUC, rename it there too.
	if _, err = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String()); err != nil {
		return fmt.Errorf("tenant.WithTenant: set_config: %w", err)
	}
	if err = fn(tx); err != nil {
		return err
	}
	return nil
}

// WithTenantInferred is a sugar for WithTenant that pulls
// the tenant from ctx. Panics if no tenant is set, so use
// only in code paths already validated by the auth
// middleware.
func WithTenantInferred(ctx context.Context, db TxStarter, fn func(pgx.Tx) error) error {
	return WithTenant(ctx, db, MustFromContext(ctx).ID, fn)
}
