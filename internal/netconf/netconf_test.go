package netconf

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeServer answers the hello, the schema list and get-schema, with RFC 6242
// chunked framing when chunked is set (base:1.1) or end-of-message framing.
func fakeServer(t *testing.T, chunked bool, in io.Reader, out io.Writer) {
	caps := `<capability>urn:ietf:params:netconf:base:1.0</capability>`
	if chunked {
		caps += `<capability>urn:ietf:params:netconf:base:1.1</capability>`
	}
	// both sides send their hello at once; a pipe has no buffer, so don't block
	go fmt.Fprintf(out, `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities>%s</capabilities><session-id>4</session-id></hello>]]>]]>`, caps)
	r := bufio.NewReader(in)
	readMsg := func() (string, error) {
		if !chunked {
			var b strings.Builder
			for !strings.HasSuffix(b.String(), "]]>]]>") {
				c, err := r.ReadByte()
				if err != nil {
					return "", err
				}
				b.WriteByte(c)
			}
			return strings.TrimSuffix(b.String(), "]]>]]>"), nil
		}
		var b bytes.Buffer
		for {
			if _, err := r.ReadString('#'); err != nil {
				return "", err
			}
			h, _ := r.ReadString('\n')
			h = strings.TrimSpace(h)
			if h == "#" {
				return b.String(), nil
			}
			n, _ := strconv.Atoi(h)
			io.CopyN(&b, r, int64(n))
		}
	}
	reply := func(s string) {
		msg := `<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">` + s + `</rpc-reply>`
		if !chunked {
			io.WriteString(out, msg+"]]>]]>")
			return
		}
		// split in two chunks to exercise reassembly
		h := len(msg) / 2
		fmt.Fprintf(out, "\n#%d\n%s\n#%d\n%s\n##\n", h, msg[:h], len(msg)-h, msg[h:])
	}
	// the client hello
	if !chunked {
		readMsg()
	} else {
		var b strings.Builder
		for !strings.HasSuffix(b.String(), "]]>]]>") {
			c, err := r.ReadByte()
			if err != nil {
				return
			}
			b.WriteByte(c)
		}
	}
	for {
		m, err := readMsg()
		if err != nil {
			return
		}
		switch {
		case strings.Contains(m, "<schemas/>"):
			reply(`<data><netconf-state xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring"><schemas>
<schema><identifier>acme-system</identifier><version>2024-01-01</version><format>yang</format><namespace>urn:acme</namespace></schema>
<schema><identifier>acme-system</identifier><version>2024-01-01</version><format>yin</format><namespace>urn:acme</namespace></schema>
<schema><identifier>acme-broken</identifier><version></version><format>ncm:yang</format><namespace>urn:b</namespace></schema>
<schema><identifier>../evil</identifier><version></version><format>yang</format><namespace>urn:e</namespace></schema>
</schemas></netconf-state></data>`)
		case strings.Contains(m, "<identifier>acme-system</identifier>"):
			reply(`<data xmlns="urn:ietf:params:xml:ns:yang:ietf-netconf-monitoring">module acme-system { namespace "urn:acme"; prefix as; leaf x { type string; description "a &lt; b"; } }</data>`)
		case strings.Contains(m, "<get-schema"):
			reply(`<rpc-error><error-tag>invalid-value</error-tag><error-message>no such schema</error-message></rpc-error>`)
		case strings.Contains(m, "<close-session"):
			reply(`<ok/>`)
			return
		}
	}
}

func TestDownload(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint("chunked=", chunked), func(t *testing.T) {
			cr, sw := io.Pipe()
			sr, cw := io.Pipe()
			go fakeServer(t, chunked, sr, sw)
			s, err := NewSession(cr, cw)
			if err != nil {
				t.Fatal(err)
			}
			if s.chunked != chunked {
				t.Fatalf("chunked %v", s.chunked)
			}
			dir := t.TempDir()
			n, skipped, err := s.Download(context.Background(), dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if n != 1 || len(skipped) != 2 {
				t.Fatalf("n=%d skipped=%v", n, skipped)
			}
			b, err := os.ReadFile(filepath.Join(dir, "acme-system@2024-01-01.yang"))
			if err != nil || !strings.Contains(string(b), `"a < b"`) {
				t.Fatalf("%s %v", b, err)
			}
			s.Close()
		})
	}
}
