package diskbpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../include/bpf -target bpfel diskBPF disk_bpf.c

// The line above compiles disk_bpf.c with clang and generates
// the diskbpf_bpfel.o object file and the diskbpf_bpfel.go binding
// This will be executed when running go generate.