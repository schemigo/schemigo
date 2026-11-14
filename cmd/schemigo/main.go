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
	"regexp"
	"strings"

	"github.com/schemigo/schemigo"
	"github.com/schemigo/schemigo/clickhouse"
	"github.com/schemigo/schemigo/postgres"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, in io.Reader, out, diagnostics io.Writer) int {
	if len(args) > 0 && (args[0] == "table" || args[0] == "enum" || args[0] == "view") {
		return runScaffold(ctx, args, out, diagnostics)
	}
	flags := flag.NewFlagSet("schemigo", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	dsn := flags.String("d", "", "PostgreSQL or ClickHouse connection URL")
	dir := flags.String("dir", "./schema", "Go schema directory")
	schemaName := flags.String("schema", "public", "database schema")
	export := flags.Bool("export", false, "export current database schema")
	dryRun := flags.Bool("dry-run", false, "print migration SQL without applying it")
	yes := flags.Bool("yes", false, "apply without confirmation")
	history := flags.Bool("history", false, "show migration history")
	keeper := flags.String("keeper", "", "Keeper/ZooKeeper host:port,... for ClickHouse Apply")
	noBackup := flags.Bool("no-backup", false, "do not save a schema snapshot")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if *dsn == "" {
		fmt.Fprintln(diagnostics, "schemigo: -d is required")
		return 1
	}
	if flags.NArg() != 0 || *export && (*history || *dryRun) || *history && *dryRun {
		fmt.Fprintln(diagnostics, "schemigo: conflicting options or unexpected arguments")
		return 1
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(diagnostics, "schemigo:", err)
		return 1
	}
	u, err := url.Parse(*dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" && u.Scheme != "clickhouse" {
		fmt.Fprintln(diagnostics, "schemigo: unsupported database URL scheme; use postgres://, postgresql:// or clickhouse://")
		return 1
	}
	var db *sql.DB
	var driver schemigo.Driver
	if u.Scheme == "clickhouse" {
		if !*export && !*history && !*dryRun && *keeper == "" {
			fmt.Fprintln(diagnostics, "schemigo: ClickHouse Apply requires -keeper")
			return 1
		}
		if !visitedFlag(flags, "schema") {
			*schemaName = strings.TrimPrefix(u.Path, "/")
			if *schemaName == "" {
				*schemaName = "default"
			}
		}
		db, err = clickhouse.Open(*dsn)
		driver = clickhouse.New(nil)
	} else {
		db, err = postgres.Open(*dsn)
		driver = postgres.New()
	}
	if err != nil {
		fmt.Fprintln(diagnostics, "schemigo:", safeError(err, *dsn))
		return 1
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
					fmt.Fprintln(out, "No changes.")
					return 0
				}
				if err = printSQL(out, plan.SQL()); err != nil {
					fmt.Fprintln(diagnostics, "schemigo:", safeError(err, *dsn))
					return 1
				}
				if *dryRun {
					return 0
				}
				if !*yes {
					fmt.Fprint(out, "Apply migration? [Y/n]: ")
					answer, readErr := bufio.NewReader(in).ReadString('\n')
					if readErr != nil && !errors.Is(readErr, io.EOF) {
						err = readErr
					} else if strings.TrimSpace(answer) != "Y" && strings.TrimSpace(answer) != "y" {
						fmt.Fprintln(out, "Cancelled.")
						return 0
					}
				}
				if err == nil {
					if u.Scheme == "clickhouse" {
						var locker clickhouse.Locker
						locker, err = clickhouse.NewKeeperLocker(ctx, strings.Split(*keeper, ","), "/schemigo")
						if err != nil {
							fmt.Fprintln(diagnostics, "schemigo:", safeError(err, *dsn))
							return 1
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
	}
	if err != nil {
		fmt.Fprintln(diagnostics, "schemigo:", safeError(err, *dsn))
		return 1
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

var sqlKeywords = regexp.MustCompile(`(?im)^(\s*)(CREATE|ALTER|DROP|SELECT|INSERT|UPDATE|DELETE|DO|GRANT|REVOKE|COMMENT|VALIDATE|SET|RESET)(\b)`)

func printSQL(out io.Writer, sql string) error {
	file, ok := out.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" {
		_, err := fmt.Fprint(out, sql)
		return err
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		_, err := fmt.Fprint(out, sql)
		return err
	}
	_, err = fmt.Fprint(out, sqlKeywords.ReplaceAllString(sql, "${1}\x1b[36m${2}\x1b[0m${3}"))
	return err
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
