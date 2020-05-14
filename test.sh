#!/bin/sh
go generate
go generate github.com/wansing/ulist/internal/listdb/
go test ./... -cover
