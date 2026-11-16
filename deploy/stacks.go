// Package deploy carries the Terraform stacks inside the binary, so an operator
// needs no checkout of this repository. The files here are the source of truth;
// awsops writes them out to the application directory before running Terraform.
package deploy

import "embed"

// Stacks holds deploy/aws/{persistent,main,labs} as they are in this release.
//
//go:embed all:aws
var Stacks embed.FS
