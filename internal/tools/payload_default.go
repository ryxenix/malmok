//go:build !airgap

package tools

import "embed"

// A default build carries nothing. The empty FS answers false to every lookup,
// which is what makes the tools step fall back to downloading.
var payload embed.FS
