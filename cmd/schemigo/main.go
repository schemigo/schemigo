// Command schemigo manages declarative database schemas.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/moby/term"
	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/clickhouse"
	"github.com/schemigo/schemigo/mysql"
	"github.com/schemigo/schemigo/postgres"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, in io.Reader, out, diagnostics io.Writer) int {
	if len(args) > 0 && (args[0] == "table" || args[0] == "enum" || args[0] == "view") {
		return runScaffold(ctx, args, out, diagnostics)
	}
	flags := flag.NewFlagSet("schemigo", flag.ContinueOnError)
	dsn := flags.String("d", "", "PostgreSQL, MySQL or ClickHouse connection URL")
	dir := flags.String("dir", "./schema", "Go schema directory")
	schemaName := flags.String("schema", "", "database schema (defaults to public for PostgreSQL or the URL database)")
	export := flags.Bool("export", false, "export current database schema")
	dryRun := flags.Bool("dry-run", false, "print migration SQL without applying it")
	checkOnly := flags.Bool("check", false, "execute the PostgreSQL migration in a transaction and roll it back")
	yes := flags.Bool("yes", false, "apply without confirmation")
	history := flags.Bool("history", false, "show migration history")
	keeper := flags.String("keeper", "", "Keeper/ZooKeeper host:port,... for ClickHouse Apply")
	noBackup := flags.Bool("no-backup", false, "do not save a schema snapshot")
	if err := parseFlags(flags, args, diagnostics); err != nil {
		return 1
	}
	if *dsn == "" {
		return reportError(diagnostics, "schemigo: -d is required")
	}
	conflictingCheck := *checkOnly && (*export || *history || *dryRun || *yes)
	if flags.NArg() != 0 || *export && (*history || *dryRun) || *history && *dryRun || conflictingCheck {
		return reportError(diagnostics, "schemigo: conflicting options or unexpected arguments")
	}
	if err := ctx.Err(); err != nil {
		return reportError(diagnostics, "schemigo: "+err.Error())
	}
	u, err := url.Parse(*dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" && u.Scheme != "clickhouse" && u.Scheme != "mysql" {
		return reportError(diagnostics,
			"schemigo: unsupported database URL scheme; use postgres://, postgresql://, mysql:// or clickhouse://")
	}
	if *checkOnly && u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return reportError(diagnostics, "schemigo: -check requires PostgreSQL")
	}
	var db *sql.DB
	var driver schemigo.Driver
	if u.Scheme == "clickhouse" {
		if !*export && !*history && !*dryRun && *keeper == "" {
			return reportError(diagnostics, "schemigo: ClickHouse Apply requires -keeper")
		}
		if !visitedFlag(flags, "schema") {
			*schemaName = strings.TrimPrefix(u.Path, "/")
			if *schemaName == "" {
				*schemaName = "default"
			}
		}
		db, err = clickhouse.Open(*dsn)
		driver = clickhouse.New(nil)
	} else if u.Scheme == "mysql" {
		if *schemaName == "" {
			*schemaName = strings.TrimPrefix(u.Path, "/")
		}
		db, err = mysql.Open(*dsn)
		driver = mysql.New()
	} else {
		if *schemaName == "" {
			*schemaName = "public"
		}
		db, err = postgres.Open(*dsn)
		driver = postgres.New()
	}
	if err != nil {
		return reportError(diagnostics, "schemigo: "+safeError(err, *dsn))
	}
	defer db.Close()
	if *export {
		err = schemigo.Export(ctx, db, driver, *schemaName, *dir)
		if err == nil {
			fmt.Fprintln(out, "Schema exported to", *dir)
		}
	} else if *history {
		var events []schemigo.Migration
		events, err = schemigo.History(ctx, db, driver, *schemaName)
		if err == nil {
			for _, event := range events {
				fmt.Fprintf(out, "%s %s %s\n", event.AppliedAt.Format("2006-01-02T15:04:05Z07:00"), event.Hash, event.User)
			}
		}
	} else {
		var desired schemigo.Schema
		desired, err = schemigo.Load(ctx, *dir)
		if err == nil {
			var plan schemigo.Plan
			plan, err = schemigo.PlanMigration(ctx, db, driver, *schemaName, desired)
			if err == nil {
				if len(plan.Statements) == 0 {
					if err := printNoChanges(out); err != nil {
						return reportError(diagnostics, "schemigo: "+safeError(err, *dsn))
					}
					return 0
				}
				if err = printSQL(out, plan, driver.Name()); err != nil {
					return reportError(diagnostics, "schemigo: "+safeError(err, *dsn))
				}
				if *dryRun {
					return 0
				}
				if !*yes {
					var check func() error
					if pg, ok := driver.(*postgres.Driver); ok {
						check = func() error {
							return pg.CheckMigration(ctx, db, *schemaName, desired, plan.SHA256())
						}
					}
					proceed, confirmErr := confirmMigration(in, out, *checkOnly, check)
					if confirmErr != nil {
						return reportError(diagnostics, "schemigo: "+safeError(confirmErr, *dsn))
					}
					if !proceed {
						return 0
					}
				}
				if u.Scheme == "clickhouse" {
					var locker clickhouse.Locker
					locker, err = clickhouse.NewKeeperLocker(ctx, strings.Split(*keeper, ","), "/schemigo")
					if err != nil {
						return reportError(diagnostics, "schemigo: "+safeError(err, *dsn))
					}
					defer locker.Close()
					driver = clickhouse.New(locker)
				}
				err = schemigo.Apply(ctx, db, driver, *schemaName, desired, schemigo.ApplyOptions{NoBackup: *noBackup, ExpectedHash: plan.SHA256()})
				if err == nil {
					fmt.Fprintln(out, "Applied.")
				}
			}
		}
	}
	if err != nil {
		return reportError(diagnostics, "schemigo: "+safeError(err, *dsn))
	}
	return 0
}

