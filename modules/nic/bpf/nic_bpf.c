//go:build ignore

// NIC traffic accounting using eBPF cgroup socket buffer programs

// cGroup socket buffer programs are attached to a cGroup and are called for incoming
// or outgoing packets to or from processes within that cGroup.
//
// 2 Modes supported:
//   - system-wide: we attach to the root cgroup (/sys/fs/cgroup), so we count 
//	    the sent/received bytes on all interfaces for the whole system.
//   - per-process: we attach to a dedicated child cgroup that contains the target
//     process we want to monitor, so we count the sent/received 
//     bytes only for that process and it's threads.

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>

#define DIR_EGRESS  0  // bytes sent
#define DIR_INGRESS 1  // bytes received

struct nic_metrics {
	__u64 rx_bytes;
	__u64 tx_bytes;
};

// key: __u32 ifindex (network interface index)
// value: struct nic_metrics (rx_bytes, tx_bytes)
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 256);
	__type(key, __u32);
	__type(value, struct nic_metrics);
} nic_stats SEC(".maps");

static __always_inline void account_bytes(__u32 ifindex, __u8 dir, __u32 len)
{
	struct nic_metrics *m;
	struct nic_metrics zero = {};

	// no interface index, skip
	if (ifindex == 0)
		return;

	// get the metrics for this interface
	m = bpf_map_lookup_elem(&nic_stats, &ifindex);
	if (!m) {
		// if the interface still have no metrics, create an empty one for it
		bpf_map_update_elem(&nic_stats, &ifindex, &zero, BPF_NOEXIST);
		m = bpf_map_lookup_elem(&nic_stats, &ifindex);
		// if we still can't get the metrics, skip
		if (!m)
			return;
	}

	 // update the metrics depending on the direction TX/RX
	if (dir == DIR_EGRESS)
		__sync_fetch_and_add(&m->tx_bytes, len);
	else
		__sync_fetch_and_add(&m->rx_bytes, len);
}

// Account every packet sent by a socket in the attached cgroup.
// skb->ifindex is the outgoing interface selected by routing.
SEC("cgroup_skb/egress")
int cg_count_egress(struct __sk_buff *skb)
{ 
	account_bytes(skb->ifindex, DIR_EGRESS, skb->len);
	return 1; // allow the packet
}

// Account every packet received by a socket in the attached cgroup.
// skb->ifindex is the interface the packet arrived on.
SEC("cgroup_skb/ingress")
int cg_count_ingress(struct __sk_buff *skb)
{
	account_bytes(skb->ifindex, DIR_INGRESS, skb->len);
	return 1; // allow the packet
}

char LICENSE[] SEC("license") = "GPL";