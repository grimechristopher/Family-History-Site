// Package assets embeds the templates and static files.
//
// They live in their own package at the repository root so they can be edited as
// ordinary files, while still compiling into the binary — deployment stays a
// single artefact with nothing to copy alongside it.
package assets

import "embed"

//go:embed all:templates all:static
var FS embed.FS
