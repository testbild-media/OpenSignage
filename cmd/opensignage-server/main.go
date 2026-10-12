// Command opensignage-server is the OpenSignage server.
//
//	opensignage-server [serve]                       run the server (default)
//	opensignage-server create-admin -u NAME          create the first admin (headless setup)
//	opensignage-server reset-password -u NAME        set a new password for a user
//	opensignage-server backup-db FILE                write a consistent database copy
//	opensignage-server rollback                      undo a failed server update (systemd OnFailure)
//	opensignage-server version                       print the version
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	_ "time/tzdata" // time zones must work without system tzdata

	"golang.org/x/term"

	"opensignage/internal/common/logging"
	"opensignage/internal/common/version"
	"opensignage/internal/server/app"
	"opensignage/internal/server/config"
	"opensignage/internal/server/db"
	"opensignage/internal/server/updates"
	"opensignage/internal/server/users"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configFile := fs.String("config", config.DefaultFile(), "config file (KEY=VALUE)")
	username := fs.String("u", "", "username")
	force := fs.Bool("force", false, "create an admin even if users exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cmd == "version" {
		fmt.Printf("opensignage-server %s (%s)\n", version.Version, version.Commit)
		return nil
	}
	cfg, err := config.Load(*configFile)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "serve":
		log, rf, err := logging.Setup(filepath.Join(cfg.DataDir, "logs"), "server", cfg.LogLevel)
		if err != nil {
			return err
		}
		defer rf.Close()
		if cfg.File == "" {
			log.Info("no config file found, using defaults", "path", *configFile)
		}
		// One process at a time works on the data directory: a rollback started by
		// systemd (OnFailure) may still be restoring the database. Held until the process
		// ends, taken before anything (pending restore, update check, database) starts.
		lock, err := updates.LockDataDir(ctx, cfg.DataDir, "serve", updates.ServeLockWait, log)
		if err != nil {
			log.Error("data directory lock", "err", err)
			return err
		}
		defer lock.Release()
		// First start after a server update: migrations + self-test, automatic rollback
		// on failure (the error ends the process, systemd starts the previous version).
		dbFile := filepath.Join(cfg.DataDir, "db", "opensignage.db")
		selfTest := func(ctx context.Context) error { return updates.SelfTest(ctx, dbFile) }
		if err := updates.Startup(ctx, cfg.DataDir, version.Version, updates.Symlinks{}, selfTest, log); err != nil {
			log.Error("server update failed", "err", err)
			return err
		}
		srv, err := app.New(ctx, cfg, log)
		if err != nil {
			return err
		}
		return srv.Run(ctx)
	case "create-admin", "reset-password":
		if *username == "" {
			return fmt.Errorf("-u NAME is required")
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "db", "opensignage.db"))
		if err != nil {
			return err
		}
		defer d.Close()
		if cmd == "create-admin" {
			err = users.CreateAdminCLI(ctx, d, *username, pw, *force)
		} else {
			err = users.ResetPasswordCLI(ctx, d, *username, pw)
		}
		if err == nil {
			fmt.Println("ok")
		}
		return err
	case "rollback":
		// Called by opensignage-rollback.service (with the PREVIOUS release's binary) when
		// the current release keeps crashing. Restores the pre-update database backup and
		// switches the release symlink back (see internal/server/updates).
		return rollbackCmd(cfg)
	case "backup-db":
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: backup-db FILE")
		}
		d, err := db.Open(ctx, filepath.Join(cfg.DataDir, "db", "opensignage.db"))
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Backup(ctx, fs.Arg(0))
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// readPassword reads the password twice from the terminal, or once from stdin when piped.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", fmt.Errorf("passwords do not match")
	}
	return string(a), nil
}
