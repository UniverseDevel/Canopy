package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/app"
	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/connections"
	"github.com/UniverseDevel/Canopy/internal/format"
	"github.com/UniverseDevel/Canopy/internal/secrets"
	"github.com/spf13/cobra"
	"os"
)

func (g *globals) presets() *connections.Store { return connections.Open("") }

func newConnCmd(g *globals) *cobra.Command {
	c := &cobra.Command{
		Use:   "conn",
		Short: "Manage saved connections (presets)",
		Long: `Saved connections keep target, TLS, jump host and working settings (YANG
folders, favourite paths, encoding) under a name, for use with --conn and in the UI.
Passwords aren't saved with the connection. A connection can name the
environment variable that holds its password (--password-env, default
GNMI_PASSWORD), or keep it, opt-in, in the OS keychain or in an encrypted file
next to its connections file (<name>.secrets.yaml, git-ignored, key in the
keychain): see 'canopy conn password'.

Your presets live in the user config folder, or in the file(s) given with
--connections or CANOPY_CONNECTIONS (separated like PATH): new presets go to the
first one, and every preset is edited and deleted in the file it came from.
'canopy conn path' prints the files.`,
	}
	c.AddCommand(newConnListCmd(g), newConnShowCmd(g), newConnSaveCmd(g), newConnRmCmd(g), newConnPathCmd(g), newConnPasswordCmd(g), &cobra.Command{
		Use: "forget-password NAME", Short: "Remove a stored password (keychain and encrypted file)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := ""
			if p, err := g.presets().Get(args[0]); err == nil {
				src = p.Source
			}
			return secrets.Forget(args[0], src)
		},
	})
	return c
}

func newConnListCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved connections",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := g.presets().List()
			if err != nil {
				return err
			}
			return g.result(cmd, list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintln(w, "No saved connections. Save one with 'canopy conn save NAME -a host:port ...'.")
					return
				}
				for _, c := range list {
					via := ""
					if len(c.Jumps) > 0 {
						via = " via " + strings.Join(c.Jumps, ",")
					}
					fmt.Fprintf(w, "%-24s %s%s\n", c.Name, c.Address, via)
					if c.Description != "" {
						fmt.Fprintf(w, "%-24s %s\n", "", c.Description)
					}
				}
			})
		},
	}
}

func newConnShowCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show a saved connection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.presets().Get(args[0])
			if err != nil {
				return err
			}
			return g.result(cmd, c, func(w io.Writer) {
				s, _ := format.YAML(c)
				if c.Source != "" {
					fmt.Fprintf(w, "# from %s\n", c.Source)
				}
				fmt.Fprint(w, s)
			})
		},
	}
}

