//go:build !(linux && amd64) && !(linux && arm64) && !(darwin && amd64) && !(darwin && arm64)

package awsops

// AWS publishes no plugin we embed for this platform (Windows is not supported
// yet), so connect looks for one on PATH.
var pluginGz []byte
