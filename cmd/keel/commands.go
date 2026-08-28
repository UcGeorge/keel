package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/smart-minds/keel/internal/config"
	"github.com/smart-minds/keel/internal/devserver"
	"github.com/smart-minds/keel/internal/manifest"
	"github.com/smart-minds/keel/internal/version"
	"github.com/smart-minds/keel/internal/web"
	"github.com/spf13/cobra"
)

// workDir resolves the repository directory for a command.
func workDir(args []string) (string, error) {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	return filepath.Abs(dir)
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [dir]",
		Short: "Create starter keel.yaml and deploy/Dockerfile files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workDir(args)
			if err != nil {
				return err
			}
			res, err := config.Init(dir)
			if err != nil {
				return err
			}
			if res.CreatedConfig {
				fmt.Printf("Created %s\n", res.ConfigPath)
			} else {
				fmt.Printf("Kept existing configuration in %s\n", dir)
			}
			if res.CreatedDockerfile {
				fmt.Printf("Created %s\n", res.DockerfilePath)
			}
			fmt.Println("\nNext steps:")
			fmt.Println("  1. Edit keel.yaml — define your deployments, steps, and variables")
			fmt.Println("  2. Edit deploy/Dockerfile — install the tools your steps need")
			fmt.Println("  3. Run `keel dev` to open the local UI and deploy")
			return nil
		},
	}
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [dir]",
		Short: "Validate the keel.yaml configuration",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workDir(args)
			if err != nil {
				return err
			}
			path, err := config.Find(dir)
			if err != nil {
				return err
			}
			cfg, err := config.LoadFile(path)
			if err != nil {
				var verrs *config.ValidationErrors
				if errors.As(err, &verrs) {
					fmt.Printf("✗ %s is invalid:\n", path)
					for _, e := range verrs.Errors {
						fmt.Printf("  - %s\n", e.Error())
					}
					return fmt.Errorf("%d problem(s) found", len(verrs.Errors))
				}
				return err
			}
			fmt.Printf("✓ %s is valid\n", path)
			for _, d := range cfg.Deployments {
				fmt.Printf("  deployment %-24s %d step(s), %d variable(s)\n", d.Name, len(d.Steps), len(d.Variables))
			}
			return nil
		},
	}
}

func newDevCmd() *cobra.Command {
	var port int
	var host string
	cmd := &cobra.Command{
		Use:   "dev [dir]",
		Short: "Run the Keel UI for this repository",
		Long: `Starts the local Keel UI with the repository set to [dir] (default: the
current directory). If the directory has no keel.yaml yet, starter files are
created first. Docker must be running to execute deployments.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workDir(args)
			if err != nil {
				return err
			}
			if _, err := config.Find(dir); err != nil {
				fmt.Printf("No keel.yaml in %s — initializing it first.\n\n", dir)
				res, err := config.Init(dir)
				if err != nil {
					return err
				}
				fmt.Printf("Created %s\nCreated %s\n\n", res.ConfigPath, res.DockerfilePath)
			}

			srv, err := devserver.New(dir, version.Version)
			if err != nil {
				return err
			}
			defer srv.Close()

			addr := fmt.Sprintf("%s:%d", host, port)
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", addr, err)
			}
			httpSrv := &http.Server{Handler: srv.Handler()}

			if err := srv.Runner.CheckDocker(cmd.Context()); err != nil {
				fmt.Printf("⚠ %v\n  Deployments will fail until Docker is running.\n\n", err)
			}
			fmt.Printf("Keel dev running\n\n  Repository:  %s\n  UI:          http://%s\n\nPress Ctrl+C to stop.\n", dir, displayAddr(ln.Addr().String(), host))

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errCh := make(chan error, 1)
			go func() { errCh <- httpSrv.Serve(ln) }()
			select {
			case err := <-errCh:
				return err
			case <-ctx.Done():
				fmt.Println("\nShutting down…")
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = httpSrv.Shutdown(shutdownCtx)
				return nil
			}
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 3400, "port to listen on")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "host to bind")
	return cmd
}

func displayAddr(actual, host string) string {
	if strings.HasPrefix(actual, "0.0.0.0") || strings.HasPrefix(actual, "[::]") {
		_, port, _ := net.SplitHostPort(actual)
		return "localhost:" + port
	}
	_ = host
	return actual
}

func newManifestCmd() *cobra.Command {
	var out string
	var format string
	var vars []string
	var project string
	cmd := &cobra.Command{
		Use:   "manifest <deployment>",
		Short: "Generate the variable manifest for a deployment",
		Long: `Generates the document you hand to whoever must supply the deployment's
values: each selected variable with its description, why it is needed, and
how to obtain it — exactly as specified in keel.yaml.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := workDir(nil)
			if err != nil {
				return err
			}
			cfg, err := config.LoadDir(dir)
			if err != nil {
				return err
			}
			d := cfg.Deployment(args[0])
			if d == nil {
				return fmt.Errorf("no deployment named %q (have: %s)", args[0], strings.Join(cfg.DeploymentNames(), ", "))
			}
			opts := manifest.Options{ProjectName: project}
			if opts.ProjectName == "" {
				opts.ProjectName = filepath.Base(dir)
			}
			if len(vars) > 0 {
				opts.Select = manifest.SortSelection(d, vars)
			}
			doc, err := manifest.Generate(d, opts)
			if err != nil {
				return err
			}
			var payload string
			switch format {
			case "md", "markdown":
				payload = doc
			case "html":
				payload = web.StandaloneManifestHTML(opts.ProjectName, doc)
			default:
				return fmt.Errorf("unknown format %q (expected md or html)", format)
			}
			if out == "" || out == "-" {
				fmt.Print(payload)
				return nil
			}
			if err := os.WriteFile(out, []byte(payload), 0o644); err != nil {
				return err
			}
			fmt.Printf("Wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "output file (default: stdout)")
	cmd.Flags().StringVarP(&format, "format", "f", "md", "output format: md or html")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "variable to include (repeatable; default: the manifest selection from keel.yaml)")
	cmd.Flags().StringVar(&project, "project", "", "project name shown in the document header")
	return cmd
}

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
}
