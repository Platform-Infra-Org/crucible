//go:build darwin && arm64

package awsops

import _ "embed"

// A build for darwin/arm64 embeds only its own plugin, so a binary carries one
// copy and not four. An empty placeholder means this tree has not run
// scripts/plugin-blobs.sh; sessionPlugin then falls back to PATH.

//go:embed blobs/session-manager-plugin_darwin_arm64.gz
var pluginGz []byte
