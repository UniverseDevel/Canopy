package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/format"
	"github.com/spf13/cobra"
)

type opFlags struct {
	o  app.OperatorOptions
	ns string
}

func addOpFlags(cmd *cobra.Command, f *opFlags, withReach bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.o.Kubeconfig, "kubeconfig", "", "kubeconfig file (default $KUBECONFIG or ~/.kube/config)")
	fl.StringVar(&f.o.Context, "context", "", "kube context (default: current)")
	fl.StringVarP(&f.ns, "namespace", "n", "default", "namespace")
	if withReach {
		fl.StringSliceVarP(&f.o.Jumps, "jump", "J", nil, "SSH jump host(s) to reach the device addresses (often only routable from the cluster network)")
		fl.StringSliceVar(&f.o.IdentityFiles, "ssh-identity", nil, "SSH key file(s) for the jump hosts")
		fl.IntVar(&f.o.TimeoutSec, "timeout-sec", 10, "connection timeout in seconds")
		fl.StringVar(&f.o.OperatorRepo, "operator-repo", "", "network-operator checkout for fixtures (default $CANOPY_OPERATOR_REPO or ../network-operator)")
	}
}

func newOperatorCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:     "operator",
		Aliases: []string{"op"},
		Short:   "Operator mode: devices and resources managed by network-operator (QA)",
		Long: `Reads network-operator's Device and resource CRs from the current kube context,
connects to devices the way the operator does (address, basic-auth Secret, TLS
Secrets; add --jump when device addresses are only routable from the cluster), and
verifies that a resource's intent is on the device.`,
	}
	c.AddCommand(newOpDevicesCmd(g), newOpResourcesCmd(g), newOpConnectCmd(g), newOpPauseCmd(g, true), newOpPauseCmd(g, false), newOpVerifyCmd(g))
	c.AddCommand(newOpProxyCmds(g)...)
	return c
}

func newOpDevicesCmd(g *globals) *cobra.Command {
	var f opFlags
	var all bool
	cmd := &cobra.Command{
		Use: "devices", Short: "List Device CRs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := g.service().Operator(f.o)
			if err != nil {
				return err
			}
			ns := f.ns
			if all {
				ns = ""
			}
			ds, err := cl.Devices(ctx(cmd), ns)
			if err != nil {
				return err
			}
			return g.result(cmd, ds, func(w io.Writer) {
				for _, d := range ds {
					paused := ""
					if d.Paused {
						paused = " PAUSED"
					}
					fmt.Fprintf(w, "%-12s %-16s %-22s ready=%-5s %-10s %s%s\n", d.Namespace, d.Name, d.Address, d.Ready, d.Phase, d.Provider, paused)
				}
			})
		},
	}
	addOpFlags(cmd, &f, false)
	cmd.Flags().BoolVarP(&all, "all-namespaces", "A", false, "all namespaces")
	return cmd
}

func newOpResourcesCmd(g *globals) *cobra.Command {
	var f opFlags
	cmd := &cobra.Command{
		Use: "resources DEVICE", Short: "List the resources attached to a Device (spec.deviceRef)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := g.service().Operator(f.o)
			if err != nil {
				return err
			}
			rs, err := cl.Resources(ctx(cmd), f.ns, args[0])
			if err != nil {
				return err
			}
			return g.result(cmd, rs, func(w io.Writer) {
				for _, r := range rs {
					extra := ""
					if r.Paused {
						extra = " PAUSED"
					}
					if r.Observed != 0 && r.Observed != r.Generation {
						extra += fmt.Sprintf(" (gen %d, observed %d)", r.Generation, r.Observed)
					}
					fmt.Fprintf(w, "%-18s %-24s ready=%s%s\n", r.Kind, r.Name, r.Ready, extra)
				}
			})
		},
	}
	addOpFlags(cmd, &f, false)
	return cmd
}

func newOpConnectCmd(g *globals) *cobra.Command {
	var f opFlags
	cmd := &cobra.Command{
		Use: "connect DEVICE", Short: "Connect to a Device as the operator does and print its capabilities", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := g.service()
			defer svc.Close()
			sess, err := svc.ConnectDevice(ctx(cmd), f.o, f.ns, args[0], app.Target{})
			if err != nil {
				return err
			}
			return g.result(cmd, sess, func(w io.Writer) {
				fmt.Fprintf(w, "Connected to %s (%s) in %d ms", sess.Label, sess.Address, sess.ConnectMs)
				if len(sess.Hops) > 0 {
					fmt.Fprintf(w, " via %s", strings.Join(sess.Hops, " -> "))
				}
				fmt.Fprintf(w, "\ngNMI %s, %d models, encodings %s\n", sess.Capabilities.GNMIVersion, len(sess.Capabilities.Models), strings.Join(sess.Capabilities.Encodings, ", "))
			})
		},
	}
	addOpFlags(cmd, &f, true)
	return cmd
}