func safeError(err error, dsn string) string {
	message := strings.ReplaceAll(err.Error(), dsn, "<dsn>")
	if u, parseErr := url.Parse(dsn); parseErr == nil && u.User != nil {
		if password, ok := u.User.Password(); ok && password != "" {
			message = strings.ReplaceAll(message, password, "<redacted>")
			message = strings.ReplaceAll(message, url.PathEscape(password), "<redacted>")
		}
	}
	return message
}

func confirmMigration(in io.Reader, out io.Writer, checkOnly bool, check func() error) (bool, error) {
	fd, terminal := term.GetFdInfo(out)
	color := terminal && useColor()
	separator := ""
	if terminal {
		columns := 60
		if size, err := term.GetWinsize(fd); err == nil && size.Width > 0 {
			columns = min(columns, int(size.Width))
		}
		separator = strings.Repeat("─", columns) + "\n"
	}
	reader := bufio.NewReader(in)
	ask := func(prompt string) (string, error) {
		if _, err := io.WriteString(out, separator+prompt); err != nil {
			return "", err
		}
		answer, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.ToLower(strings.TrimSpace(answer)), nil
	}
	cancel := func() (bool, error) {
		_, err := fmt.Fprintln(out, "Cancelled.")
		return false, err
	}
	if !checkOnly {
		answer, err := ask(migrationPrompt(check != nil, color))
		if err != nil || answer == "y" {
			return answer == "y", err
		}
		if check == nil || answer != "c" && answer != "check" {
			return cancel()
		}
	}
	if err := check(); err != nil {
		return false, fmt.Errorf("check migration: %w", err)
	}
	if _, err := fmt.Fprintln(out, "Migration check passed. Transaction rolled back."); err != nil {
		return false, err
	}
	if checkOnly {
		return false, nil
	}
	answer, err := ask(migrationPrompt(false, color))
	if err != nil || answer == "y" {
		return answer == "y", err
	}
	return cancel()
}

func visitedFlag(flags *flag.FlagSet, name string) bool {
	found := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
