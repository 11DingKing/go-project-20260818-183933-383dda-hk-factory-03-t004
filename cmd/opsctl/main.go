// Command opsctl is the sitesync operations CLI. It shares the same SQLite
// store implementation as the HTTP service so on-disk state stays consistent.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/domain"
	"sitesync/internal/logging"
	"sitesync/internal/service"
	"sitesync/internal/store"
)

func main() {
	root := newRoot()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

type globals struct {
	configPath string
	dataDir    string
	dbFile     string
}

func newRoot() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:   "opsctl",
		Short: "sitesync operations and maintenance CLI",
	}
	root.PersistentFlags().StringVar(&g.configPath, "config", "", "path to YAML config file")
	root.PersistentFlags().StringVar(&g.dataDir, "data-dir", "", "override storage.data_dir")
	root.PersistentFlags().StringVar(&g.dbFile, "db-file", "", "override storage.db_file")
	root.AddCommand(
		initCmd(g),
		importCmd(g),
		exportCmd(g),
		verifyCmd(g),
		rebuildIndexCmd(g),
		diagnoseCmd(g),
		requeueCmd(g),
	)
	return root
}

// openStore loads config, applies overrides, opens and migrates the store.
func (g *globals) openStore(ctx context.Context) (*store.Store, config.Config, error) {
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return nil, cfg, err
	}
	if g.dataDir != "" {
		cfg.Storage.DataDir = g.dataDir
	}
	if g.dbFile != "" {
		cfg.Storage.DBFile = g.dbFile
	}
	clk := clock.Real{}
	st, err := store.New(ctx, cfg, clk)
	if err != nil {
		return nil, cfg, err
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, cfg, err
	}
	return st, cfg, nil
}

// withStore opens the shared store for the duration of fn so every subcommand
// shares one open/migrate/defer-close path and the same on-disk implementation.
func (g *globals) withStore(fn func(ctx context.Context, st *store.Store, cfg config.Config) error) error {
	ctx := context.Background()
	st, cfg, err := g.openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(ctx, st, cfg)
}

func (g *globals) services(st *store.Store, cfg config.Config) (*service.Services, service.Deps) {
	clk := clock.Real{}
	logger := newLogger()
	deps := service.Deps{
		UOW: st, Clock: clk, Logger: logger, Cfg: cfg,
		Persons: st, Customers: st, Devices: st,
		Orders: st, Steps: st, OrderDevices: st,
		Trials: st, Inspections: st,
		Records: st, WorkHours: st, Adjudications: st,
		Bills: st, Batches: st, Manuals: st, SyncState: st,
		Audit: st, Failures: st,
	}
	return service.New(deps), deps
}

func initCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "create the data directory and apply migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
				v, _ := st.SchemaVersion(ctx)
				fmt.Printf("data dir: %s\n", cfg.Storage.DataDir)
				fmt.Printf("db file:  %s\n", filepath.Join(cfg.Storage.DataDir, cfg.Storage.DBFile))
				fmt.Printf("schema version: %d\n", v)
				return nil
			})
		},
	}
}

type importFile struct {
	Persons   []service.CreatePersonRequest   `json:"persons"`
	Customers []service.CreateCustomerRequest `json:"customers"`
	Devices   []service.CreateDeviceRequest   `json:"devices"`
}

func importCmd(g *globals) *cobra.Command {
	var path string
	c := &cobra.Command{
		Use:   "import",
		Short: "import persons, customers and devices from a JSON file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
				svc, _ := g.services(st, cfg)
				data, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("read import file: %w", err)
				}
				var in importFile
				if err := json.Unmarshal(data, &in); err != nil {
					return fmt.Errorf("parse import file: %w", err)
				}
				tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				for _, p := range in.Persons {
					created, err := svc.Registry.CreatePerson(ctx, "opsctl", p)
					if err != nil {
						fmt.Fprintf(tw, "person %s\tFAIL\t%v\n", p.Code, err)
						continue
					}
					fmt.Fprintf(tw, "person %s\tOK\t%s\n", created.Code, created.ID)
				}
				for _, c := range in.Customers {
					created, err := svc.Registry.CreateCustomer(ctx, "opsctl", c)
					if err != nil {
						fmt.Fprintf(tw, "customer %s\tFAIL\t%v\n", c.Code, err)
						continue
					}
					fmt.Fprintf(tw, "customer %s\tOK\t%s\n", created.Code, created.ID)
				}
				for _, d := range in.Devices {
					created, err := svc.Registry.CreateDevice(ctx, "opsctl", d)
					if err != nil {
						fmt.Fprintf(tw, "device %s\tFAIL\t%v\n", d.Serial, err)
						continue
					}
					fmt.Fprintf(tw, "device %s\tOK\t%s\n", created.Serial, created.ID)
				}
				return tw.Flush()
			})
		},
	}
	c.Flags().StringVar(&path, "file", "", "path to JSON import file (required)")
	_ = c.MarkFlagRequired("file")
	return c
}

