package cli

import (
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/schema"
	"github.com/UniverseDevel/Canopy/internal/web"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

func newUICmd(g *globals) *cobra.Command {
	var listen string
	var noBrowser bool
	var yangDirs []string
	var op app.OperatorOptions
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Start the web UI (listens on 127.0.0.1 only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc := g.service()
			defer svc.Close()
			if len(yangDirs) > 0 {
				if _, err := svc.LoadSchema(yangDirs, schema.LoadOptions{}); err != nil {
					return err
				}
			}
			srv, err := web.New(svc, g.log, listen)
			if err == nil {
				srv.OpDefaults = op
			}
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(ctx(cmd), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			url := srv.URL()
			g.result(cmd, map[string]string{"url": url}, func(w io.Writer) {
				fmt.Fprintf(w, "Canopy UI: %s\nPress Ctrl+C to stop.\n", url)
			})
			if !noBrowser {
				browser.Stdout, browser.Stderr = io.Discard, io.Discard
				if err := browser.OpenURL(url); err != nil {
					fmt.Fprintln(g.stderr, "could not open a browser; open the URL above")
				}
			}
			return srv.Serve(ctx)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "listen address (loopback only; port 0 = random)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "don't open a browser")
	cmd.Flags().StringSliceVarP(&yangDirs, "yang", "y", nil, "load YANG models from these folders at start")
	cmd.Flags().StringVar(&op.Kubeconfig, "kubeconfig", "", "kubeconfig for operator mode (default $KUBECONFIG or ~/.kube/config)")
	cmd.Flags().StringVar(&op.Context, "kube-context", "", "kube context for operator mode (default: current)")
	cmd.Flags().StringSliceVar(&op.Jumps, "operator-jump", nil, "SSH jump host(s) to reach devices managed by the operator")
	cmd.Flags().StringSliceVar(&op.IdentityFiles, "operator-identity", nil, "SSH key file(s) for --operator-jump")
	cmd.Flags().StringVar(&op.OperatorRepo, "operator-repo", "", "network-operator checkout for QA fixtures (default $CANOPY_OPERATOR_REPO or ../network-operator)")
	return cmd
}
