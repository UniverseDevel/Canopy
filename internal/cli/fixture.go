package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/fixtures"
	"github.com/spf13/cobra"
)

func newFixtureCmd(g *globals) *cobra.Command {
	c := &cobra.Command{Use: "fixture", Short: "network-operator test fixtures (.txtar)"}
	var cr, preload, expect, del, title, out string
	export := &cobra.Command{
		Use:   "export",
		Short: "Write a fixture from a resource YAML and snapshots of a real device",
		Long: `Builds a fixture in network-operator's test/gnmi/testdata layout: the resource,
and state/preload, state/expect and state/delete from snapshots (taken with
'canopy snapshot take' before applying, after applying, and after deleting the
resource). Snapshots are names or files. Paths in the device data keep the module
prefixes the device sent; the provider's fake server compares the same way.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cr == "" || expect == "" {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--cr and --expect are required")
			}
			crYAML, err := os.ReadFile(cr)
			if err != nil {
				return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
			}
			st := g.service().Snapshots()
			load := func(name string) (any, error) {
				if name == "" {
					return nil, nil
				}
				sn, err := st.Load(name)
				if err != nil {
					return nil, err
				}
				return sn.Data, nil
			}
			var p, e, d any
			if p, err = load(preload); err != nil {
				return err
			}
			if e, err = load(expect); err != nil {
				return err
			}
			if d, err = load(del); err != nil {
				return err
			}
			if title == "" {
				title = "Exported by Canopy"
			}
			b, err := fixtures.Export(title, crYAML, p, e, d)
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err = g.stdout.Write(b)
				return err
			}
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
			}
			return g.result(cmd, map[string]any{"file": out, "bytes": len(b)}, func(w io.Writer) { fmt.Fprintf(w, "wrote %s (%d bytes)\n", out, len(b)) })
		},
	}
	export.Flags().StringVar(&cr, "cr", "", "resource YAML file")
	export.Flags().StringVar(&preload, "preload", "", "snapshot before the resource was applied")
	export.Flags().StringVar(&expect, "expect", "", "snapshot after the resource was applied")
	export.Flags().StringVar(&del, "delete", "", "snapshot after the resource was deleted")
	export.Flags().StringVar(&title, "title", "", "fixture title (first line)")
	export.Flags().StringVarP(&out, "out", "o", "", "output file (default: stdout)")
	c.AddCommand(export)
	return c
}
