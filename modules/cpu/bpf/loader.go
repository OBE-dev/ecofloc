package cpubpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../include/bpf -target bpfel cpuBPF cpu_bpf.c

// The line above compiles cpu_bpf.c with clang and generates
// the cpubpf_bpfel.o object file and the cpubpf_bpfel.go binding
// This will be executed when running go generate.