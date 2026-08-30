//go:build ignore

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char LICENSE[] SEC("license") = "GPL";

/* TGID of the process to monitor. 0 means system-wide monitoring. */
volatile __u32 target_tgid = 0;

/* code logic:
 * - when a process is scheduled out, calculate the delta time and add it to the cpu time consumed for this pid
 * - when a process is scheduled in, register the start time for this pid
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

/* create a map to store the CPU time consumed by each pid: map["pid"] = cpu_time_consumed */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, struct pid_key);
    __type(value, struct cpu_time_consumed);
    __uint(max_entries, 10240);
} cpu_time_ns SEC(".maps");

/* create a map to store the start time of a specific pid: map["pid"] = pid_start_time */
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __type(key, struct pid_key);
    __type(value, struct pid_start_time);
    __uint(max_entries, 10240);
} cpu_pid_start SEC(".maps");

SEC("tracepoint/sched/sched_switch")
int handle_sched_switch(struct trace_event_raw_sched_switch *ctx)
{
    /* get current timestamp in nanoseconds */
    __u64 now = bpf_ktime_get_ns();

    /* if previous process is not idle */
    if (ctx->prev_pid > 0) {
        /* get the previous (still running) pid key */
        struct pid_key pid_prev = { .pid = ctx->prev_pid };
        /* get the registered start time for this pid */
        struct pid_start_time *st = bpf_map_lookup_elem(&cpu_pid_start, &pid_prev);
        /* if this pid is found in the map */
        if (st) {
            /* calculate the delta time */
            __u64 delta = (now > st->start_ns) ? (now - st->start_ns) : 0;
            __u32 pid_to_monitor;
            /* if a TGID is specified, check if the current process belongs to it */
            if (target_tgid != 0) {
                /* get the current process TGID */
                __u64 cur_pid_tgid = bpf_get_current_pid_tgid();
                __u32 cur_tgid = cur_pid_tgid >> 32;
                if (cur_tgid != target_tgid) {
                    /* if the current process does not belong to the tracked TGID, clean up the start time and skip */
                    bpf_map_delete_elem(&cpu_pid_start, &pid_prev);
                    goto next;
                }
                pid_to_monitor = target_tgid;
            } else {
                /* if no TGID is specified, we monitor all processes */
                pid_to_monitor = ctx->prev_pid;
            }
            /* get the registered cpu time consumed for this pid */
            struct pid_key pk = { .pid = pid_to_monitor };
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
            /* delete the start time for this pid */
            bpf_map_delete_elem(&cpu_pid_start, &pid_prev);
        }
    }

next:
    /* for the next process, register the start time of the pid key */
    if (ctx->next_pid > 0) {
        struct pid_key pid_next = { .pid = ctx->next_pid };
        struct pid_start_time st2 = { .start_ns = now };
        bpf_map_update_elem(&cpu_pid_start, &pid_next, &st2, BPF_ANY);
    }

    return 0;
}
