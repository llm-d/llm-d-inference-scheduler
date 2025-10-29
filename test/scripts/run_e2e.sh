#!/bin/bash

echo "Running end to end tests"

DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
go test -v ${DIR}/../e2e/ -ginkgo.v
