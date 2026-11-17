package awsops

import _ "embed"

// The licence and notice AWS ships beside the plugin. They are the same text
// for every platform, so they are embedded once, unconditionally.

//go:embed blobs/session-manager-plugin.LICENSE
var pluginLicence []byte

//go:embed blobs/session-manager-plugin.NOTICE
var pluginNotice []byte
