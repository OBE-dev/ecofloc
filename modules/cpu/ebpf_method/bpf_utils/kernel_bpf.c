//go:build ignore

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char LICENSE[] SEC("license") = "GPL";

/* TGID of the process to monitor. 0 means system-wide monitoring. */
volatile __u32 target_tgid = 0;

/* code logic:
 * The scheduler emits sched_stat_runtime events every time it
 * accounts the runtime of the running task (scheduler tick,
 * context switch...). The event carries the CPU time consumed since the previous
 * accounting, which is added to the time consumed on the current CPU, either for
 * the monitored TGID (targeted mode) or for the whole system (tgid key = 0).
*/

struct cpu_time_key {
    __u32 tgid; /* monitored TGID, or 0 for the whole system */
    __u32 cpu;  /* logical CPU id */
};

struct cpu_time_consumed {
    __u64 ns;
};

/* CPU time consumed per (tgid, cpu): map[{tgid, cpu}] = cpu_time_consumed */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, struct cpu_time_key);
    __type(value, struct cpu_time_consumed);
    __uint(max_entries, 1024);
} cpu_time_ns SEC(".maps");

SEC("tracepoint/sched/sched_stat_runtime")
int handle_sched_stat_runtime(struct trace_event_raw_sched_stat_runtime *ctx)
{
    /* skip the idle task */
    if (ctx->pid == 0)
        return 0;

    /* by default we set key.tgid to 0 (system-wide monitoring)*/
    struct cpu_time_key key = { .tgid = 0, .cpu = bpf_get_smp_processor_id() };

    /* if we are monitoring a specific TGID, check if the current task matches */
    if (target_tgid != 0) {
        __u64 pid_tgid = bpf_get_current_pid_tgid(); // pid_tgid = (tgid << 32) | pid
        if ((__u32)(pid_tgid >> 32) != target_tgid)
            return 0;
        key.tgid = target_tgid;
    }
    /* lookup the CPU time consumed for this (tgid, cpu) */
    struct cpu_time_consumed *ct = bpf_map_lookup_elem(&cpu_time_ns, &key);
    if (!ct) {
        /* if this (tgid, cpu) still have no entry, create one */
        struct cpu_time_consumed init = {};
        bpf_map_update_elem(&cpu_time_ns, &key, &init, BPF_ANY);
        ct = bpf_map_lookup_elem(&cpu_time_ns, &key);
        /* if the entry has still no value, return */
        if (!ct)
            return 0;
    }
    /* add the runtime consumed since the previous scheduler accounting on this CPU */
    __sync_fetch_and_add(&ct->ns, ctx->runtime);

    return 0;
}
