// Package user embeds the in-app Docs pages (Markdown with front matter, one folder per section).
package user

import "embed"

// FS holds every page; docs.Load ignores non-Markdown files such as this one's source.
//
//go:embed *
var FS embed.FS
