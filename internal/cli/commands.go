package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/format"
	"github.com/UniverseDevel/Canopy/internal/generate"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/spf13/cobra"
)

func newGetCmd(g *globals) *cobra.Command {
	var f targetFlags
	var paths []string
	var typ, enc, fmtName, prefix string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get data from a target",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch fmtName {
			case "json", "yaml", "flat", "notifications":
			default:
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--format must be json, yaml, flat or notifications")
			}
			if len(paths) == 0 && f.preset != "" {
				if p, err := g.presets().Get(f.preset); err == nil {
					paths = p.Paths // the connection's favourite paths
				}
			}
			if len(paths) == 0 {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--path is required")
			}
			if _, err := live.BuildGetRequest(live.GetRequest{Paths: paths, Type: typ, Encoding: enc, Prefix: prefix}); err != nil {
				return err // validate before connecting
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
			res, err := svc.Get(ctx(cmd), sess.ID, live.GetRequest{Paths: paths, Type: typ, Encoding: enc, Prefix: prefix})
			if err != nil {
				return err
			}
			return g.result(cmd, res, func(w io.Writer) {
				var s string
				switch fmtName {
				case "json":
					s, _ = format.JSON(res.Data)
				case "yaml":
					s, _ = format.YAML(res.Data)
				case "notifications":
					s, _ = format.JSON(res.Notifications)
				case "flat":
					lines := make([]format.FlatLine, 0, len(res.Flat))
					for _, u := range res.Flat {
						lines = append(lines, format.FlatLine{Path: u.Path, Value: u.Value})
					}
					s = format.Flat(lines)
				}
				if res.Empty {
					fmt.Fprintln(g.stderr, "(no data)")
				}
				fmt.Fprint(w, s)
			})
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().StringSliceVarP(&paths, "path", "p", nil, "path(s) to read, e.g. /system/information or 'openconfig:/interfaces'")
	cmd.Flags().StringVar(&prefix, "prefix", "", "common path prefix")
	cmd.Flags().StringVarP(&typ, "type", "t", "all", "data type: all, config, state, operational")
	cmd.Flags().StringVarP(&enc, "encoding", "e", "", "encoding (default: json_ietf or json, from capabilities)")
	cmd.Flags().StringVarP(&fmtName, "format", "f", "json", "text output: json, yaml, flat, notifications")
	return cmd
}

func newTreeCmd(g *globals) *cobra.Command {
	var dirs, modules []string
	var modelSpec string
	var search []string
	var path, find string
	var depth, limit int
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Load YANG models from folders and print the schema tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if modelSpec != "" {
				d, err := g.resolveModels(cmd, modelSpec)
				if err != nil {
					return err
				}
				dirs = append(dirs, d.Dirs...)
				search = d.SearchDirs
			}
			if len(dirs) == 0 {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--yang or --models is required")
			}
			svc := g.service()
			tr, err := svc.LoadSchema(dirs, schema.LoadOptions{Modules: modules, SearchDirs: search})
			if err != nil {
				return err
			}
			if find != "" {
				res := tr.Search(find, limit)
				return g.result(cmd, map[string]any{"summary": tr.Summary(), "query": find, "matches": res}, func(w io.Writer) {
					for _, r := range res {
						fmt.Fprintf(w, "%-10s %-30s %s\n", r.Kind, r.Module, r.Path)
					}
					fmt.Fprintf(g.stderr, "%d matches\n", len(res))
				})
			}
			start := -1
			if path != "" {
				n := tr.FindPath(path)
				if n == nil {
					return apperr.New(apperr.ClassSchema, apperr.CodeNotFound, "path %s not found in the loaded models", path)
				}
				start = n.ID
			}
			type jnode struct {
				*schema.Node
				Children []*jnode `json:"children,omitempty"`
			}
			var build func(id, d int) []*jnode
			build = func(id, d int) []*jnode {
				var out []*jnode
				for _, c := range tr.Children(id) {
					jn := &jnode{Node: c}
					if depth == 0 || d < depth {
						jn.Children = build(c.ID, d+1)
					}
					out = append(out, jn)
				}
				return out
			}
			var nodes []*jnode
			if start >= 0 {
				nodes = []*jnode{{Node: tr.Node(start), Children: build(start, 1)}}
			} else {
				nodes = build(-1, 1)
			}
			return g.result(cmd, map[string]any{"summary": tr.Summary(), "nodes": nodes}, func(w io.Writer) {
				s := tr.Summary()
				fmt.Fprintf(w, "# %d files, %d modules, %d nodes, %d ms\n", s.Files, len(s.Modules), s.Nodes, s.LoadMs)
				for _, wn := range s.Warnings {
					fmt.Fprintf(g.stderr, "warning: %s\n", wn)
				}
				var pr func(ns []*jnode, indent string)
				pr = func(ns []*jnode, indent string) {
					for _, n := range ns {
						line := treeLine(n.Node)
						if n.Parent < 0 {
							line += "    (" + n.Module + ")"
						}
						fmt.Fprintf(w, "%s%s\n", indent, line)
						pr(n.Children, indent+"   ")
					}
				}
				pr(nodes, "")
			})
		},
	}
	cmd.Flags().StringSliceVarP(&dirs, "yang", "y", nil, "folder(s) with .yang files (searched recursively)")
	cmd.Flags().StringVar(&modelSpec, "models", "", "model set from GitHub, SOURCE@VERSION (e.g. srlinux@v26.7.1, nxos@10.4-3); downloaded once and cached")
	cmd.Flags().StringSliceVarP(&modules, "module", "m", nil, "only show the tree of these modules (imports are still loaded)")
	cmd.Flags().StringVarP(&path, "path", "p", "", "print only the subtree at this data path")
	cmd.Flags().StringVar(&find, "find", "", "search node names (or paths, if the query contains '/')")
	cmd.Flags().IntVarP(&depth, "depth", "d", 0, "maximum depth to print (0 = all)")
	cmd.Flags().IntVar(&limit, "limit", 500, "maximum number of search results")
	return cmd
}

