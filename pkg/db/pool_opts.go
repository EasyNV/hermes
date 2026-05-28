package db

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolOpts tunes a pgxpool.Pool for managed-Postgres deployments
// (Cloud SQL, Aurora, Supabase) where SSL is mandatory and connection
// lifetime must rotate to pick up fresh IAM auth tokens.
//
// All fields are optional. Zero values mean "leave the URL / pgx
// default untouched". When a non-zero value is supplied, it overlays
// the URL: existing matching query parameters or pgxpool fields are
// replaced.
type PoolOpts struct {
	// SSLMode applied if non-empty. Valid values: "disable", "allow",
	// "prefer", "require", "verify-ca", "verify-full". Overlays
	// (or sets) the ?sslmode= query parameter.
	SSLMode string

	// SSLRootCert applied if non-empty. Overlays the ?sslrootcert= query
	// parameter. Typically a path inside the container (e.g.
	// /etc/ssl/rds-ca-bundle.pem).
	SSLRootCert string

	// MaxConns applied if > 0. Maps to pgxpool.Config.MaxConns. Managed
	// DBs have hard connection caps so this should be set conservatively
	// (compose: 20; Cloud SQL t2-medium: 200 total → ~20-50 per pod).
	MaxConns int32

	// ConnMaxLifetime applied if > 0. Forces pgxpool to recycle each
	// connection after this duration. Necessary for IAM-token-based
	// managed DBs where the password expires every ~15 minutes; pick
	// a value comfortably below that (e.g. 30m for non-IAM, 10m for
	// IAM). Maps to pgxpool.Config.MaxConnLifetime.
	ConnMaxLifetime time.Duration
}

// NewPoolWithOpts is a superset of NewPool that allows tuning for
// managed-Postgres deployments. URL query parameters in opts overlay
// the parsed URL: existing matching keys are replaced. Unset opts
// leave the URL untouched, so NewPoolWithOpts(ctx, url, PoolOpts{})
// is behaviorally identical to NewPool(ctx, url).
func NewPoolWithOpts(ctx context.Context, databaseURL string, opts PoolOpts) (*pgxpool.Pool, error) {
	overlayed, err := overlayURL(databaseURL, opts)
	if err != nil {
		return nil, fmt.Errorf("overlay database URL: %w", err)
	}

	config, err := pgxpool.ParseConfig(overlayed)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}

	if opts.MaxConns > 0 {
		config.MaxConns = opts.MaxConns
	}
	if opts.ConnMaxLifetime > 0 {
		config.MaxConnLifetime = opts.ConnMaxLifetime
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return pool, nil
}

// overlayURL merges opts.SSLMode and opts.SSLRootCert into databaseURL's
// query string. Exported for unit tests.
func overlayURL(databaseURL string, opts PoolOpts) (string, error) {
	if opts.SSLMode == "" && opts.SSLRootCert == "" {
		return databaseURL, nil
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if opts.SSLMode != "" {
		q.Set("sslmode", opts.SSLMode)
	}
	if opts.SSLRootCert != "" {
		q.Set("sslrootcert", opts.SSLRootCert)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
