package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// A local flag whose shorthand is already taken by an inherited (persistent)
// flag makes cobra panic when that command runs, even for --help.
func TestNoShorthandClashes(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: %v", c.CommandPath(), r)
				}
			}()
			c.InheritedFlags()
			c.LocalFlags()
		}()
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot(&globals{}))
}
