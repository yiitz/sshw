#!/bin/bash
go build -ldflags "-s -w" -o sshw cmd/sshw/main.go
mv sshw $GOPATH/bin/