#!/bin/sh
export GIT_COMMIT=$(git rev-list -1 HEAD)
go generate
go generate github.com/wansing/ulist/internal/listdb/
go build -ldflags "-s -w -X main.GitCommit=$GIT_COMMIT"
