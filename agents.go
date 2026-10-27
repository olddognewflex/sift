// Package sift holds assets built into the sift binary.
package sift

import "embed"

// Agents contains the built-in agent definitions (agents/*.md).
//
//go:embed agents/*.md
var Agents embed.FS
