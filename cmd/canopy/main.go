// Command canopy is a vendor-agnostic gNMI and YANG browser.
package main

import (
	"os"

	"github.com/UniverseDevel/Canopy/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
