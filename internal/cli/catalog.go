package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/catalog"
	"github.com/UniverseDevel/Canopy/internal/models"
)

// Catalog, profiles and prefetch commands under "canopy models".

func newModelsProfilesCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "profiles",
		Short: "List the platform profiles used to detect devices",
		Long: `A profile says how a platform is recognized from Capabilities, where its
software version is read, and how a version maps to a model release. Built-in
profiles can be replaced, and new ones added, with YAML files in the folder
'canopy models profiles' prints (same format, same id to replace).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ps, err := models.Profiles()
			if err != nil {
				return err
			}
			return g.result(cmd, map[string]any{"dir": models.ProfilesDir(), "profiles": ps}, func(w io.Writer) {
				for _, p := range ps {
					src := p.File
					if src == "" {
						src = "built-in"
					}
					fmt.Fprintf(w, "%-14s %-16s models: %-11s %s\n", p.ID, p.Name, p.Source, src)
				}
				fmt.Fprintf(w, "\nUser profiles: %s\n", models.ProfilesDir())
			})
		},
	}
}

func newModelsSearchCmd(g *globals) *cobra.Command {
	var q catalog.Query
	cmd := &cobra.Command{
		Use:   "search WORDS...",
		Short: "Search the model catalog (module name, prefix, namespace, description)",
		Long: `Searches the releases in the local catalog. A release is added when it is
downloaded, or listed with 'canopy models index' (file names only, nothing
downloaded). Every word must match the start of a word: "openconfig interf"
finds openconfig-interfaces.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q.Text = strings.Join(args, " ")
			c, err := g.service().Models.Catalog()
			if err != nil {
				return err
			}
			es, err := c.Search(ctx(cmd), q)
			if err != nil {
				return err
			}
			return g.result(cmd, es, func(w io.Writer) { printEntries(w, es) })
		},
	}
	cmd.Flags().StringVar(&q.Source, "source", "", "only this model source")
	cmd.Flags().StringVar(&q.Version, "version", "", "only this release")
	cmd.Flags().StringVar(&q.Module, "module", "", "exact module name")
	cmd.Flags().IntVar(&q.Limit, "limit", 50, "maximum results")
	return cmd
}

func newModelsContainingCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "containing MODULE",
		Short: "List the indexed releases that contain a module, with its revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.service().Models.Catalog()
			if err != nil {
				return err
			}
			es, err := c.Containing(ctx(cmd), args[0])
			if err != nil {
				return err
			}
			return g.result(cmd, es, func(w io.Writer) {
				if len(es) == 0 {
					fmt.Fprintln(w, "Not in any indexed release (see 'canopy models catalog').")
				}
				for _, e := range es {
					fmt.Fprintf(w, "%-11s %-24s %-11s %s\n", e.Source, e.Version, e.Revision, ocv(e))
				}
			})
		},
	}
}

func newModelsIndexCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "index SOURCE VERSION...",
		Short: "Add releases to the catalog",
		Long: `A downloaded release is indexed from its files (module, revision, prefix,
namespace, OpenConfig version, imports, deviations). Any other release is
indexed from a listing of its file names on GitHub: one API call, nothing is
downloaded, so module names and revisions become searchable across releases.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			m := g.service().Models
			var out []*models.IndexResult
			for _, v := range args[1:] {
				r, err := m.Index(ctx(cmd), args[0], models.NormalizeVersion(args[0], v))
				if err != nil {
					return err
				}
				out = append(out, r)
			}
			return g.result(cmd, out, func(w io.Writer) {
				for _, r := range out {
					how := "file names"
					if r.Enriched {
						how = "parsed files"
					}
					fmt.Fprintf(w, "%s %s: %d modules (%s)\n", r.Source, r.Version, r.Files, how)
				}
			})
		},
	}
}

func newModelsCatalogCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "catalog",
		Short: "List the releases in the catalog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.service().Models.Catalog()
			if err != nil {
				return err
			}
			rs, err := c.Releases()
			if err != nil {
				return err
			}
			return g.result(cmd, rs, func(w io.Writer) {
				if len(rs) == 0 {
					fmt.Fprintln(w, "The catalog is empty: download a release ('canopy models fetch') or list one ('canopy models index').")
				}
				for _, r := range rs {
					how := "names only"
					if r.Enriched {
						how = "parsed"
					}
					fmt.Fprintf(w, "%-11s %-24s %5d modules  %-10s %s\n", r.Source, r.Version, r.Files, how, r.IndexedAt.Local().Format("2006-01-02 15:04"))
				}
			})
		},
	}
}

func newModelsPrefetchCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "prefetch SOURCE VERSION...",
		Short: "Download model sets ahead of time, for offline use",
		Long: `Downloads and indexes model sets so they are available without internet
