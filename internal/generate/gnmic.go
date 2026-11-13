// Package generate produces gNMIc commands from a target and a selection of
// paths. Every command is a single line. With SSH jump hosts, gNMIc runs on the
// last jump host through ssh, so nothing has to stay running. Secrets are never
// embedded: commands reference $GNMI_USERNAME and $GNMI_PASSWORD, which the local
// shell expands.
package generate

import (
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
	Mode     string       `json:"mode"`               // get (default), once, stream, poll, capabilities, set
	Type     string       `json:"type,omitempty"`     // get: all, config, state, operational
	Encoding string       `json:"encoding,omitempty"` // empty: gNMIc default
	// Stream options
	StreamMode     string `json:"streamMode,omitempty"`     // sample (default), on-change, target-defined
	SampleInterval string `json:"sampleInterval,omitempty"` // default 10s
	Shell          string `json:"shell,omitempty"`          // bash (default) or powershell
	// SetOps are the changes for mode "set".
	SetOps []SetOp `json:"ops,omitempty"`
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
}

// SetOp is one change for mode "set".
type SetOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value,omitempty"` // JSON text
}

// seg is a piece of a command line: literal text, or the name of an environment
// variable that the local shell expands.
type seg struct {
	text string
	env  bool
}

type line struct {
	segs   []seg
	shell  string // shell that parses gNMIc's arguments
	remote bool
}

func (l *line) lit(parts ...string) {
	for _, p := range parts {
		if len(l.segs) > 0 {
			p = " " + p
		}
		if n := len(l.segs); n > 0 && !l.segs[n-1].env {
			l.segs[n-1].text += p
		} else {
			l.segs = append(l.segs, seg{text: p})
		}
	}
}

func (l *line) cred(flag, name string) {
	switch {
	case l.remote:
		// expanded by the local shell, single-quoted for the remote one
		l.lit(flag, "'")
		l.segs = append(l.segs, seg{text: name, env: true}, seg{text: "'"})
	case l.shell == "powershell":
		l.lit(flag, "$env:"+name)
	default:
		l.lit(flag, `"$`+name+`"`)
	}
}

