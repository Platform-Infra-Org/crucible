// Package userdocs embeds the in-app guide (the Docs tab). One folder per section; every page starts with front matter
// (title, roles, covers, order). Any capability change updates these pages in the same commit (CLAUDE.md).
package userdocs

import "embed"

//go:embed */*.md
var FS embed.FS
