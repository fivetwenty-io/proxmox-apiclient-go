// Package api groups the typed, generated bindings for the Proxmox VE
// REST API. Each child package (version, nodes, cluster, …) is generated
// by cmd/pvegen from _data/apidoc.json and exposes a Service interface
// plus typed request/response shapes.
//
// Do not add hand-written code at this directory level; everything here
// is metadata so that `go generate ./...` has a stable home.
package api

//go:generate go run ../../cmd/pvegen --spec ../../_data/apidoc.json --out . --namespace version
