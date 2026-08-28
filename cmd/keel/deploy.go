package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/smart-minds/keel/internal/config"
	"github.com/smart-minds/keel/internal/engine"
	"github.com/smart-minds/keel/internal/secretbox"
	"github.com/smart-minds/keel/internal/store/devdb"
	"github.com/spf13/cobra"
)

func newDeployCmd() *cobra.Command {
	var targetName string
	var varFlags []string
	var varFile string
	cmd := &cobra.Command{
		Use:   "deploy <deployment>",
		Short: "Run a deployment from the terminal",
		Long: `Runs a deployment headlessly: builds the environment image and executes the
steps, streaming output to the terminal.

Values come from, in increasing precedence:
  1. defaults declared in keel.yaml
  2. the saved values of --target (as configured in the Keel UI)
  3. an env-style file passed with --var-file
  4. individual --var NAME=value flags`,
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

			values := map[string]string{}
			display := targetName
			if targetName != "" {
				saved, err := loadTargetValues(cmd.Context(), dir, d, targetName)
				if err != nil {
					return err
				}
				for k, v := range saved {
					values[k] = v
				}
			} else {
				display = "cli"
			}
			if varFile != "" {
				fileVals, err := parseVarFile(varFile)
				if err != nil {
					return err
				}
				for k, v := range fileVals {
					values[k] = v
				}
			}
			for _, kv := range varFlags {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("invalid --var %q (expected NAME=value)", kv)
				}
				values[strings.TrimSpace(k)] = v
			}
			// Drop values for variables the deployment doesn't declare, so a
			// var-file shared across deployments works.
			for k := range values {
				if d.Variable(k) == nil {
					delete(values, k)
				}
			}

			if problems := config.CheckValues(d, values); len(problems) > 0 {
				fmt.Fprintln(os.Stderr, "The deployment is missing or has invalid values:")
				deployTime := false
				for _, p := range problems {
					fmt.Fprintf(os.Stderr, "  - %s %s\n", p.Name, p.Message)
					if v := d.Variable(p.Name); v != nil && v.DeployTime {
						deployTime = true
					}
				}
				if deployTime {
					fmt.Fprintln(os.Stderr, "Deploy-time variables are passed per run: --var NAME=value")
				}
				return fmt.Errorf("%d value problem(s)", len(problems))
			}

			runner := &engine.Runner{}
			if err := runner.CheckDocker(cmd.Context()); err != nil {
				return err
			}

			env := config.ResolveValues(d, values)
			var secrets []string
			for _, v := range d.Variables {
				if v.Secret && env[v.Name] != "" {
					secrets = append(secrets, env[v.Name])
				}
			}
			steps := make([]engine.Step, len(d.Steps))
			for i, st := range d.Steps {
				steps[i] = engine.Step{Name: st.Name, Run: st.Run}
			}
			runID := uuid.NewString()[:8]
			spec := engine.Spec{
				RunID:        runID,
				Deployment:   d.Name,
				Target:       display,
				RepoDir:      dir,
				Dockerfile:   d.Environment.Dockerfile,
				Context:      d.Environment.Context,
				Steps:        steps,
				Env:          env,
				SecretValues: secrets,
				Outputs:      d.OutputNames(),
				ImageTag:     "keel/dev-" + d.Name,
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Printf("Deploying %s", d.Name)
			if targetName != "" {
				fmt.Printf(" → target %s", targetName)
			}
			fmt.Printf(" (run %s)\n\n", runID)

			res, err := runner.Run(ctx, spec, &cliSink{})
			if err != nil {
				if res.Canceled {
					return fmt.Errorf("deployment canceled")
				}
				return err
			}
			fmt.Println("\n✓ Deployment succeeded")
			if len(res.Outputs) > 0 {
				fmt.Println("\nOutputs:")
				for _, o := range d.Outputs {
					value, ok := res.Outputs[o.Name]
					if !ok {
						continue
					}
					if o.Secret {
						fmt.Printf("  %s = ••• (secret — revealable in the Keel UI)\n", o.Name)
						continue
					}
					// A value that carries a secret input must never print.
					hidden := false
					for _, sv := range secrets {
						if sv != "" && strings.Contains(value, sv) {
							hidden = true
							break
						}
					}
					if hidden {
						fmt.Printf("  %s = ••• (contains a secret value)\n", o.Name)
					} else {
						fmt.Printf("  %s = %s\n", o.Name, value)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&targetName, "target", "t", "", "deployment target whose saved values to use")
	cmd.Flags().StringArrayVar(&varFlags, "var", nil, "set a variable, NAME=value (repeatable)")
	cmd.Flags().StringVar(&varFile, "var-file", "", "env-style file (NAME=value per line) of variable values")
	return cmd
}

// loadTargetValues decrypts saved values for a named target from .keel/dev.db.
func loadTargetValues(ctx context.Context, dir string, d *config.Deployment, targetName string) (map[string]string, error) {
	dbPath := filepath.Join(dir, ".keel", "dev.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no local Keel state at %s — create the target in `keel dev` first", dbPath)
	}
	db, err := devdb.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	q := devdb.New(db)
	t, err := q.GetTargetByName(ctx, devdb.GetTargetByNameParams{Deployment: d.Name, Name: targetName})
	if err != nil {
		return nil, fmt.Errorf("no target named %q for deployment %q — create it in `keel dev` first", targetName, d.Name)
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		cfgDir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	box, err := secretbox.LoadOrCreateKeyFile(filepath.Join(cfgDir, "keel", "dev.key"))
	if err != nil {
		return nil, err
	}
	rows, err := q.ListTargetValues(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, row := range rows {
		if d.Variable(row.VarName) == nil {
			continue
		}
		plain, err := box.OpenString(row.ValueEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", row.VarName, err)
		}
		values[row.VarName] = plain
	}
	return values, nil
}

// parseVarFile reads an env-style file: NAME=value per line, # comments.
func parseVarFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected NAME=value", path, i+1)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// cliSink prints engine output to the terminal.
type cliSink struct{}

func (c *cliSink) Log(line string) { fmt.Println(line) }

func (c *cliSink) Phase(p engine.Phase) {}

func (c *cliSink) StepStatus(idx int, status engine.StepStatus) {
	if status == engine.StepFailed {
		fmt.Fprintf(os.Stderr, "✗ step %d failed\n", idx+1)
	}
}
