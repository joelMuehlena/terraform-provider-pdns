lint:
	golangci-lint run

generate:
	cd tools; go generate ./...
