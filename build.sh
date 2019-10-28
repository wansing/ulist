#!/bin/sh
export GIT_COMMIT=$(git rev-list -1 HEAD)
go generate
go build -ldflags "-s -w -X main.GitCommit=$GIT_COMMIT"