func exportCmd(g *globals) *cobra.Command {
	var orderID, outFile, status string
	c := &cobra.Command{
		Use:   "export",
		Short: "export operation records or reconciliation to JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
				svc, _ := g.services(st, cfg)
				f := domain.RecordFilter{OrderID: orderID, Status: domain.RecordStatus(status)}
				rows, total, err := svc.Reconciliation.Export(ctx, f, domain.Page{Size: domain.MaxPageSize})
				if err != nil {
					return err
				}
				payload := map[string]any{"rows": rows, "total": total}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if outFile != "" {
					fh, err := os.Create(outFile)
					if err != nil {
						return err
					}
					defer fh.Close()
					enc = json.NewEncoder(fh)
					enc.SetIndent("", "  ")
				}
				return enc.Encode(payload)
			})
		},
	}
	c.Flags().StringVar(&orderID, "order", "", "filter by deployment order id")
	c.Flags().StringVar(&status, "status", "", "filter by record status")
	c.Flags().StringVar(&outFile, "out", "", "write to file instead of stdout")
	return c
}

func verifyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:          "verify",
		Short:        "run consistency checks against the database",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
				report, err := runVerify(ctx, st)
				if err != nil {
					return err
				}
				tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "CHECK\tRESULT\tDETAIL")
				issues := 0
				for _, r := range report {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", r.name, r.status, r.detail)
					if r.status == "FAIL" {
						issues++
					}
				}
				_ = tw.Flush()
				if issues > 0 {
					return fmt.Errorf("verify found %d issue(s)", issues)
				}
				return nil
			})
		},
	}
}

type verifyRow struct {
	name   string
	status string
	detail string
}

func runVerify(ctx context.Context, st *store.Store) ([]verifyRow, error) {
	db := st.DB()
	var rows []verifyRow
	orphan, err := countZero(ctx, db, `SELECT COUNT(*) FROM operation_records r
LEFT JOIN deployment_orders o ON o.id = r.order_id WHERE o.id IS NULL`)
	if err != nil {
		return nil, err
	}
	rows = append(rows, verdict("orphan records (no order)", orphan))
	stuck, err := countZero(ctx, db, `SELECT COUNT(*) FROM sync_batches
WHERE status = 'leasing' AND lease_until IS NOT NULL AND lease_until < datetime('now')`)
	if err != nil {
		return nil, err
	}
	rows = append(rows, verdict("stuck/expired leases", stuck))
	novers, err := countZero(ctx, db, `SELECT COUNT(*) FROM deployment_orders
WHERE trial_id IS NOT NULL AND trial_id NOT IN (SELECT id FROM trial_periods)`)
	if err != nil {
		return nil, err
	}
	rows = append(rows, verdict("orders with dangling trial", novers))
	dup, err := countZero(ctx, db, `SELECT COUNT(*) FROM (SELECT device_id, work_date, COUNT(*) c
FROM customer_work_hours GROUP BY device_id, work_date HAVING c > 1)`)
	if err != nil {
		return nil, err
	}
	rows = append(rows, verdict("duplicate customer work hours", dup))
	pfail, err := countZero(ctx, db, `SELECT COUNT(*) FROM permanent_failures WHERE status = 'permanent'`)
	if err != nil {
		return nil, err
	}
	rows = append(rows, verifyRow{name: "permanent failures", status: "INFO", detail: fmt.Sprintf("%d", pfail)})
	return rows, nil
}

func verdict(name string, n int) verifyRow {
	if n == 0 {
		return verifyRow{name: name, status: "OK", detail: "0"}
	}
	return verifyRow{name: name, status: "FAIL", detail: fmt.Sprintf("%d", n)}
}

