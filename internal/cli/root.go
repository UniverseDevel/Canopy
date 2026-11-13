// Package cli implements the canopy command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/buildinfo"
	"github.com/UniverseDevel/Canopy/internal/connections"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// SchemaVersion is the version of the JSON output schema.
const SchemaVersion = 1

type globals struct {
	output         string
	nonInteractive bool
	logLevel       string
	logFile        string
	connFiles      []string

	stdout io.Writer
	stderr io.Writer
	log    *slog.Logger
	logF   *os.File
}

// envelope is the JSON output of every command.
type envelope struct {
	SchemaVersion int           `json:"schemaVersion"`
	Command       string        `json:"command"`
	OK            bool          `json:"ok"`
	Result        any           `json:"result,omitempty"`
	Error         *apperr.Error `json:"error,omitempty"`
}

// Main runs the CLI and returns the exit code.
func Main(args []string) int {
	g := &globals{stdout: os.Stdout, stderr: os.Stderr}
	root := newRoot(g)
	root.SetArgs(args)
	cmd, err := root.ExecuteContextC(context.Background())
	if g.logF != nil {
		defer g.logF.Close()
	}
	if err == nil {
		return 0
	}
	ae := apperr.As(err)
	if !isAppErr(err) {
		// cobra flag and argument errors
		ae = apperr.New(apperr.ClassUsage, apperr.CodeUsage, "%s", err.Error())
	}
	name := "canopy"
	if cmd != nil {
		name = strings.TrimPrefix(cmd.CommandPath(), "canopy ")
	}
	if g.log != nil {
		g.log.Error("command failed", "command", name, "code", ae.Code, "grpcCode", ae.GRPCCode, "error", ae.Message)
	}
	if g.output == "json" {
		g.writeJSON(envelope{SchemaVersion: SchemaVersion, Command: name, OK: false, Error: ae})
	} else {
		fmt.Fprintf(g.stderr, "error [%s]: %s\n", ae.Code, ae.Error())
		for k, v := range ae.Details {
			if k != "errors" {
				fmt.Fprintf(g.stderr, "  %s: %s\n", k, v)
			}
		}
		if d := ae.Details["errors"]; d != "" {
			fmt.Fprintln(g.stderr, d)
		}
	}
	if ae.Class == 0 {
		return int(apperr.ClassInternal)
	}
	return int(ae.Class)
}

func isAppErr(err error) bool {
	var ae *apperr.Error
	return errors.As(err, &ae)
}

func newRoot(g *globals) *cobra.Command {
	root := &cobra.Command{
		Use:           "canopy",
		Short:         "Vendor-agnostic gNMI and YANG browser",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if g.output != "text" && g.output != "json" {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--output must be text or json")
			}
			connections.ExtraFiles = g.connFiles
			return g.setupLog()
		},
	}
	root.PersistentFlags().StringVarP(&g.output, "output", "o", "text", "output format: text or json")
	root.PersistentFlags().BoolVar(&g.nonInteractive, "non-interactive", false, "never prompt (also implied when stdin isn't a terminal)")
	root.PersistentFlags().StringVar(&g.logLevel, "log-level", "", "log level: debug, info, warn, error")
	root.PersistentFlags().StringVar(&g.logFile, "log-file", "", "write structured JSON logs to this file")
	root.PersistentFlags().StringSliceVar(&g.connFiles, "connections", nil, "extra read-only saved-connections file(s), e.g. lab/connections.yaml (also $CANOPY_CONNECTIONS)")
	root.AddCommand(newVersionCmd(g), newConnCmd(g), newModelsCmd(g), newSetCmd(g), newSnapshotCmd(g), newShadowCmd(g), newOperatorCmd(g), newFixtureCmd(g), newProxyCmd(g), newConnectCmd(g), newGetCmd(g), newTreeCmd(g), newGenerateCmd(g), newUICmd(g), newTUICmd(g))
	return root
}

func (g *globals) setupLog() error {
	level := slog.LevelInfo
	switch strings.ToLower(g.logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "", "info":
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown --log-level %q", g.logLevel)
	}
	var w io.Writer
	switch {
	case g.logFile != "":
		f, err := os.OpenFile(g.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		g.logF = f
		w = f
	case g.logLevel != "":
		w = g.stderr
	}
	if w == nil {
		g.log = slog.New(slog.DiscardHandler)
		return nil
	}
	g.log = slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})).With("canopy", buildinfo.Get().Version)
	return nil
}

func (g *globals) interactive() bool {
	return !g.nonInteractive && term.IsTerminal(int(os.Stdin.Fd()))
}

func (g *globals) writeJSON(v any) {
	enc := json.NewEncoder(g.stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// result writes a successful result: JSON envelope, or text from textFn.
func (g *globals) result(cmd *cobra.Command, v any, textFn func(w io.Writer)) error {
	if g.output == "json" {
		g.writeJSON(envelope{SchemaVersion: SchemaVersion, Command: strings.TrimPrefix(cmd.CommandPath(), "canopy "), OK: true, Result: v})
		return nil
	}
	textFn(g.stdout)
	return nil
}

func (g *globals) service() *app.Service { return app.New(g.log) }

func (g *globals) prompt(label string, secret bool) (string, error) {
	fmt.Fprint(g.stderr, label)
	if secret {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(g.stderr)
		return string(b), err
	}
	var s string
	_, err := fmt.Fscanln(os.Stdin, &s)
	return s, err
}

func ctx(cmd *cobra.Command) context.Context { return cmd.Context() }

func newVersionCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := buildinfo.Get()
			return g.result(cmd, info, func(w io.Writer) {
				fmt.Fprintf(w, "canopy %s\ncommit: %s\ndate:   %s\ngo:     %s\n", info.Version, info.Commit, info.Date, info.GoVersion)
			})
		},
	}
}
