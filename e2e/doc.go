// Package e2e drives the built ah binary against a real OpenSSH server in
// Docker. The tests carry the e2e build tag so plain `go test ./...` never
// needs Docker; run them with `make e2e`.
package e2e
