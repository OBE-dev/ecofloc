############# ecofloc build #############

# for the first time you need to run: make vmlinux
# this will generate the vmlinux.h under the /include/bpf/ directory. This file will be needed by the bpf code

# the ecofloc project is compiled in two stages:
# 1. make generate: this will let the clang compiles the *_bpf.c files into bpf object files (*_bpfel.o) and the bpf2go will 
# generate the go bindings (*_bpfel.go) from the bpf object files. the make generate will run the //go:generate directives included in all the loader.go files)
# 2. make build: this will build the ecofloc binary, from the all the .go files and link the bpf object files generated in the first stage.
# the first stage is only necessary for the first compilation or when the *_bpf.c files are modified.

PKG		:= ./...
BINARY  := ./bin/ecofloc
GO      ?= go
CLANG   ?= clang

.PHONY: all generate build clean tidy vmlinux

## vmlinux: generate the BTF header required by the bpf c code
vmlinux:
	bpftool btf dump file /sys/kernel/btf/vmlinux format c > include/bpf/vmlinux.h

## all: generate and build
all: generate build

## generate: generate the bpf object files and go bindings
generate:
	$(GO) generate $(PKG)

## build: build the ecofloc binary
build:
	$(GO) build -o $(BINARY) .
	
## clean: clean the build binary (keep the generated object files and bindings)
clean:
	rm -f $(BINARY)

## tidy: sync the go modules (update go.mod and go.sum)
tidy:
	$(GO) mod tidy