package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/config"
	"github.com/Armur-Ai/Pentest-Swarm-AI/internal/db"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply SQL migrations to the configured database",
	Long: `Apply SQL migrations in internal/db/migrations/*.sql to
the database configured in the YAML config (or PENTESTSWARM_*
environment variables).

This is the same routine called by the API server on startup,
exposed as a one-shot CLI for operators who want to:

  - apply migrations before starting the API server
    (recommended for production where the API runs as
    multiple replicas — race the schema, not the API)
  - verify a migration applied cleanly
  - smoke-test a fresh database connection

Exit code is 0 on success and 1 on any error (so the docker-compose
"migrate" profile can wire it up as a dependency check).`,
	Example: `  pentestswarm migrate
  pentestswarm migrate --auto   # exit 0 if schema is up-to-date`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// We do NOT use the cfg.LLM / orchestrator paths in
		// this command — only database credentials. Loading
		// the full config keeps the env override semantics
		// consistent with `serve` though.
		cfg, err := config.Load(cfgFile)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		dsn := cfg.Database.DSN()
		if dsn == "" {
			return fmt.Errorf("database not configured (set database.* in config.yaml or PENTESTSWARM_DATABASE_* env)")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		fmt.Printf("Connecting to %s:%d/%s ...\n", cfg.Database.Host, cfg.Database.Port, cfg.Database.Name)
		pool, err := db.Connect(ctx, dsn)
		if err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		defer pool.Close()

		t0 := time.Now()
		if err := db.Migrate(ctx, pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		fmt.Printf("✅ migrations applied in %s\n", time.Since(t0).Round(time.Millisecond))
		return nil
	},
}

func init() {
	migrateCmd.Flags().Bool("auto", false, "no-op flag kept for symmetry with `serve` (migrations are idempotent)")
	rootCmd.AddCommand(migrateCmd)
}
