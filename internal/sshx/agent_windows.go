//go:build windows

package sshx

import (
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

// dialAgent connects to the Windows OpenSSH agent's named pipe, or to the pipe
// named in SSH_AUTH_SOCK.
func dialAgent() (net.Conn, error) {
	pipe := `\\.\pipe\openssh-ssh-agent`
	if s := os.Getenv("SSH_AUTH_SOCK"); strings.HasPrefix(s, `\\.\pipe\`) {
		pipe = s
	}
	timeout := 2 * time.Second
	return winio.DialPipe(pipe, &timeout)
}
