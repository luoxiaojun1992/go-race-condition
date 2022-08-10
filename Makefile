.PHONY: build
build:
	go build -a -o ./build/`uname -s`_`uname -m`/linter ./cmd/main.go

.PHONY: demo
demo: build
	./build/`uname -s`_`uname -m`/linter --file ./testdata/sample.go

.PHONY: vet
vet:
	go vet ./pkg ./cmd

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: fmt
fmt:
	go fmt ./pkg ./cmd

.PHONY: init
init: tidy

.PHONY: all
all: tidy vet fmt build