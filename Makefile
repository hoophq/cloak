.PHONY: build test e2e vet

build:
	go build -o cloak .

test:
	go test ./...

# Requires Docker; runs the full broker path against real PostgreSQL.
e2e:
	go test -tags e2e -count=1 -v ./e2e/

vet:
	go vet ./...
