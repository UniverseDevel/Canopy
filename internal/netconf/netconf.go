// Package netconf is a minimal NETCONF client (RFC 6241 over SSH, RFC 6242
// framing) for one job: downloading the YANG modules a device implements with
// get-schema (RFC 6022). It reads only; it never edits configuration.
package netconf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/UniverseDevel/Canopy/internal/apperr"
	"github.com/UniverseDevel/Canopy/internal/sshx"
)

const (
	base10 = "urn:ietf:params:netconf:base:1.0"
	base11 = "urn:ietf:params:netconf:base:1.1"
	eom    = "]]>]]>"
)

// Session is an open NETCONF session.
type Session struct {
	client  *ssh.Client
	sess    *ssh.Session
	w       io.Writer
	r       *bufio.Reader
	chunked bool
	msgID   int
	// Capabilities are the server's hello capabilities.
	Capabilities []string
}

// Options for Dial.
type Options struct {
	Address  string // host:port, usually port 830
	Username string
	Password string
	SSH      sshx.Config // host keys, agent and key files (jump hosts are in Dial)
	Timeout  time.Duration
}

// Dial opens a NETCONF session over conn (a TCP connection to the device,
// direct or through jump hosts).
func Dial(ctx context.Context, conn net.Conn, o Options) (*Session, error) {
	cfg := o.SSH
	cfg.Password = o.Password
	cfg.Timeout = o.Timeout
	client, err := sshx.Handshake(ctx, conn, o.Username, o.Address, cfg)
	if err != nil {
		conn.Close()
		if e := apperr.As(err); e != nil {
			e.Message = strings.Replace(e.Message, "jump host", "NETCONF", 1)
		}
		return nil, err
	}
	s, err := open(client)
	if err != nil {
		client.Close()
		return nil, err
	}
	return s, nil
}

func open(client *ssh.Client) (*Session, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, ncErr(err)
	}
	w, err := sess.StdinPipe()
	if err != nil {
		return nil, ncErr(err)
	}
	r, err := sess.StdoutPipe()
	if err != nil {
		return nil, ncErr(err)
	}
	if err := sess.RequestSubsystem("netconf"); err != nil {
		sess.Close()
		return nil, apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable, fmt.Errorf("NETCONF subsystem: %w", err)).
			WithHint("NETCONF may be disabled on the device")
	}
	s := &Session{client: client, sess: sess, w: w, r: bufio.NewReaderSize(r, 64<<10)}
	return s, s.hello()
}

// NewSession runs NETCONF over an established reader and writer (tests).
func NewSession(r io.Reader, w io.Writer) (*Session, error) {
	s := &Session{w: w, r: bufio.NewReaderSize(r, 64<<10)}
	return s, s.hello()
}

func (s *Session) hello() error {
	if _, err := io.WriteString(s.w, `<?xml version="1.0" encoding="UTF-8"?><hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>`+
		base10+`</capability><capability>`+base11+`</capability></capabilities></hello>`+eom); err != nil {
		return ncErr(err)
	}
	b, err := s.readEOM()
	if err != nil {
		return err
	}
	var h struct {
		Caps []string `xml:"capabilities>capability"`
	}
	if err := xml.Unmarshal(b, &h); err != nil {
		return ncErr(fmt.Errorf("server hello: %w", err))
	}
	s.Capabilities = h.Caps
	for _, c := range h.Caps {
		if strings.TrimSpace(c) == base11 {
			s.chunked = true
		}
	}
	return nil
}