access (for example before working from an isolated jump host). Versions can be
written as the device reports them when the platform profile knows how to map
them: 'canopy models prefetch nxos "10.4(3)"' fetches 10.4-3.`,
		Example: `  canopy models prefetch srlinux v26.7.1 v26.3.1
  canopy models prefetch junos 23.4R2.13`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sets, err := g.service().Models.Prefetch(ctx(cmd), args[0], args[1:], g.progress())
			if err != nil {
				return err
			}
			return g.result(cmd, sets, func(w io.Writer) {
				for _, s := range sets {
					st := "downloaded"
					if s.Cached {
						st = "already cached"
					}
					fmt.Fprintf(w, "%s %s: %s\n", s.Source, s.Version, st)
				}
			})
		},
	}
}

func printEntries(w io.Writer, es []catalog.Entry) {
	if len(es) == 0 {
		fmt.Fprintln(w, "No matches.")
		return
	}
	for _, e := range es {
		fmt.Fprintf(w, "%-40s %-11s %-11s %s@%s\n", e.Module, e.Revision, ocv(e), e.Source, e.Version)
		if e.Description != "" {
			fmt.Fprintf(w, "    %s\n", e.Description)
		}
	}
}

func ocv(e catalog.Entry) string {
	if e.OCVersion != "" {
		return "oc " + e.OCVersion
	}
	return ""
}

func newModelsFromDeviceCmd(g *globals) *cobra.Command {
	var f targetFlags
	var req app.DeviceModelsRequest
	cmd := &cobra.Command{
		Use:   "from-device",
		Short: "Download the device's own YANG modules over NETCONF (get-schema)",
		Long: `Connects with gNMI as usual, then opens NETCONF over SSH (port 830 unless
--netconf-port says otherwise) on the device's address, with the same user name,
password and jump hosts, lists the schemas the device offers
(ietf-netconf-monitoring) and downloads each one with get-schema (RFC 6022).
Useful when a device's models aren't published. Read only. The modules are kept
per device in the model cache; --refresh downloads them again.`,
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
			req.Session = sess.ID
			req.AcceptHostKeys = append(req.AcceptHostKeys, t.AcceptHostKeys...)
			req.SaveHostKeys = t.SaveHostKeys
			res, err := svc.DeviceModels(ctx(cmd), req, g.progress())
			if err != nil {
				return err
			}
			return g.result(cmd, res, func(w io.Writer) {
				st := "downloaded"
				if res.Cached {
					st = "from an earlier download"
				}
				fmt.Fprintf(w, "%d modules (%s)\nfolder: %s\n", res.Files, st, res.Dir)
				if res.Schema != nil {
					fmt.Fprintf(w, "loaded: %d modules, %d nodes\n", len(res.Schema.Modules), res.Schema.Nodes)
				}
				for _, s := range res.Skipped {
					fmt.Fprintf(w, "skipped: %s\n", s)
				}
			})
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().IntVar(&req.Port, "netconf-port", 830, "NETCONF port on the device")
	cmd.Flags().StringVar(&req.Host, "netconf-host", "", "NETCONF host, when not the gNMI target's host")
	cmd.Flags().BoolVar(&req.Refresh, "refresh", false, "download again instead of using the earlier download")
	return cmd
}