func newConnSaveCmd(g *globals) *cobra.Command {
	var f targetFlags
	var desc, enc, pwEnv, modelSet string
	var remember bool
	var pwStore string
	var tags, yangDirs, paths []string
	cmd := &cobra.Command{
		Use:   "save NAME",
		Short: "Save (or update) a connection",
		Long: `Save a connection from the target flags. When NAME exists, it is updated:
flags given now replace its values, others are kept.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := g.presets()
			name := args[0]
			var base *connections.Connection
			if old, err := store.Get(name); err == nil {
				base = old
				f.preset = name // start from the existing values
			}
			t, err := g.target(&f, false)
			if err != nil {
				return err
			}
			c := app.PresetFrom(name, t)
			// Only an explicit --username is saved, never $GNMI_USERNAME.
			c.Username = ""
			if f.fs.Changed("username") {
				c.Username = f.username
			} else if base != nil {
				c.Username = base.Username
			}
			if base != nil {
				c.Description, c.Tags, c.Encoding, c.PasswordEnv = base.Description, base.Tags, base.Encoding, base.PasswordEnv
				c.YangDirs, c.Paths = base.YangDirs, base.Paths
				c.Models = base.Models
			}
			fl := cmd.Flags()
			if fl.Changed("description") {
				c.Description = desc
			}
			if fl.Changed("tag") {
				c.Tags = tags
			}
			if fl.Changed("encoding") {
				c.Encoding = enc
			}
			if fl.Changed("password-env") {
				c.PasswordEnv = pwEnv
			}
			if fl.Changed("yang") {
				c.YangDirs = yangDirs
			}
			if fl.Changed("models") {
				c.Models = modelSet
			}
			if fl.Changed("path") {
				c.Paths = paths
			}
			if err := store.Put(c); err != nil {
				return err
			}
			saved, err := store.Get(name)
			if err != nil {
				return err
			}
			if remember {
				pw := os.Getenv("GNMI_PASSWORD")
				if pw == "" && g.interactive() {
					pw, _ = g.prompt("Password to remember: ", true)
				}
				if pw == "" {
					return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "--remember-password needs GNMI_PASSWORD or a prompt")
				}
				if _, err := secrets.Remember(pwStore, saved.Name, saved.Source, pw); err != nil {
					return err
				}
			}
			return g.result(cmd, saved, func(w io.Writer) {
				fmt.Fprintf(w, "Saved connection %q to %s\n", saved.Name, saved.Source)
			})
		},
	}
	addTargetFlags(cmd, &f)
	cmd.Flags().Lookup("conn").Hidden = true
	cmd.Flags().StringVar(&desc, "description", "", "free text, for example the device role or owner")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "tags, for example lab, nxos, site-a")
	cmd.Flags().StringVarP(&enc, "encoding", "e", "", "preferred encoding")
	cmd.Flags().StringVar(&pwEnv, "password-env", "", "environment variable holding this device's password (default GNMI_PASSWORD)")
	cmd.Flags().StringSliceVarP(&yangDirs, "yang", "y", nil, "YANG folders for this device")
	cmd.Flags().StringVar(&modelSet, "models", "", "model set from GitHub, SOURCE@VERSION (default: detect from the device)")
	cmd.Flags().BoolVar(&remember, "remember-password", false, "store the password (GNMI_PASSWORD or prompt), see --password-store")
	cmd.Flags().StringVar(&pwStore, "password-store", "keychain", "where --remember-password keeps it: keychain, or file (encrypted, next to the connections file, git-ignored)")
	cmd.Flags().StringSliceVarP(&paths, "path", "p", nil, "favourite paths (used by 'get' when no --path is given)")
	return cmd
}

func newConnRmCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Delete a saved connection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := g.presets().Delete(args[0]); err != nil {
				return err
			}
			return g.result(cmd, map[string]string{"deleted": args[0]}, func(w io.Writer) {
				fmt.Fprintf(w, "Deleted %q\n", args[0])
			})
		},
	}
}

func newConnPathCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print where saved connections are stored",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := g.presets()
			return g.result(cmd, map[string]any{"file": s.Path, "more": s.Extra}, func(w io.Writer) {
				fmt.Fprintln(w, s.Path)
				for _, x := range s.Extra {
					fmt.Fprintln(w, x)
				}
			})
		},
	}
}

func newConnPasswordCmd(g *globals) *cobra.Command {
	var store string
	cmd := &cobra.Command{
		Use:   "password NAME",
		Short: "Remember a saved connection's password (keychain or encrypted file)",
		Long: `Stores the password for a saved connection, from GNMI_PASSWORD or a prompt.

--store keychain (default) keeps it in the OS keychain. --store file keeps it in
an encrypted file next to the connection's file (connections.yaml ->
connections.secrets.yaml): AES-256-GCM with a key that lives in this
computer's OS keychain, so the file is useless elsewhere. Canopy makes sure git
ignores the file (it adds *.secrets.yaml to a .gitignore when needed).
Storing it in one place removes it from the other.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.presets().Get(args[0])
			if err != nil {
				return err
			}
			pw := os.Getenv("GNMI_PASSWORD")
			if p.PasswordEnv != "" && os.Getenv(p.PasswordEnv) != "" {
				pw = os.Getenv(p.PasswordEnv)
			}
			if pw == "" && g.interactive() {
				pw, _ = g.prompt("Password for "+p.Name+": ", true)
			}
			if pw == "" {
				return apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no password: set GNMI_PASSWORD or run in a terminal")
			}
			msg, err := secrets.Remember(store, p.Name, p.Source, pw)
			if err != nil {
				return err
			}
			return g.result(cmd, map[string]string{"name": p.Name, "store": store, "message": msg}, func(w io.Writer) {
				fmt.Fprintf(w, "%s: password %s\n", p.Name, msg)
			})
		},
	}
	cmd.Flags().StringVar(&store, "store", "keychain", "keychain, or file (encrypted, git-ignored)")
	return cmd
}
