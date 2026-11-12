// Package generate produces gNMIc commands and related snippets from a target and
// a selection of paths. Secrets are never embedded: commands reference
// $GNMI_USERNAME and $GNMI_PASSWORD.
package generate

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/conn"
	"github.com/UniverseDevel/Canopy/internal/sshx"
)

// Request is what to generate.
type Request struct {
	Target   conn.Options `json:"target"`
	Hops     []sshx.Hop   `json:"-"`
	Paths    []string     `json:"paths"`
	Mode     string       `json:"mode"`               // get (default), once, stream, poll, capabilities
	Type     string       `json:"type,omitempty"`     // get: all, config, state, operational
	Encoding string       `json:"encoding,omitempty"` // empty: gNMIc default
	// Stream options
	StreamMode     string `json:"streamMode,omitempty"`     // sample (default), on-change, target-defined
	SampleInterval string `json:"sampleInterval,omitempty"` // default 10s
	Shell          string `json:"shell,omitempty"`          // bash (default) or powershell
	LocalPort      int    `json:"localPort,omitempty"`      // tunnel port, default 57400+
}

// Command is one generated command.
type Command struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Note  string `json:"note,omitempty"`
}

// Output is the generator result.
type Output struct {
	Shell    string    `json:"shell"`
	Commands []Command `json:"commands"`
	// Config is an equivalent gNMIc config file (YAML).
	Config string `json:"config,omitempty"`
}

// Gnmic generates the commands.
func Gnmic(r Request) (*Output, error) {
	mode := strings.ToLower(r.Mode)
	if mode == "" {
		mode = "get"
	}
	switch mode {
	case "get", "once", "stream", "poll", "capabilities":
	default:
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown mode %q (get, once, stream, poll, capabilities)", r.Mode)
	}
	if mode != "capabilities" && len(r.Paths) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "at least one path is required")
	}
	shell := strings.ToLower(r.Shell)
	if shell == "" {
		shell = "bash"
	}
	if shell != "bash" && shell != "powershell" {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown shell %q (bash, powershell)", r.Shell)
	}
	q := quoter(shell)
	out := &Output{Shell: shell}

	addr := r.Target.Address
	host, port, _ := net.SplitHostPort(addr)
	serverName := r.Target.ServerName
	if len(r.Hops) > 0 {
		lp := r.LocalPort
		if lp == 0 {
			lp, _ = strconv.Atoi(port)
			if lp < 1024 {
				lp += 50000
			}
		}
		var jumps []string
		for _, h := range r.Hops[:len(r.Hops)-1] {
			jumps = append(jumps, hopString(h))
		}
		last := r.Hops[len(r.Hops)-1]
		ssh := fmt.Sprintf("ssh -N -L %d:%s ", lp, net.JoinHostPort(host, port))
		if r.Target.SSH != nil {
			for _, f := range r.Target.SSH.IdentityFiles {
				ssh += "-i " + q(f) + " "
			}
		}
		if len(jumps) > 0 {
			ssh += "-J " + strings.Join(jumps, ",") + " "
		}
		// -J accepts user@host:port, but the destination needs -p.
		if last.Port != 0 && last.Port != 22 {
			ssh += "-p " + strconv.Itoa(last.Port) + " "
		}
		dest := last.Host
		if last.Alias != "" {
			dest = last.Alias
		}
		if last.User != "" {
			dest = last.User + "@" + dest
		}
		ssh += dest
		out.Commands = append(out.Commands, Command{
			Title: "Tunnel (keep running in its own terminal)",
			Text:  ssh,
			Note:  "forwards 127.0.0.1:" + strconv.Itoa(lp) + " to the target through the jump host(s)",
		})
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(lp))
		if !r.Target.Insecure && !r.Target.SkipVerify && serverName == "" {
			serverName = host // certificate is for the real name, not 127.0.0.1
		}
	}

	var b []string
	b = append(b, "gnmic", "-a", addr)
	b = append(b, "-u", envRef(shell, "GNMI_USERNAME"), "-p", envRef(shell, "GNMI_PASSWORD"))
	switch {
	case r.Target.Insecure:
		b = append(b, "--insecure")
	case r.Target.SkipVerify:
		b = append(b, "--skip-verify")
	}
	if r.Target.CAFile != "" {
		b = append(b, "--tls-ca", q(r.Target.CAFile))
	}
	if r.Target.CertFile != "" {
		b = append(b, "--tls-cert", q(r.Target.CertFile), "--tls-key", q(r.Target.KeyFile))
	}
	if serverName != "" && !r.Target.Insecure {
		b = append(b, "--tls-server-name", q(serverName))
	}
	if r.Target.Timeout > 0 {
		b = append(b, "--timeout", r.Target.Timeout.String())
	}
	if r.Encoding != "" && mode != "capabilities" {
		b = append(b, "-e", strings.ToLower(r.Encoding))
	}
	switch mode {
	case "capabilities":
		b = append(b, "capabilities")
	case "get":
		b = append(b, "get")
		if t := strings.ToLower(r.Type); t != "" && t != "all" {
			b = append(b, "--type", t)
		}
		for _, p := range r.Paths {
			b = append(b, "--path", q(p))
		}
	default:
		b = append(b, "subscribe")
		for _, p := range r.Paths {
			b = append(b, "--path", q(p))
		}
		b = append(b, "--mode", mode)
		if mode == "stream" {
			sm := r.StreamMode
			if sm == "" {
				sm = "sample"
			}
			b = append(b, "--stream-mode", sm)
			if sm == "sample" {
				si := r.SampleInterval
				if si == "" {
					si = "10s"
				}
				b = append(b, "--sample-interval", si)
			}
		}
	}
	out.Commands = append(out.Commands, Command{Title: "gNMIc " + mode, Text: wrap(shell, b)})
	out.Config = config(r, mode, addr, serverName)
	return out, nil
}

