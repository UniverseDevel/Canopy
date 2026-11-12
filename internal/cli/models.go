package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/models"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/spf13/cobra"
)

func newModelsCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "models",
		Short: "List, download and detect YANG model sets from GitHub",
		Long: `Model sets are downloaded from GitHub on demand (no clone of the large
repositories) and cached in the user cache folder, so each version is fetched once.
Set GITHUB_TOKEN if the GitHub API rate limit (60 requests an hour without a
token) gets in the way.`,
	}
	c.AddCommand(newModelsSourcesCmd(g), newModelsVersionsCmd(g), newModelsFetchCmd(g), newModelsDetectCmd(g), newModelsReportCmd(g))
	return c
}

func newModelsSourcesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "sources",
		Short: "List model sources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return g.result(cmd, models.Sources, func(w io.Writer) {
				for _, s := range models.Sources {
					fmt.Fprintf(w, "%-11s %-16s github.com/%s  %s\n", s.ID, s.Name, s.Repo, s.Description)
				}
			})
		},
	}
}

func newModelsVersionsCmd(g *globals) *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "versions SOURCE",
		Short: "List the versions of a model source (newest first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vs, err := g.service().Models.Versions(ctx(cmd), args[0], refresh)
			if err != nil {
				return err
			}
			return g.result(cmd, vs, func(w io.Writer) {
				for _, v := range vs {
					c := ""
					if v.Cached {
						c = "  (cached)"
					}
					fmt.Fprintf(w, "%s%s\n", v.Name, c)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "list again from GitHub instead of the cached listing (kept a day)")
	return cmd
}

func newModelsFetchCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "fetch SOURCE VERSION",
		Short: "Download a model set and print the folders to load",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			set, err := g.service().Models.Fetch(ctx(cmd), args[0], args[1], g.progress())
			if err != nil {
				return err
			}
			return g.result(cmd, set, func(w io.Writer) {
				for _, d := range set.Dirs {
					fmt.Fprintln(w, d)
				}
			})
		},
	}
}

func newModelsDetectCmd(g *globals) *cobra.Command {
	var f targetFlags
	var fetch bool
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Detect a device's platform and the matching model set",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			var res any
			var d *models.Detection
			var dirs []string
			if fetch {
				r, err := svc.AutoModels(ctx(cmd), sess.ID, g.progress())
				if err != nil {
					return err
				}
				d = r.Detection
				if r.Set != nil {
					dirs = r.Set.Dirs
				}
				res = r
			} else {
				d, err = svc.DetectModels(ctx(cmd), sess.ID)
				if err != nil {
					return err
				}
				res = d
			}
			return g.result(cmd, res, func(w io.Writer) {
				if d.Source == "" {
					fmt.Fprintln(w, "Platform not recognized:", d.Reason)
					return
				}
				fmt.Fprintf(w, "source:  %s\ndevice:  %s\nmodels:  %s (exact: %v)\nreason:  %s\n", d.Source, d.DeviceVersion, d.Version, d.Exact, d.Reason)
				for _, x := range dirs {
					fmt.Fprintf(w, "folder:  %s\n", x)
				}
			})
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().BoolVar(&fetch, "fetch", false, "also download the detected model set and load it")
	return cmd
}

// progress prints download progress to stderr in text mode.
func (g *globals) progress() func(string) {
	if g.output == "json" {
		return nil
	}
	return func(s string) { fmt.Fprintln(g.stderr, s) }
}

// resolveModels turns "source@version" into folders, downloading if needed.
func (g *globals) resolveModels(cmd *cobra.Command, spec string) (*models.Set, error) {
	src, ver, ok := strings.Cut(spec, "@")
	if !ok {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--models must be SOURCE@VERSION, e.g. srlinux@v26.7.1 (see 'canopy models sources')")
	}
	return g.service().Models.Fetch(ctx(cmd), src, ver, g.progress())
}

func newModelsReportCmd(g *globals) *cobra.Command {
	var f targetFlags
	var dirs []string
	var spec string
	var all bool
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Compare the models a device advertises with the loaded models",
		Long: `Connects, loads models (--models SOURCE@VERSION, --yang folders, or detected
from the device when neither is given) and lists each advertised model as match,
mismatch (other revision or OpenConfig version), loaded (no comparable version) or
missing. --all also lists loaded modules the device doesn't advertise.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			switch {
			case spec != "":
				src, ver, _ := strings.Cut(spec, "@")
				if _, err := svc.LoadModels(ctx(cmd), src, ver, g.progress()); err != nil {
					return err
				}
			case len(dirs) > 0:
				if _, err := svc.LoadSchema(dirs, schema.LoadOptions{}); err != nil {
					return err
				}
			default:
				res, err := svc.AutoModels(ctx(cmd), sess.ID, g.progress())
				if err != nil {
					return err
				}
				if res.Schema == nil {
					return apperr.New(apperr.ClassSchema, apperr.CodeSchema, "could not detect the models: %s", res.Detection.Reason).
						WithHint("pass --models SOURCE@VERSION or --yang")
				}
			}
			rep, err := svc.MatchReport(sess.ID)
			if err != nil {
				return err
			}
			if !all {
				var keep []models.MatchEntry
				for _, e := range rep.Entries {
					if e.Status != models.MatchNotAdv {
						keep = append(keep, e)
					}
				}
				rep.Entries = keep
			}
			return g.result(cmd, rep, func(w io.Writer) {
				for _, e := range rep.Entries {
					fmt.Fprintf(w, "%-9s %-55s %-22s %s\n", e.Status, e.Module, e.Advertised, e.Loaded)
				}
				fmt.Fprintf(w, "\nmatch %d, mismatch %d, loaded %d, missing %d, extra %d\n",
					rep.Counts[models.MatchOK], rep.Counts[models.MatchVersion], rep.Counts[models.MatchUnknown],
					rep.Counts[models.MatchMissing], rep.Counts[models.MatchNotAdv])
			})
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().StringVar(&spec, "models", "", "model set SOURCE@VERSION (default: detect)")
	cmd.Flags().StringSliceVarP(&dirs, "yang", "y", nil, "local YANG folders instead")
	cmd.Flags().BoolVar(&all, "all", false, "also list loaded modules the device doesn't advertise")
	return cmd
}
