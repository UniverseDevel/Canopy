package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/live"
	"github.com/UniverseDevel/Canopy/internal/shadow"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/spf13/cobra"
)

func newShadowCmd(g *globals) *cobra.Command {
	var f targetFlags
	var listen, httpAddr, load, snap string
	var seed []string
	var auth bool
	cmd := &cobra.Command{
		Use:   "shadow",
		Short: "Run an in-memory gNMI target (shadow of a device) for tests and dry runs",
		Long: `Runs a plaintext gNMI target that keeps its state in memory and supports
Capabilities, Get, Set (update, replace, delete) and Subscribe ONCE.

Seed it from a real device to rehearse changes without touching the device:
  --conn/--target  copy the device's Capabilities (so version-dependent clients,
                   such as network-operator's NX-OS provider, see the real release)
  --seed PATH      also copy the device's data at PATH (repeatable)
  --snapshot NAME  load the data of a saved snapshot
  --load FILE      load a JSON tree (RFC 7951)

The HTTP control API (--http) reads and replaces the state and lists received
Sets: GET/PUT/DELETE /v1/state, GET /v1/leaves, GET /v1/sets.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sh := shadow.New()
			if auth {
				sh.Username, sh.Password = os.Getenv("GNMI_USERNAME"), os.Getenv("GNMI_PASSWORD")
				if sh.Username == "" {
					return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--auth needs GNMI_USERNAME and GNMI_PASSWORD")
				}
			}
			if f.preset != "" || f.address != "" {
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
				caps := &gpb.CapabilityResponse{GNMIVersion: sess.Capabilities.GNMIVersion}
				for _, m := range sess.Capabilities.Models {
					caps.SupportedModels = append(caps.SupportedModels, &gpb.ModelData{Name: m.Name, Organization: m.Organization, Version: m.Version})
				}
				caps.SupportedEncodings = []gpb.Encoding{gpb.Encoding_JSON_IETF, gpb.Encoding_JSON}
				sh.SetCapabilities(caps)
				if len(seed) > 0 {
					res, err := svc.Get(ctx(cmd), sess.ID, live.GetRequest{Paths: seed})
					if err != nil {
						return err
					}
					b, _ := json.Marshal(res.Data)
					if err := sh.Load(b); err != nil {
						return apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
					}
				}
			}
			if snap != "" {
				sn, err := g.service().Snapshots().Load(snap)
				if err != nil {
					return err
				}
				b, _ := json.Marshal(sn.Data)
				if err := sh.Load(b); err != nil {
					return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
				}
			}
			if load != "" {
				b, err := os.ReadFile(load)
				if err != nil {
					return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
				}
				if err := sh.Load(b); err != nil {
					return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "%s: %v", load, err)
				}
			}
			c, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			addr, err := sh.Serve(c, listen)
			if err != nil {
				return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
			}
			info := map[string]any{"gnmi": addr, "leaves": len(sh.Leaves())}
			if httpAddr != "" {
				ha, err := sh.ServeControl(c, httpAddr)
				if err != nil {
					return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
				}
				info["http"] = "http://" + ha
			}
			g.result(cmd, info, func(w io.Writer) {
				fmt.Fprintf(w, "shadow gNMI target on %s (plaintext), %d leaves\n", addr, len(sh.Leaves()))
				if h, ok := info["http"]; ok {
					fmt.Fprintf(w, "control API: %s/v1/state\n", h)
				}
				fmt.Fprintln(w, "Press Ctrl+C to stop.")
			})
			<-c.Done()
			return nil
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:57500", "gNMI listen address")
	cmd.Flags().StringVar(&httpAddr, "http", "127.0.0.1:57580", "control API listen address (empty: off)")
	cmd.Flags().StringVar(&load, "load", "", "JSON file to load as state")
	cmd.Flags().StringVar(&snap, "snapshot", "", "snapshot to load as state")
	cmd.Flags().StringSliceVar(&seed, "seed", nil, "copy the data (config and state) at these paths from the --conn/--target device")
	cmd.Flags().BoolVar(&auth, "auth", false, "require GNMI_USERNAME/GNMI_PASSWORD from clients")
	return cmd
}