// treeLine renders a node pyang-style: "+--rw name?  type".
func treeLine(n *schema.Node) string {
	rw := "rw"
	if !n.Config {
		rw = "ro"
	}
	name := n.Name
	switch n.Kind {
	case "list":
		name += "* [" + strings.Join(n.Keys, " ") + "]"
	case "leaf-list":
		name += "*"
	case "leaf":
		if !n.Mandatory {
			name += "?"
		}
	case "container":
		if n.Presence {
			name += "!"
		}
	}
	s := "+--" + rw + " " + name
	if n.Type != nil {
		s = fmt.Sprintf("%-45s %s", s, n.Type.Name)
	}
	return s
}

func newGenerateCmd(g *globals) *cobra.Command {
	gen := &cobra.Command{Use: "generate", Short: "Generate commands for other tools"}
	var f targetFlags
	var r generate.Request
	cmd := &cobra.Command{
		Use:   "gnmic",
		Short: "Generate gNMIc commands (no connection is made)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := g.target(&f, false)
			if err != nil {
				return err
			}
			if f.preset != "" {
				if p, err := g.presets().Get(f.preset); err == nil {
					if len(r.Paths) == 0 {
						r.Paths = p.Paths
					}
					if r.Encoding == "" {
						r.Encoding = p.Encoding
					}
				}
			}
			out, err := g.service().Generate("", &t, r)
			if err != nil {
				return err
			}
			return g.result(cmd, out, func(w io.Writer) {
				for _, c := range out.Commands {
					fmt.Fprintf(w, "# %s\n", c.Title)
					if c.Note != "" {
						fmt.Fprintf(w, "# %s\n", c.Note)
					}
					fmt.Fprintln(w, c.Text)
					fmt.Fprintln(w)
				}
			})
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().StringSliceVarP(&r.Paths, "path", "p", nil, "path(s)")
	cmd.Flags().StringVar(&r.Mode, "mode", "get", "get, once, stream, poll or capabilities")
	cmd.Flags().StringVarP(&r.Type, "type", "t", "", "get data type: all, config, state, operational")
	cmd.Flags().StringVarP(&r.Encoding, "encoding", "e", "", "encoding")
	cmd.Flags().StringVar(&r.StreamMode, "stream-mode", "", "stream mode: sample, on-change, target-defined")
	cmd.Flags().StringVar(&r.SampleInterval, "sample-interval", "", "sample interval (default 10s)")
	cmd.Flags().StringVar(&r.Shell, "shell", "bash", "shell syntax: bash or powershell")
	gen.AddCommand(cmd)
	return gen
}
