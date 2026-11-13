package cli

import (
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/tui"
)

func newTUICmd(g *globals) *cobra.Command {
	var f targetFlags
	var dirs []string
	var spec, shell string
	var plain bool
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Browse the schema and read data in the terminal",
		Long: `A terminal front end for places without a browser, such as an SSH session on
a jump host. With a target (-a or --conn) it connects, loads the matching models
(detected, or --models/--yang), and reads data; without one it browses the
models given with --models or --yang.

Keys: / search, space mark a path, g read the node, G read the marked paths,
Enter expand (or read a leaf), d details, c capabilities, y YAML/JSON,
p qualified/plain paths, x clear marks, q quit. The bottom line is the gNMIc
command for the marked paths (or the current node).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc := g.service()
			defer svc.Close()
			opt := tui.Options{Shell: shell, Qualified: !plain}
			if opt.Shell == "" {
				opt.Shell = "bash"
				if runtime.GOOS == "windows" {
					opt.Shell = "powershell"
				}
			}
			connected := f.address != "" || f.preset != ""
			if connected {
				t, err := g.target(&f, true)
				if err != nil {
					return err
				}
				sess, err := g.connect(ctx(cmd), svc, t)
				if err != nil {
					return err
				}
				opt.Session = sess
			}
			switch {
			case spec != "":
				src, ver, ok := strings.Cut(spec, "@")
				if !ok {
					return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--models must be SOURCE@VERSION")
				}
				if _, err := svc.LoadModels(ctx(cmd), src, ver, g.progress()); err != nil {
					return err
				}
				opt.Models = src + " " + ver
			case len(dirs) > 0:
				if _, err := svc.LoadSchema(dirs, schema.LoadOptions{}); err != nil {
					return err
				}
				opt.Models = strings.Join(dirs, ", ")
			case opt.Session != nil && len(opt.Session.YangDirs) > 0:
				if _, err := svc.LoadSchema(opt.Session.YangDirs, schema.LoadOptions{}); err != nil {
					return err
				}
				opt.Models = strings.Join(opt.Session.YangDirs, ", ")
			case opt.Session != nil:
				res, err := svc.AutoModels(ctx(cmd), opt.Session.ID, g.progress())
				if err != nil {
					return err
				}
				if res.Schema == nil {
					return apperr.New(apperr.ClassSchema, apperr.CodeSchema, "could not detect the models: %s", res.Detection.Reason).
						WithHint("pass --models SOURCE@VERSION or --yang")
				}
				opt.Models = res.Set.Source + " " + res.Set.Version
			default:
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "give a target (-a or --conn) or models (--models or --yang)")
			}
			return tui.Run(ctx(cmd), svc, opt)
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().StringSliceVarP(&dirs, "yang", "y", nil, "load YANG models from these folders")
	cmd.Flags().StringVar(&spec, "models", "", "load a model set, SOURCE@VERSION (e.g. srlinux@v26.7.1)")
	cmd.Flags().StringVar(&shell, "shell", "", "shell for the gNMIc command: bash or powershell (default: by OS)")
	cmd.Flags().BoolVar(&plain, "plain", false, "plain paths instead of module-qualified ones")
	return cmd
}