func hopString(h sshx.Hop) string {
	s := h.Host
	if h.Alias != "" {
		s = h.Alias // keeps ~/.ssh/config settings for the alias
	}
	if h.User != "" {
		s = h.User + "@" + s
	}
	if h.Port != 0 && h.Port != 22 {
		s += ":" + strconv.Itoa(h.Port)
	}
	return s
}

func envRef(shell, name string) string {
	if shell == "powershell" {
		return "$env:" + name
	}
	return `"$` + name + `"`
}

// quoter returns a function that quotes an argument when needed.
func quoter(shell string) func(string) string {
	var q func(string) string
	q = func(s string) string {
		if strings.HasPrefix(s, "~/") && len(s) > 2 {
			return "~/" + q(s[2:]) // keep ~ unquoted so the shell expands it
		}
		if s != "" && !strings.ContainsAny(s, " \t'\"$`*?[]{}()|&;<>!#~\\") {
			return s
		}
		if shell == "powershell" {
			return "'" + strings.ReplaceAll(s, "'", "''") + "'"
		}
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return q
}

// wrap joins args, breaking lines before flags when the command is long.
func wrap(shell string, args []string) string {
	one := strings.Join(args, " ")
	if len(one) <= 100 {
		return one
	}
	cont := " \\\n  "
	if shell == "powershell" {
		cont = " `\n  "
	}
	var b strings.Builder
	lineLen := 0
	for i, a := range args {
		if i > 0 {
			if strings.HasPrefix(a, "-") && lineLen > 40 {
				b.WriteString(cont)
				lineLen = 2
			} else {
				b.WriteByte(' ')
				lineLen++
			}
		}
		b.WriteString(a)
		lineLen += len(a)
	}
	return b.String()
}

// config renders a gNMIc config file equivalent.
func config(r Request, mode, addr, serverName string) string {
	var b strings.Builder
	b.WriteString("# gnmic --config canopy.yaml " + map[bool]string{true: "get", false: "subscribe"}[mode == "get"] + "\n")
	b.WriteString("# credentials come from the environment: GNMIC_USERNAME, GNMIC_PASSWORD\n")
	if r.Encoding != "" {
		fmt.Fprintf(&b, "encoding: %s\n", strings.ToLower(r.Encoding))
	}
	b.WriteString("targets:\n")
	fmt.Fprintf(&b, "  %q:\n", addr)
	switch {
	case r.Target.Insecure:
		b.WriteString("    insecure: true\n")
	case r.Target.SkipVerify:
		b.WriteString("    skip-verify: true\n")
	}
	if r.Target.CAFile != "" {
		fmt.Fprintf(&b, "    tls-ca: %q\n", r.Target.CAFile)
	}
	if serverName != "" && !r.Target.Insecure {
		fmt.Fprintf(&b, "    tls-server-name: %q\n", serverName)
	}
	if mode == "get" || mode == "capabilities" {
		if mode == "get" {
			b.WriteString("get-path:\n")
			for _, p := range r.Paths {
				fmt.Fprintf(&b, "  - %q\n", p)
			}
			if t := strings.ToLower(r.Type); t != "" && t != "all" {
				fmt.Fprintf(&b, "get-type: %s\n", t)
			}
		}
		return b.String()
	}
	b.WriteString("subscriptions:\n  canopy:\n    paths:\n")
	for _, p := range r.Paths {
		fmt.Fprintf(&b, "      - %q\n", p)
	}
	fmt.Fprintf(&b, "    mode: %s\n", mode)
	if mode == "stream" {
		sm := r.StreamMode
		if sm == "" {
			sm = "sample"
		}
		fmt.Fprintf(&b, "    stream-mode: %s\n", sm)
		if sm == "sample" {
			si := r.SampleInterval
			if si == "" {
				si = "10s"
			}
			fmt.Fprintf(&b, "    sample-interval: %s\n", si)
		}
	}
	return b.String()
}