func countZero(ctx context.Context, db *sql.DB, q string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func cursorSuffix(inserted int64) string {
	if inserted == 0 {
		return ""
	}
	return fmt.Sprintf(", %d new", inserted)
}

func rebuildIndexCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild-index",
		Short: "recompute change-version counter and per-order sync cursors",
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
				db := st.DB()
				var maxVer int
				if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(change_version), 0) FROM operation_records").Scan(&maxVer); err != nil {
					return err
				}
				if _, err := db.ExecContext(ctx, "UPDATE change_counter SET next_version = ? WHERE id = 1", maxVer+1); err != nil {
					return err
				}
				_, rerr := db.ExecContext(ctx, `UPDATE sync_state SET last_change_version = (SELECT COALESCE(MAX(r.change_version),0) FROM operation_records r WHERE r.order_id = sync_state.order_id)`)
				if rerr != nil {
					return fmt.Errorf("rebuild-index: refresh cursors: %w", rerr)
				}
				ins, ierr := db.ExecContext(ctx, `INSERT INTO sync_state (order_id, last_change_version, last_pulled_at, last_backfill_at, updated_at)
SELECT o.id, COALESCE((SELECT MAX(change_version) FROM operation_records r WHERE r.order_id = o.id), 0), NULL, NULL, ?
FROM deployment_orders o
WHERE NOT EXISTS (SELECT 1 FROM sync_state s WHERE s.order_id = o.id)`,
					clock.Real{}.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"))
				if ierr != nil {
					return fmt.Errorf("rebuild-index: insert cursors: %w", ierr)
				}
				extra, _ := ins.RowsAffected()
				fmt.Printf("rebuild-index: change_counter.next_version = %d, cursors refreshed%s\n", maxVer+1, cursorSuffix(extra))
				return nil
			})
		},
	}
}

func diagnoseCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "diagnose",
		Short: "print status counts and outstanding work",
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
				db := st.DB()
				tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintf(tw, "data_dir\t%s\n", cfg.Storage.DataDir)
				v, _ := st.SchemaVersion(ctx)
				fmt.Fprintf(tw, "schema_version\t%d\n", v)
				printCounts(ctx, db, tw, "orders by status", `SELECT status, COUNT(*) FROM deployment_orders GROUP BY status ORDER BY status`)
				printCounts(ctx, db, tw, "records by status", `SELECT status, COUNT(*) FROM operation_records GROUP BY status ORDER BY status`)
				printCounts(ctx, db, tw, "batches by status", `SELECT status, COUNT(*) FROM sync_batches GROUP BY status ORDER BY status`)
				printCounts(ctx, db, tw, "trials by status", `SELECT status, COUNT(*) FROM trial_periods GROUP BY status ORDER BY status`)
				var overdue int
				_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trial_periods WHERE acceptance_deadline < ? AND status IN ('active','overdue')`, clock.Real{}.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")).Scan(&overdue)
				fmt.Fprintf(tw, "overdue trials\t%d\n", overdue)
				var dead int
				_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM permanent_failures WHERE status='permanent'").Scan(&dead)
				fmt.Fprintf(tw, "dead letters\t%d\n", dead)
				return tw.Flush()
			})
		},
	}
}

func printCounts(ctx context.Context, db *sql.DB, tw *tabwriter.Writer, label, q string) {
	rs, err := db.QueryContext(ctx, q)
	if err != nil {
		fmt.Fprintf(tw, "%s\terror\t%v\n", label, err)
		return
	}
	defer rs.Close()
	for rs.Next() {
		var status string
		var n int
		_ = rs.Scan(&status, &n)
		fmt.Fprintf(tw, "%s:%s\t%d\n", label, status, n)
	}
}

func requeueCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "requeue [failure-id]",
		Short: "re-arm a permanent failure for retry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.withStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
				svc, _ := g.services(st, cfg)
				if err := svc.Query.RequeueFailure(ctx, args[0]); err != nil {
					return err
				}
				fmt.Printf("requeued %s\n", args[0])
				return nil
			})
		},
	}
}

func newLogger() *zap.Logger {
	l, err := logging.New("info", false)
	if err != nil {
		return zap.NewNop()
	}
	return l
}
