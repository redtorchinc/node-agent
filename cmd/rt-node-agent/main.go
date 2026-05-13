package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redtorchinc/node-agent/internal/buildinfo"
	"github.com/redtorchinc/node-agent/internal/config"
	"github.com/redtorchinc/node-agent/internal/config/migrate"
	"github.com/redtorchinc/node-agent/internal/health"
	"github.com/redtorchinc/node-agent/internal/server"
	"github.com/redtorchinc/node-agent/internal/service"
)

const usage = `rt-node-agent — RedTorch node load-visibility agent

Usage:
  rt-node-agent <command>

Commands:
  run             Run the HTTP server in the foreground (default)
  install         Install as a native service (systemd/launchd/Windows SCM)
  uninstall       Remove the native service
  status          Print service state
  start           Start an installed service
  stop            Stop an installed service
  version         Print version info
  healthcheck     Run /health logic once and exit (0=healthy, 1=degraded)
  config migrate        Surface new config keys as a commented diff (writes .new file)
  config migrate-force  Back up the existing config and write defaults (recovery)
  update          Replace the binary from the latest release and restart the service

Environment:
  RT_AGENT_PORT       Listen port (default 11435)
  RT_AGENT_BIND       Bind address (default 0.0.0.0)
  RT_AGENT_TOKEN      Bearer token for /actions/*
  RT_AGENT_CONFIG     Config file path (override default location)
  RT_AGENT_METRICS    Set to 1 to enable /metrics endpoint
`

func main() {
	// Custom flag set so we control usage text.
	flag.CommandLine.SetOutput(io.Discard)
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}

	// Windows SCM detection happens inside runCommand via internal/service.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	err := dispatch(cmd, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		slog.Error("command failed", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "run":
		return runServer(args)
	case "install":
		return service.Install()
	case "uninstall":
		return service.Uninstall()
	case "status":
		return service.PrintStatus()
	case "start":
		return service.Start()
	case "stop":
		return service.Stop()
	case "version", "--version", "-v":
		return printVersion()
	case "healthcheck":
		return runHealthcheck()
	case "config":
		return runConfigCommand(args)
	case "update":
		return fmt.Errorf("update: not implemented yet; re-run install.sh to upgrade")
	case "help", "--help", "-h":
		fmt.Fprint(os.Stderr, usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command: %q", cmd)
	}
}

func runServer(_ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	reporter, err := health.NewReporter(cfg)
	if err != nil {
		return fmt.Errorf("build reporter: %w", err)
	}

	srv := server.New(cfg, reporter)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// On Windows the process may need to integrate with the SCM. service.RunIfWindowsService
	// returns (true, err) if it handled the lifecycle, otherwise (false, nil) and we run
	// in the foreground.
	handled, err := service.RunIfWindowsService(ctx, srv, reporter)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}

	slog.Info("rt-node-agent starting",
		"version", buildinfo.Version,
		"addr", srv.Addr(),
	)
	return srv.Run(ctx)
}

func runHealthcheck() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	reporter, err := health.NewReporter(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rep, err := reporter.Report(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return err
	}
	if rep.Degraded {
		os.Exit(1)
	}
	return nil
}

// runConfigCommand dispatches subcommands under `rt-node-agent config ...`.
// Subcommands:
//
//	migrate         Compare the on-disk config against the embedded default
//	                and write a `.new` sibling with missing top-level keys
//	                appended (commented). Never modifies the original file.
//	migrate-force   Back up the existing config to <path>.broken-<unix-ts>
//	                and write a fresh defaults file in its place. Recovery
//	                path for broken YAML; explicit operator opt-in for
//	                replacing a v0.1.x config with v0.2.0 defaults.
func runConfigCommand(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rt-node-agent config (migrate | migrate-force)")
		return fmt.Errorf("missing subcommand")
	}
	path := os.Getenv("RT_AGENT_CONFIG")
	if path == "" {
		path = config.DefaultConfigPath()
	}
	switch args[0] {
	case "migrate":
		res, err := migrate.Migrate(path, config.DefaultYAML)
		if err != nil {
			// On broken YAML, point the operator at the recovery command
			// rather than just dumping the parse error.
			if errors.Is(err, migrate.ErrBrokenYAML) {
				fmt.Fprintf(os.Stderr,
					"config at %s is not valid YAML.\nRun: sudo rt-node-agent config migrate-force\nThat backs up the broken file and writes defaults.\nCause: %v\n",
					path, err)
				return err
			}
			return err
		}
		if res.AlreadyCurrent {
			fmt.Println("config is up-to-date (v" + itoa(res.NewVersion) + ")")
			return nil
		}
		fmt.Print(res.Banner(path))
		return nil
	case "migrate-force":
		backup, err := migrate.ForceReset(path, config.DefaultYAML)
		if err != nil {
			return err
		}
		if backup == "" {
			fmt.Printf("wrote fresh config at %s (no prior file)\n", path)
		} else {
			fmt.Printf("backed up %s → %s\nwrote fresh config at %s (from v%d defaults)\nReview, edit, then: sudo systemctl restart rt-node-agent\n",
				path, backup, path, config.SchemaVersion)
		}
		return nil
	default:
		fmt.Fprintln(os.Stderr, "unknown config subcommand:", args[0])
		fmt.Fprintln(os.Stderr, "available: migrate, migrate-force")
		return fmt.Errorf("unknown subcommand")
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func printVersion() error {
	out := map[string]string{
		"version":    buildinfo.Version,
		"git_sha":    buildinfo.GitSHA,
		"build_time": buildinfo.BuildTime,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
