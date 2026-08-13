.PHONY: build test fmt docker

build:
	go build -o _out/omni-infra-provider-xoa ./cmd/omni-infra-provider-xoa

test:
	go test ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

docker:
	docker build -t omni-infra-provider-xoa:local .
