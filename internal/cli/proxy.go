package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/proxy"
	"github.com/UniverseDevel/Canopy/internal/shadow"
	"github.com/spf13/cobra"
)

func newProxyCmd(g *globals) *cobra.Command {
	var listen, upstream, certFile, keyFile, upCA, upName, record, httpAddr, nxListen, nxUp string
	var selfTLS, upInsecure, upSkip bool
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run a transparent, recording gRPC proxy in front of a device",
		Long: `Forwards every gRPC call (gNMI, gNOI, ...) to --upstream and records them;
gNMI messages are decoded. Use it to see exactly what a client (for example
network-operator) sends to a device. Records go to --record (JSON lines) and to the
HTTP API (--http): GET /v1/records[?method=Set], DELETE /v1/records.

TLS: --tls serves a fresh self-signed certificate; --tls-cert/--tls-key serve your
own (for a client that verifies, it must be signed by a CA the client trusts).
Upstream: --upstream-insecure (plaintext), --upstream-skip-verify, or
--upstream-ca/--upstream-server-name.

NX-API: the operator's NX-OS provider also sends HTTPS requests to the device
address on port 443; --nxapi-listen proxies those to --nxapi-upstream.

Put it between the operator and a device with 'canopy operator insert-proxy'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if upstream == "" {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--upstream is required")
			}
			host, _, _ := net.SplitHostPort(listen)
			var serverTLS *tls.Config
			switch {
			case certFile != "":
				cert, err := tls.LoadX509KeyPair(certFile, keyFile)
				if err != nil {
					return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
				}
				serverTLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
			case selfTLS:
				tc, _, err := shadow.SelfSigned([]string{host, "localhost", "127.0.0.1"})
				if err != nil {
					return apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
				}
				serverTLS = tc
			}
			var upTLS *tls.Config
			if !upInsecure {
				upTLS = &tls.Config{InsecureSkipVerify: upSkip, ServerName: upName, MinVersion: tls.VersionTLS12} //nolint:gosec // explicit flag
				if upCA != "" {
					pem, err := os.ReadFile(upCA)
					if err != nil {
						return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
					}
					pool := x509.NewCertPool()
					pool.AppendCertsFromPEM(pem)
					upTLS.RootCAs = pool
				}
				if upName == "" && !upSkip {
					upTLS.ServerName, _, _ = net.SplitHostPort(upstream)
				}
			}
			var mu sync.Mutex
			var out *os.File
			if record != "" {
				f, err := os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
				}
				defer f.Close()
				out = f
			}
			verbose := g.output != "json"
			p, err := proxy.New(proxy.Options{Listen: listen, Upstream: upstream, TLS: serverTLS, UpstreamTLS: upTLS,
				OnRecord: func(r proxy.Record) {
					mu.Lock()
					defer mu.Unlock()
					if out != nil {
						b, _ := json.Marshal(r)
						out.Write(append(b, '\n'))
					}
					if verbose {
						status := ""
						if r.Error != "" {
							status = "  ERROR " + r.Error
						}
						fmt.Fprintf(g.stderr, "%s #%d %-9s %-30s %6d B%s\n", r.Time.Format("15:04:05.000"), r.Call, r.Direction, r.Method, r.Size, status)
					}
				}})
			if err != nil {
				return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
			}
			c, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			addr, err := p.Serve(c)
			if err != nil {
				return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
			}
			info := map[string]any{"listen": addr, "upstream": upstream}
			if httpAddr != "" {
				mux := http.NewServeMux()
				mux.HandleFunc("GET /v1/records", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					enc := json.NewEncoder(w)
					enc.SetIndent("", "  ")
					enc.Encode(p.Records(r.URL.Query().Get("method")))
				})
				mux.HandleFunc("DELETE /v1/records", func(w http.ResponseWriter, _ *http.Request) { p.Clear(); w.WriteHeader(http.StatusNoContent) })
				ln, err := net.Listen("tcp", httpAddr)
				if err != nil {
					return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
				}
				srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
				go func() { <-c.Done(); srv.Close() }()
				go srv.Serve(ln)
				info["http"] = "http://" + ln.Addr().String() + "/v1/records"
			}
			if nxListen != "" {
				if nxUp == "" {
					uh, _, _ := net.SplitHostPort(upstream)
					nxUp = net.JoinHostPort(uh, "443")
				}
				srvTLS := serverTLS
				if srvTLS == nil {
					tc, _, err := shadow.SelfSigned([]string{host, "localhost", "127.0.0.1"})
					if err != nil {
						return apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
					}
					srvTLS = tc
				}
				na, err := p.ReverseHTTPS(c, nxListen, nxUp, srvTLS, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // lab proxy
				if err != nil {
					return apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err)
				}
				info["nxapi"] = na + " -> " + nxUp
			}
			g.result(cmd, info, func(w io.Writer) {
				mode := "plaintext"
				if serverTLS != nil {
					mode = "TLS"
				}
				fmt.Fprintf(w, "proxy on %s (%s) -> %s\n", addr, mode, upstream)
				for _, k := range []string{"http", "nxapi"} {
					if v, ok := info[k]; ok {
						fmt.Fprintf(w, "%s: %v\n", k, v)
					}
				}
				fmt.Fprintln(w, "Press Ctrl+C to stop.")
			})
			<-c.Done()
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&listen, "listen", "127.0.0.1:57600", "listen address")
	fl.StringVar(&upstream, "upstream", "", "device address host:port")
	fl.BoolVar(&selfTLS, "tls", false, "serve TLS with a self-signed certificate")
	fl.StringVar(&certFile, "tls-cert", "", "serve TLS with this certificate")
	fl.StringVar(&keyFile, "tls-key", "", "key for --tls-cert")
	fl.BoolVar(&upInsecure, "upstream-insecure", false, "plaintext to the upstream")
	fl.BoolVar(&upSkip, "upstream-skip-verify", false, "TLS to the upstream without verification")
	fl.StringVar(&upCA, "upstream-ca", "", "CA to verify the upstream")
	fl.StringVar(&upName, "upstream-server-name", "", "server name expected in the upstream certificate")
	fl.StringVar(&record, "record", "", "append records to this file (JSON lines)")
	fl.StringVar(&httpAddr, "http", "127.0.0.1:57680", "records API listen address (empty: off)")
	fl.StringVar(&nxListen, "nxapi-listen", "", "also proxy NX-API (HTTPS) on this address, e.g. :443")
	fl.StringVar(&nxUp, "nxapi-upstream", "", "NX-API upstream host:port (default: the upstream host on 443)")
	return cmd
}

func newOpProxyCmds(g *globals) []*cobra.Command {
	var f opFlags
	var addr string
	var dropTLS bool
	ins := &cobra.Command{
		Use: "insert-proxy DEVICE --address IP:PORT", Short: "Point a Device at a proxy (the original endpoint is kept)", Args: cobra.ExactArgs(1),
		Long: `Rewrites the Device's spec.endpoint.address to the proxy and keeps the original
endpoint in the annotation canopy.ironcore.dev/original-endpoint. The address must
be IPv4:port and reachable from the operator. If the Device verifies TLS with a CA,
the proxy must serve a certificate from that CA, or use --drop-tls to remove
spec.endpoint.tls while proxied (restored by remove-proxy).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if addr == "" {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--address is required")
			}
			cl, err := g.service().Operator(f.o)
			if err != nil {
				return err
			}
			orig, err := cl.InsertProxy(ctx(cmd), f.ns, args[0], addr, dropTLS)
			if err != nil {
				return err
			}
			return g.result(cmd, map[string]any{"device": args[0], "address": addr, "original": json.RawMessage(orig)}, func(w io.Writer) {
				fmt.Fprintf(w, "%s/%s now points at %s (original endpoint saved: %s)\n", f.ns, args[0], addr, orig)
			})
		},
	}
	addOpFlags(ins, &f, false)
	ins.Flags().StringVar(&addr, "address", "", "proxy address IPv4:port, reachable from the operator")
	ins.Flags().BoolVar(&dropTLS, "drop-tls", false, "remove spec.endpoint.tls while proxied")
	var rf opFlags
	rm := &cobra.Command{
		Use: "remove-proxy DEVICE", Short: "Restore a Device's original endpoint", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := g.service().Operator(rf.o)
			if err != nil {
				return err
			}
			orig, err := cl.RemoveProxy(ctx(cmd), rf.ns, args[0])
			if err != nil {
				return err
			}
			return g.result(cmd, map[string]any{"device": args[0], "restored": json.RawMessage(orig)}, func(w io.Writer) {
				fmt.Fprintf(w, "%s/%s endpoint restored: %s\n", rf.ns, args[0], orig)
			})
		},
	}
	addOpFlags(rm, &rf, false)
	return []*cobra.Command{ins, rm}
}
