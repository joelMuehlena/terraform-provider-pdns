lint:
	golangci-lint run

generate:
	cd tools; go generate ./...

test:
	go test -race -cover ./...
