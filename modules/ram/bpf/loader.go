package rambpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../include/bpf -target bpfel ramBPF ram_bpf.c

// The line above compiles ram_bpf.c with clang and generates
// the rambpf_bpfel.o object file and the rambpf_bpfel.go binding
// This will be executed when running go generate.