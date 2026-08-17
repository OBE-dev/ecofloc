//go:build ignore

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char LICENSE[] SEC("license") = "GPL";

/* code logic:
 * - when a process is scheduled out, calculate the delta time and add it to the cpu time consumed for this pid
 * - when a process is scheduled in, register the start time for this {cpu, pid}
 */

struct pid_key {
    __u32 pid;
};

struct cpu_time_consumed {
    __u64 ns;
};

struct pid_start_time {
    __u64 start_ns;
};

struct cpu_pid_key {
    __u32 cpu;
    __u32 pid;
};

/* create a map to store the CPU time consumed by each pid: map["pid"] = cpu_time_consumed */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, struct pid_key);
    __type(value, struct cpu_time_consumed);
    __uint(max_entries, 10240);
} cpu_time_ns SEC(".maps");

/* create a map to store the start time of specific pid on a specific cpu: map["cpu", "pid"] = pid_start_time */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, struct cpu_pid_key);
    __type(value, struct pid_start_time);
    __uint(max_entries, 10240);
} cpu_pid_start SEC(".maps");

SEC("tracepoint/sched/sched_switch")
int handle_sched_switch(struct trace_event_raw_sched_switch *ctx)
{
    /* get current timestamp in nanoseconds */
    __u64 now = bpf_ktime_get_ns(); 
    
    /* get current CPU */
    __u32 cpu = bpf_get_smp_processor_id();

    /* if previous process is not idle */
    if (ctx->prev_pid > 0) {
        /* get the previous {cpu, pid} key */
        struct cpu_pid_key cpu_pid_prev = { .cpu = cpu, .pid = ctx->prev_pid };
        /* get the registred start time for this {cpu, pid} */
        struct pid_start_time *st = bpf_map_lookup_elem(&cpu_pid_start, &cpu_pid_prev);
        /* if this {cpu, pid} is found in the map */
        if (st) {
            /* calculate the delta time */
            __u64 delta = (now > st->start_ns) ? (now - st->start_ns) : 0;
            /* get the registered cpu time consumed for this pid */
            struct pid_key pk = { .pid = (__u32)ctx->prev_pid };
            struct cpu_time_consumed *ct = bpf_map_lookup_elem(&cpu_time_ns, &pk);
            if (!ct) {
                /* if no cpu time consumed for this pid, register it with zero */
                struct cpu_time_consumed init = {};
                bpf_map_update_elem(&cpu_time_ns, &pk, &init, BPF_ANY);
                ct = bpf_map_lookup_elem(&cpu_time_ns, &pk);
            }
            /* add the delta time to the cpu time consumed for this pid */
            if (ct)
                ct->ns += delta;
            /* delete the start time for this {cpu, pid} */
            bpf_map_delete_elem(&cpu_pid_start, &cpu_pid_prev);
        }
    }

    /* for the next process, register the start time of the {cpu, pid} key */
    if (ctx->next_pid > 0) {
        struct cpu_pid_key cpu_pid_next = { .cpu = cpu, .pid = ctx->next_pid };
        struct pid_start_time st2 = { .start_ns = now };
        bpf_map_update_elem(&cpu_pid_start, &cpu_pid_next, &st2, BPF_ANY);
    }

    return 0;
}
