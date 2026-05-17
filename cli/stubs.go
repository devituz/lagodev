package cli

import (
	"embed"
	"io/fs"
)

//go:embed stubs/*.stub
var stubsFS embed.FS

// Stubs returns the embedded stub filesystem rooted at "stubs/". The CLI
// command implementations read from this FS by default; applications can
// override individual stubs via cmd.SetStubOverride().
func Stubs() fs.FS {
	sub, err := fs.Sub(stubsFS, "stubs")
	if err != nil {
		panic("cli: invalid embedded stubs: " + err.Error())
	}
	return sub
}
