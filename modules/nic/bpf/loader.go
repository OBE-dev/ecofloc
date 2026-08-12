package nicbpf

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags -I../../../include/bpf -target bpfel nicBPF nic_bpf.c

// The line above compiles nic_bpf.c with clang and generates
// the nicbpf_bpfel.o object file and the nicbpf_bpfel.go binding
// This will be executed when running go generate.