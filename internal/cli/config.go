package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/config"
	"github.com/UniverseDevel/Canopy/internal/format"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/snapshot"
	"github.com/spf13/cobra"
)

func newSetCmd(g *globals) *cobra.Command {
	var f targetFlags
	var upPaths, upVals, repPaths, repVals, dels []string
	var file, spec, enc string
	var dry, yes bool
	var yangDirs []string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Change device configuration (preview, validate, then apply when confirmed)",
		Long: `Builds a gNMI Set from --update-path/--update-value, --replace-path/--replace-value
(paired in order) and --delete, or from --file with {"ops":[{"op","path","value"}]}.
Values are JSON (JSON_IETF), e.g. --update-value '"leaf1"' or '{"mtu":9000}'.

Nothing is sent without --yes (or a confirmation at the prompt). --dry-run only
validates and previews. Values are validated against the models given with
--models or --yang (or detected from the device). The config at the changed paths
is read before and after, and the difference is shown.

Only change lab devices, and only with the owner's approval.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := buildSetRequest(file, upPaths, upVals, repPaths, repVals, dels, enc)
			if err != nil {
				return err
			}
			t, err := g.target(&f, true)
			if err != nil {
				return err
			}
			svc := g.service()
			defer svc.Close()
			sess, err := g.connect(ctx(cmd), svc, t)
			if err != nil {
				return err
			}
			loadModelsFor(cmd, g, svc, sess.ID, spec, yangDirs)
			out, err := svc.Set(ctx(cmd), sess.ID, req, app.SetOptions{DryRun: true})
			if err != nil {
				return err
			}
			if !dry {
				confirmed := yes
				if !yes && g.interactive() {
					fmt.Fprintln(g.stderr, out.Preview)
					for _, p := range out.Problems {
						fmt.Fprintln(g.stderr, "problem:", p)
					}
					warn := ""
					if out.Destructive {
						warn = " (REPLACE/DELETE: existing config is removed)"
					}
					ans, _ := g.prompt(fmt.Sprintf("Apply to %s%s? Type yes: ", sess.Address, warn), false)
					confirmed = strings.EqualFold(strings.TrimSpace(ans), "yes")
				}
				if !confirmed {
					return apperr.New(apperr.ClassUsage, "confirmation_required", "not applied: confirm with --yes (or answer yes)").
						WithHint("use --dry-run to only preview")
				}
				out, err = svc.Set(ctx(cmd), sess.ID, req, app.SetOptions{Confirm: true})
				if err != nil {
					return err
				}
			}
			return g.result(cmd, out, func(w io.Writer) {
				fmt.Fprintln(w, out.Preview)
				for _, p := range out.Problems {
					fmt.Fprintln(w, "problem:", p)
				}
				for _, c := range out.Gnmic {
					fmt.Fprintf(w, "# %s\n%s\n", c.Title, c.Text)
				}
				if !out.Sent {
					fmt.Fprintln(w, "(dry run: nothing sent)")
					return
				}
				fmt.Fprintf(w, "applied in %d ms\n", out.Result.ElapsedMs)
				printDiff(w, out.Diff)
			})
		},
	}
	addTargetFlags(cmd, &f)
	fl := cmd.Flags()
	fl.StringArrayVar(&upPaths, "update-path", nil, "path to update (merge)")
	fl.StringArrayVar(&upVals, "update-value", nil, "JSON value for the matching --update-path")
	fl.StringArrayVar(&repPaths, "replace-path", nil, "path to replace")
	fl.StringArrayVar(&repVals, "replace-value", nil, "JSON value for the matching --replace-path")
	fl.StringArrayVar(&dels, "delete", nil, "path to delete")
	fl.StringVar(&file, "file", "", "JSON file with the changes")
	fl.StringVarP(&enc, "encoding", "e", "", "json_ietf (default) or json")
	fl.BoolVar(&dry, "dry-run", false, "validate and preview only")
	fl.BoolVar(&yes, "yes", false, "apply without asking")
	fl.StringVar(&spec, "models", "", "models for validation, SOURCE@VERSION (default: detect)")
	fl.StringSliceVarP(&yangDirs, "yang", "y", nil, "local YANG folders for validation")
	return cmd
}

func buildSetRequest(file string, upP, upV, repP, repV, dels []string, enc string) (config.Request, error) {
	var r config.Request
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return r, apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return r, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "%s: %v", file, err)
		}
	}
	if len(upP) != len(upV) || len(repP) != len(repV) {
		return r, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "each --update-path/--replace-path needs its --update-value/--replace-value")
	}
	for i := range upP {
		r.Ops = append(r.Ops, config.Op{Op: "update", Path: upP[i], Value: json.RawMessage(upV[i])})
	}
	for i := range repP {
		r.Ops = append(r.Ops, config.Op{Op: "replace", Path: repP[i], Value: json.RawMessage(repV[i])})
	}
	for _, d := range dels {
		r.Ops = append(r.Ops, config.Op{Op: "delete", Path: d})
	}
	if enc != "" {
		r.Encoding = enc
	}
	if len(r.Ops) == 0 {
		return r, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no changes: use --update-path/--update-value, --replace-path/--replace-value, --delete or --file")
	}
	return r, nil
}

// loadModelsFor loads models for validation; failures only mean no validation.
func loadModelsFor(cmd *cobra.Command, g *globals, svc *app.Service, id, spec string, dirs []string) {
	switch {
	case spec != "":
		src, ver, _ := strings.Cut(spec, "@")
		if _, err := svc.LoadModels(ctx(cmd), src, ver, g.progress()); err != nil {
			fmt.Fprintln(g.stderr, "models not loaded (no validation):", apperr.As(err).Message)
		}
	case len(dirs) > 0:
		if _, err := svc.LoadSchema(dirs, schema.LoadOptions{}); err != nil {
			fmt.Fprintln(g.stderr, "models not loaded (no validation):", apperr.As(err).Message)
		}
	default:
		if res, err := svc.AutoModels(ctx(cmd), id, g.progress()); err != nil || res.Schema == nil {
			fmt.Fprintln(g.stderr, "models not detected; values are not validated")
		}
	}
}

func printDiff(w io.Writer, d *snapshot.DiffResult) {
	if d == nil {
		return
	}
	fmt.Fprintf(w, "changes: %d added, %d removed, %d changed (%d unchanged)\n", d.Added, d.Removed, d.Changed, d.Unchanged)
	for _, c := range d.Changes {
		switch c.Kind {
		case "added":
			fmt.Fprintf(w, "+ %s = %s\n", c.Path, format.Scalar(c.After))
		case "removed":
			fmt.Fprintf(w, "- %s = %s\n", c.Path, format.Scalar(c.Before))
		default:
			fmt.Fprintf(w, "~ %s: %s -> %s\n", c.Path, format.Scalar(c.Before), format.Scalar(c.After))
		}
	}
}

func newSnapshotCmd(g *globals) *cobra.Command {
	c := &cobra.Command{Use: "snapshot", Short: "Save device reads and compare them"}
	var f targetFlags
	var paths, ignore []string
	var typ, name, note string
	take := &cobra.Command{
		Use:   "take",
		Short: "Read paths and save them as a snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(paths) == 0 && f.preset != "" {
				if p, err := g.presets().Get(f.preset); err == nil {
					paths = p.Paths
				}
			}
			if len(paths) == 0 {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--path is required")
			}
			t, err := g.target(&f, true)
			if err != nil {
				return err
			}
			svc := g.service()
			defer svc.Close()
			sess, err := g.connect(ctx(cmd), svc, t)
			if err != nil {
				return err
			}
			m, err := svc.TakeSnapshot(ctx(cmd), sess.ID, live.GetRequest{Paths: paths, Type: typ}, name, note)
			if err != nil {
				return err
			}
			return g.result(cmd, m, func(w io.Writer) { fmt.Fprintf(w, "saved snapshot %s (%d leaves)\n", m.Name, m.Leaves) })
		},
	}
	addTargetFlags(take, &f)
	take.Flags().StringSliceVarP(&paths, "path", "p", nil, "paths to read")
	take.Flags().StringVarP(&typ, "type", "t", "all", "data type: all, config, state, operational")
	take.Flags().StringVar(&name, "name", "", "snapshot name (default: device-date-time)")
	take.Flags().StringVar(&note, "note", "", "free text note")

	list := &cobra.Command{
		Use: "list", Short: "List snapshots", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l, err := g.service().Snapshots().List()
			if err != nil {
				return err
			}
			return g.result(cmd, l, func(w io.Writer) {
				for _, m := range l {
					fmt.Fprintf(w, "%-40s %-20s %s  %d leaves  %s\n", m.Name, m.Device, m.Time.Local().Format("2006-01-02 15:04"), m.Leaves, strings.Join(m.Paths, " "))
				}
			})
		},
	}
	var df targetFlags
	diff := &cobra.Command{
		Use:   "diff A [B]",
		Short: "Compare two snapshots, or a snapshot with the device now (--conn/--target)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := g.service()
			defer svc.Close()
			other, id := "", ""
			if len(args) == 2 {
				other = args[1]
			} else {
				t, err := g.target(&df, true)
				if err != nil {
					return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "give a second snapshot, or --conn/--target to compare with the device now")
				}
				sess, err := g.connect(ctx(cmd), svc, t)
				if err != nil {
					return err
				}
				id = sess.ID
			}
			d, err := svc.CompareSnapshot(ctx(cmd), args[0], other, id, ignore)
			if err != nil {
				return err
			}
			return g.result(cmd, d, func(w io.Writer) { printDiff(w, d) })
		},
	}
	addTargetFlags(diff, &df)
	diff.Flags().StringSliceVar(&ignore, "ignore", nil, "skip paths containing these fragments (e.g. statistics,current-datetime)")
	rm := &cobra.Command{
		Use: "rm NAME", Short: "Delete a snapshot", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.service().Snapshots().Delete(args[0]); err != nil {
				return err
			}
			return g.result(cmd, map[string]string{"deleted": args[0]}, func(w io.Writer) { fmt.Fprintf(w, "deleted %s\n", args[0]) })
		},
	}
	c.AddCommand(take, list, diff, rm)
	return c
}