// Gnmic generates the command.
func Gnmic(r Request) (*Output, error) {
	mode := strings.ToLower(r.Mode)
	if mode == "" {
		mode = "get"
	}
	switch mode {
	case "get", "once", "stream", "poll", "capabilities", "set":
	default:
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown mode %q (get, once, stream, poll, capabilities, set)", r.Mode)
	}
	if mode != "capabilities" && mode != "set" && len(r.Paths) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "at least one path is required")
	}
	if mode == "set" && len(r.SetOps) == 0 {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no changes given")
	}
	shell := strings.ToLower(r.Shell)
	if shell == "" {
		shell = "bash"
	}
	if shell != "bash" && shell != "powershell" {
		return nil, apperr.New(apperr.ClassUsage, apperr.CodeUsage, "unknown shell %q (bash, powershell)", r.Shell)
	}
	// Devices refuse a Get that mixes origins: one gnmic get per origin, chained.
	if groups := byOrigin(r.Paths); mode == "get" && len(groups) > 1 {
		var parts []string
		var note string
		for _, g := range groups {
			one := r
			one.Paths = g
			o, err := Gnmic(one)
			if err != nil {
				return nil, err
			}
			parts = append(parts, o.Commands[0].Text)
			note = o.Commands[0].Note
		}
		sep := " && "
		if shell == "powershell" {
			sep = "; "
		}
		if note != "" {
			note += "; "
		}
		return &Output{Shell: shell, Commands: []Command{{Title: "gNMIc get", Text: strings.Join(parts, sep),
			Note: note + "one get per origin, since devices refuse a Get that mixes them"}}}, nil
	}
	remote := len(r.Hops) > 0
	l := &line{shell: shell, remote: remote}
	if remote {
		l.shell = "bash" // the jump host's shell parses gNMIc's arguments
	}
	q := quoter(l.shell)

	l.lit("gnmic", "-a", r.Target.Address)
	l.cred("-u", "GNMI_USERNAME")
	l.cred("-p", "GNMI_PASSWORD")
	switch {
	case r.Target.Insecure:
		l.lit("--insecure")
	case r.Target.SkipVerify:
		l.lit("--skip-verify")
	}
	// Certificates held in memory (e.g. from a network-operator Device's
	// Secrets) have no file: name placeholder files and say so in the note.
	memTLS := ""
	if r.Target.CAFile != "" {
		l.lit("--tls-ca", q(r.Target.CAFile))
	} else if len(r.Target.CAPEM) > 0 && !r.Target.Insecure {
		l.lit("--tls-ca", "ca.pem")
		memTLS = "ca.pem"
	}
	if r.Target.CertFile != "" {
		l.lit("--tls-cert", q(r.Target.CertFile), "--tls-key", q(r.Target.KeyFile))
	} else if len(r.Target.CertPEM) > 0 && !r.Target.Insecure {
		l.lit("--tls-cert", "cert.pem", "--tls-key", "key.pem")
		memTLS = strings.TrimPrefix(memTLS+", cert.pem, key.pem", ", ")
	}
	memNote := ""
	if memTLS != "" {
		memNote = "save the device's TLS certificates (for an operator Device, from its TLS Secrets) as " + memTLS + " first; Canopy holds them in memory only"
	}
	if r.Target.ServerName != "" && !r.Target.Insecure {
		l.lit("--tls-server-name", q(r.Target.ServerName))
	}
	if r.Target.Timeout > 0 {
		l.lit("--timeout", r.Target.Timeout.String())
	}
	if r.Encoding != "" && mode != "capabilities" {
		l.lit("-e", strings.ToLower(r.Encoding))
	} else if mode == "set" {
		l.lit("-e", "json_ietf")
	}
	switch mode {
	case "capabilities":
		l.lit("capabilities")
	case "set":
		l.lit("set")
		for _, op := range r.SetOps {
			switch strings.ToLower(op.Op) {
			case "delete":
				l.lit("--delete", q(op.Path))
			case "replace":
				l.lit("--replace-path", q(op.Path), "--replace-value", quoteAlways(l.shell, op.Value))
			default:
				l.lit("--update-path", q(op.Path), "--update-value", quoteAlways(l.shell, op.Value))
			}
		}
	case "get":
		l.lit("get")
		if t := strings.ToLower(r.Type); t != "" && t != "all" {
			l.lit("--type", t)
		}
		for _, p := range r.Paths {
			l.lit("--path", q(p))
		}
	default:
		l.lit("subscribe")
		for _, p := range r.Paths {
			l.lit("--path", q(p))
		}
		l.lit("--mode", mode)
		if mode == "stream" {
			sm := r.StreamMode
			if sm == "" {
				sm = "sample"
			}
			l.lit("--stream-mode", sm)
			if sm == "sample" {
				si := r.SampleInterval
				if si == "" {
					si = "10s"
				}
				l.lit("--sample-interval", si)
			}
		}
	}
	out := &Output{Shell: shell}
	c := Command{Title: "gNMIc " + mode}
	if !remote {
		for _, s := range l.segs {
			c.Text += s.text
		}
		c.Note = memNote
		out.Commands = append(out.Commands, c)
		return out, nil
	}
	// ssh [-i key] [-J hop1,...] [-p port] [-t] user@lasthop "gnmic ..."
	sq := quoter(shell)
	ssh := []string{"ssh"}
	if r.Target.SSH != nil {
		for _, f := range r.Target.SSH.IdentityFiles {
			ssh = append(ssh, "-i", sq(f))
		}
	}
	var jumps []string
	for _, h := range r.Hops[:len(r.Hops)-1] {
		jumps = append(jumps, hopString(h))
	}
	if len(jumps) > 0 {
		ssh = append(ssh, "-J", strings.Join(jumps, ","))
	}
	last := r.Hops[len(r.Hops)-1]
	if last.Port != 0 && last.Port != 22 {
		ssh = append(ssh, "-p", strconv.Itoa(last.Port))
	}
	if mode == "stream" || mode == "poll" {
		ssh = append(ssh, "-t") // Ctrl+C reaches gNMIc
	}
	dest := last.Host
	if last.Alias != "" {
		dest = last.Alias
	}
	if last.User != "" {
		dest = last.User + "@" + dest
	}
	ssh = append(ssh, dest)
	c.Text = strings.Join(ssh, " ") + " " + remoteArg(shell, l.segs)
	host, _, _ := net.SplitHostPort(r.Target.Address)
	c.Note = "gNMIc runs on " + dest + " (it must be installed there) and connects to " + host + " directly"
	if r.Target.CAFile != "" || r.Target.CertFile != "" || memTLS != "" {
		c.Note += "; the TLS files must exist at these paths on " + dest
	}
	if memNote != "" {
		c.Note += "; " + memNote
	}
	out.Commands = append(out.Commands, c)
	return out, nil
}

// remoteArg renders the remote command as one argument in which the local shell
// expands only the credential references.
func remoteArg(shell string, segs []seg) string {
	if shell == "powershell" {
		var parts []string
		for _, s := range segs {
			if s.env {
				parts = append(parts, "$env:"+s.text)
			} else {
				parts = append(parts, "'"+strings.ReplaceAll(s.text, "'", "''")+"'")
			}
		}
		return "(" + strings.Join(parts, " + ") + ")"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, s := range segs {
		if s.env {
			b.WriteString("$" + s.text)
			continue
		}
		for _, ch := range s.text {
			if strings.ContainsRune("\\\"$`", ch) {
				b.WriteByte('\\')
			}
			b.WriteRune(ch)
		}
	}
	b.WriteByte('"')
	return b.String()
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
		return quoteAlways(shell, s)
	}
	return q
}

func quoteAlways(shell, s string) string {
	if shell == "powershell" {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// GnmicSet generates the gnmic set command for the changes (values inline).
func GnmicSet(r Request, ops []SetOp) (*Output, error) {
	r.Mode, r.SetOps, r.Paths = "set", ops, nil
	return Gnmic(r)
}

// byOrigin groups paths by their gNMI origin ("openconfig:/..." vs "/...").
func byOrigin(paths []string) [][]string {
	var out [][]string
	idx := map[string]int{}
	for _, p := range paths {
		o := ""
		if !strings.HasPrefix(p, "/") {
			if i := strings.Index(p, ":/"); i > 0 && !strings.ContainsAny(p[:i], "/[") {
				o = p[:i]
			}
		}
		i, ok := idx[o]
		if !ok {
			i = len(out)
			idx[o] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], p)
	}
	return out
}
