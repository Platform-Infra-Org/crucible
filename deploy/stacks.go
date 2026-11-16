// Package deploy carries the Terraform stacks inside the binary, so an operator
// needs no checkout of this repository. The files here are the source of truth;
// awsops writes them out to the application directory before running Terraform.
package deploy

import "embed"

// Stacks holds deploy/aws/{persistent,main,labs} as they are in this release.
//
// The patterns are explicit on purpose. A build tree that has run
// `crucible aws init` also holds `terraform.tfstate`, `.terraform/` and
// `crucible.tfvars` in these directories — all three are git-ignored — and an
// `all:aws` pattern would embed them, publishing the secret key and the GHCR
// token inside a release and letting a later apply run against stale state. So
// only the files a stack actually needs are named, and `.terraform.lock.hcl` is
// named one by one because it is dot-prefixed: a plain glob skips it, and
// without it a fresh `terraform init` could resolve a provider this release was
// never tested with.
//
//go:embed aws/*/*.tf aws/*/*.tftpl
//go:embed aws/persistent/.terraform.lock.hcl aws/main/.terraform.lock.hcl aws/labs/.terraform.lock.hcl
var Stacks embed.FS