// Close ends the session.
func (s *Session) Close() error {
	if s.w != nil {
		s.send(`<close-session/>`)
	}
	if s.sess != nil {
		s.sess.Close()
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

func (s *Session) send(op string) error {
	s.msgID++
	msg := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><rpc message-id="%d" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">%s</rpc>`, s.msgID, op)
	var err error
	if s.chunked {
		_, err = fmt.Fprintf(s.w, "\n#%d\n%s\n##\n", len(msg), msg)
	} else {
		_, err = io.WriteString(s.w, msg+eom)
	}
	return ncErr(err)
}

// Call sends an RPC and returns the reply.
func (s *Session) Call(op string) ([]byte, error) {
	if err := s.send(op); err != nil {
		return nil, err
	}
	var b []byte
	var err error
	if s.chunked {
		b, err = s.readChunked()
	} else {
		b, err = s.readEOM()
	}
	if err != nil {
		return nil, err
	}
	var re struct {
		Errors []struct {
			Tag     string `xml:"error-tag"`
			Message string `xml:"error-message"`
		} `xml:"rpc-error"`
	}
	xml.Unmarshal(b, &re)
	if len(re.Errors) > 0 {
		e := re.Errors[0]
		return nil, apperr.New(apperr.ClassDevice, apperr.CodeDevice, "NETCONF error %s: %s", e.Tag, strings.TrimSpace(e.Message))
	}
	return b, nil
}

const maxMsg = 64 << 20

func (s *Session) readEOM() ([]byte, error) {
	var buf bytes.Buffer
	for {
		line, err := s.r.ReadBytes('>')
		buf.Write(line)
		if bytes.HasSuffix(buf.Bytes(), []byte(eom)) {
			return bytes.TrimSpace(buf.Bytes()[:buf.Len()-len(eom)]), nil
		}
		if err != nil {
			return nil, ncErr(err)
		}
		if buf.Len() > maxMsg {
			return nil, ncErr(fmt.Errorf("message too large"))
		}
	}
}

func (s *Session) readChunked() ([]byte, error) {
	var buf bytes.Buffer
	for {
		// "\n#<size>\n" or "\n##\n"
		if _, err := s.r.ReadString('#'); err != nil {
			return nil, ncErr(err)
		}
		hdr, err := s.r.ReadString('\n')
		if err != nil {
			return nil, ncErr(err)
		}
		hdr = strings.TrimSpace(hdr)
		if hdr == "#" {
			return buf.Bytes(), nil
		}
		n, err := strconv.Atoi(hdr)
		if err != nil || n <= 0 || buf.Len()+n > maxMsg {
			return nil, ncErr(fmt.Errorf("bad chunk header %q", hdr))
		}
		if _, err := io.CopyN(&buf, s.r, int64(n)); err != nil {
			return nil, ncErr(err)
		}
	}
}

// Schema is one entry of ietf-netconf-monitoring's schema list.
type Schema struct {
	Identifier string `xml:"identifier" json:"identifier"`
	Version    string `xml:"version" json:"version"`
	Format     string `xml:"format" json:"format"`
	Namespace  string `xml:"namespace" json:"namespace"`
}

// Schemas lists the schemas the device offers (RFC 6022).
func (s *Session) Schemas() ([]Schema, error) {
	b, err := s.Call(`<get><filter type="subtree"><netconf-state xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring"><schemas/></netconf-state></filter></get>`)
	if err != nil {
		return nil, err
	}
	var r struct {
		Schemas []Schema `xml:"data>netconf-state>schemas>schema"`
	}
	if err := xml.Unmarshal(b, &r); err != nil {
		return nil, ncErr(fmt.Errorf("schema list: %w", err))
	}
	var out []Schema
	for _, x := range r.Schemas {
		// format is an identity, e.g. "yang" or "ncm:yang"
		if f := x.Format[strings.LastIndex(x.Format, ":")+1:]; f == "yang" || f == "" {
			out = append(out, x)
		}
	}
	return out, nil
}

// GetSchema returns the text of one module.
func (s *Session) GetSchema(id, version string) (string, error) {
	v := ""
	if version != "" {
		v = "<version>" + xmlEscape(version) + "</version>"
	}
	b, err := s.Call(`<get-schema xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring"><identifier>` + xmlEscape(id) + `</identifier>` + v + `<format>yang</format></get-schema>`)
	if err != nil {
		return "", err
	}
	var r struct {
		Data string `xml:"data"`
	}
	if err := xml.Unmarshal(b, &r); err != nil {
		return "", ncErr(fmt.Errorf("get-schema %s: %w", id, err))
	}
	return r.Data, nil
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Download writes every YANG schema the device offers into dir as
// name@revision.yang and returns how many were written. Schemas that can't be
// fetched are reported in skipped.
func (s *Session) Download(ctx context.Context, dir string, progress func(string)) (n int, skipped []string, err error) {
	list, err := s.Schemas()
	if err != nil {
		return 0, nil, err
	}
	if len(list) == 0 {
		return 0, nil, apperr.New(apperr.ClassSchema, apperr.CodeNotFound, "the device lists no YANG schemas (ietf-netconf-monitoring)")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, nil, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
	}
	if progress != nil {
		progress(fmt.Sprintf("downloading %d YANG schemas over NETCONF", len(list)))
	}
	for i, x := range list {
		if ctx.Err() != nil {
			return n, skipped, ctx.Err()
		}
		if !safeName.MatchString(x.Identifier) || (x.Version != "" && !safeName.MatchString(x.Version)) {
			skipped = append(skipped, x.Identifier+": unusual name")
			continue
		}
		text, err := s.GetSchema(x.Identifier, x.Version)
		if err != nil {
			skipped = append(skipped, x.Identifier+": "+err.Error())
			continue
		}
		name := x.Identifier
		if x.Version != "" {
			name += "@" + x.Version
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yang"), []byte(text), 0o644); err != nil {
			return n, skipped, apperr.Wrap(apperr.ClassInternal, apperr.CodeInternal, err)
		}
		n++
		if progress != nil && (i+1)%50 == 0 {
			progress(fmt.Sprintf("%d of %d", i+1, len(list)))
		}
	}
	return n, skipped, nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func ncErr(err error) error {
	if err == nil {
		return nil
	}
	if apperr.As(err) != nil {
		return err
	}
	return apperr.Wrap(apperr.ClassConnection, apperr.CodeUnreachable, fmt.Errorf("NETCONF: %w", err))
}
