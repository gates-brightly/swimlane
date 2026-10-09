// Package swimlane holds files at the repo root that the swim binary embeds
// (go:embed can't reach above a package's own directory).
package swimlane

import _ "embed"

// Changelog is CHANGELOG.md: swim's revision history, printed by
// `swim changelog`.
//
//go:embed CHANGELOG.md
var Changelog string
