package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/UniverseDevel/Canopy/internal/apperr"
)

// pickFolder opens the operating system's folder dialog on this computer (the
// UI only listens on loopback, so it is the user's own desktop) and returns
// the folder chosen. A browser can't give a page real paths, so the server
// asks. No CGo: PowerShell on Windows, osascript on macOS, zenity or kdialog
// on Linux.
func (s *Server) pickFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Start string `json:"start"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err, 0)
		return
	}
	if req.Title == "" {
		req.Title = "Choose a folder"
	}
	if st, err := os.Stat(req.Start); err != nil || !st.IsDir() {
		req.Start = ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	path, err := runPicker(ctx, req.Title, req.Start)
	if err != nil {
		writeErr(w, err, 0)
		return
	}
	writeJSON(w, map[string]any{"path": path, "cancelled": path == ""})
}

func runPicker(ctx context.Context, title, start string) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// The dialog gets a hidden topmost owner, so it opens in front of the browser.
		script := `Add-Type -AssemblyName System.Windows.Forms
$o = New-Object System.Windows.Forms.Form -Property @{TopMost = $true; ShowInTaskbar = $false}
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = $env:CANOPY_PICK_TITLE
$d.UseDescriptionForTitle = $true
if ($env:CANOPY_PICK_START) { $d.SelectedPath = $env:CANOPY_PICK_START }
if ($d.ShowDialog($o) -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.SelectedPath) }`
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
	case "darwin":
		script := `set p to ""
try
  if (system attribute "CANOPY_PICK_START") is not "" then
    set p to POSIX path of (choose folder with prompt (system attribute "CANOPY_PICK_TITLE") default location (POSIX file (system attribute "CANOPY_PICK_START")))
  else
    set p to POSIX path of (choose folder with prompt (system attribute "CANOPY_PICK_TITLE"))
  end if
end try
return p`
		cmd = exec.CommandContext(ctx, "osascript", "-e", script)
	default:
		if p, err := exec.LookPath("zenity"); err == nil {
			args := []string{"--file-selection", "--directory", "--title", title}
			if start != "" {
				args = append(args, "--filename", strings.TrimSuffix(start, "/")+"/")
			}
			cmd = exec.CommandContext(ctx, p, args...)
		} else if p, err := exec.LookPath("kdialog"); err == nil {
			if start == "" {
				start = "."
			}
			cmd = exec.CommandContext(ctx, p, "--title", title, "--getexistingdirectory", start)
		} else {
			return "", apperr.New(apperr.ClassUsage, apperr.CodeUsage, "no folder dialog available").
				WithHint("install zenity or kdialog, or type the path")
		}
	}
	cmd.Env = append(os.Environ(), "CANOPY_PICK_TITLE="+title, "CANOPY_PICK_START="+start)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return "", apperr.Wrap(apperr.ClassUsage, apperr.CodeUsage, err).WithHint("type the path instead")
	}
	// zenity and kdialog exit 1 when cancelled: that is no choice, not an error
	return strings.TrimSpace(string(out)), nil
}