func newOpPauseCmd(g *globals, pause bool) *cobra.Command {
	var f opFlags
	var wait time.Duration
	use, short := "resume KIND NAME", "Resume reconciliation (remove the paused annotation)"
	if pause {
		use, short = "pause KIND NAME", "Pause reconciliation (paused annotation), e.g. before manual changes"
	}
	cmd := &cobra.Command{
		Use: use, Short: short, Args: cobra.ExactArgs(2),
		Long: short + `. KIND is a kind or plural (device, banner, interfaces, ...).
Pausing a Device pauses all its resources. With --wait, waits for the Paused
condition.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := g.service().Operator(f.o)
			if err != nil {
				return err
			}
			if err := cl.SetPaused(ctx(cmd), args[0], f.ns, args[1], pause); err != nil {
				return err
			}
			state := "annotation set"
			if !pause {
				state = "annotation removed"
			}
			if wait > 0 {
				got, err := cl.WaitPaused(ctx(cmd), args[0], f.ns, args[1], pause, wait)
				if err != nil {
					return err
				}
				state += ", Paused=" + got
			}
			return g.result(cmd, map[string]any{"kind": args[0], "name": args[1], "paused": pause, "state": state}, func(w io.Writer) {
				fmt.Fprintf(w, "%s %s/%s: %s\n", args[0], f.ns, args[1], state)
			})
		},
	}
	addOpFlags(cmd, &f, false)
	cmd.Flags().DurationVar(&wait, "wait", 0, "wait up to this long for the Paused condition (e.g. 30s)")
	return cmd
}

func newOpVerifyCmd(g *globals) *cobra.Command {
	var f opFlags
	var device, fixture string
	var deleted bool
	cmd := &cobra.Command{
		Use:   "verify KIND NAME --device DEVICE",
		Short: "QA: check that a resource's intent is on the device",
		Long: `Takes the provider's test fixture for KIND (network-operator test/gnmi/testdata),
maps the device leaves it writes onto this resource's spec, reads them from the
device and compares. With --deleted, checks that they are gone (after deleting the
resource). The resource's Ready, Paused and generation status count too.

Exit code 0 = pass, 3 = fail.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if device == "" {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--device is required")
			}
			svc := g.service()
			defer svc.Close()
			sess, err := svc.ConnectDevice(ctx(cmd), f.o, f.ns, device, app.Target{})
			if err != nil {
				return err
			}
			v, err := svc.Verify(ctx(cmd), f.o, sess.ID, args[0], f.ns, args[1], deleted, fixture, "")
			if err != nil {
				return err
			}
			if err := g.result(cmd, v, func(w io.Writer) {
				fmt.Fprintf(w, "%s %s/%s on %s (fixture %s, %s): %s\n", v.Resource.Kind, f.ns, args[1], v.Device, v.Fixture, v.Mode, strings.ToUpper(v.Verdict))
				route := "directly"
				if len(v.Jumps) > 0 {
					route = "via " + strings.Join(v.Jumps, ", ")
				}
				fmt.Fprintf(w, "  device   %s, %s (provider %s)\n", v.Address, route, v.Provider)
				fmt.Fprintf(w, "  fixture  %s\n", v.FixtureFile)
				for _, l := range v.Leaves {
					from := ""
					if l.From != "" {
						from = "  (from spec." + l.From + ")"
					}
					switch {
					case v.Mode == "deleted" && l.Status == "other":
						fmt.Fprintf(w, "  %-8s %s (the resource set %s; now %s)\n", l.Status, l.Path, format.Scalar(l.Expected), format.Scalar(l.Actual))
					case v.Mode == "deleted":
						fmt.Fprintf(w, "  %-8s %s\n", l.Status, l.Path)
					default:
						fmt.Fprintf(w, "  %-8s %s = %s (device: %s)%s\n", l.Status, l.Path, format.Scalar(l.Expected), format.Scalar(l.Actual), from)
					}
					if l.FixturePath != "" && l.FixturePath != l.Path {
						fmt.Fprintf(w, "  %-8s (in the fixture: %s)\n", "", l.FixturePath)
					}
				}
				for _, r := range v.Reasons {
					fmt.Fprintln(w, "  !", r)
				}
				if len(v.Alternates) > 0 {
					fmt.Fprintln(w, "  other fixtures for this kind:", strings.Join(v.Alternates, ", "))
				}
			}); err != nil {
				return err
			}
			if v.Verdict != "pass" {
				return apperr.New(apperr.ClassDevice, "verify_failed", "verification %s", v.Verdict)
			}
			return nil
		},
	}
	addOpFlags(cmd, &f, true)
	cmd.Flags().StringVar(&device, "device", "", "the Device the resource belongs to")
	cmd.Flags().StringVar(&fixture, "fixture", "", "fixture file to use when several match (e.g. banner.txtar)")
	cmd.Flags().BoolVar(&deleted, "deleted", false, "check that the resource's leaves are gone (after deleting it)")
	return cmd
}
