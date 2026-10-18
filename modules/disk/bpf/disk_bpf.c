//go:build ignore

/*
Disk I/O accounting using eBPF.

- in system mode (target_tgid == 0): we account all the bytes completed by the
  disks on the block_rq_complete tracepoint.

- in process mode (target_tgid != 0): we have different cases:
    - reads : we account the read bytes when the (submit_bio) function is called.
	- writes: here it's somehow more complex :) :
		- direct writes are accounted when the (submit_bio) is called.
		- buffered writes are accounted when the process dirties a page cache
		  (writeback_dirty_folio), the later flush by a kworker is not accounted again.
		- dirty page cache cancelled before being flushed are counted
		  as cancelled writes (folio_account_cleaned), to rectify the counted bytes.

The counters are keyed by the dev_t of the disk (e.g nvme0n1), so I/O on
any partition of that disk is accounted to that disk.
*/

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>

#define REQ_OP_MASK 0xff // request operation mask
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1

/* This is an internal flag to identify cancelled writes */
#define ECOFLOC_OP_CANCELLED_WRITE 0xff

/* default page size 4KB (we can make it configurable if needed) */
#define ECOFLOC_PAGE_SIZE 4096

struct disk_metrics {
	__u64 read_bytes;
	__u64 write_bytes;
	__u64 cancelled_write_bytes;
};

/* TGID of the process to monitor. 0 means system-wide monitoring */
volatile __u32 target_tgid = 0;

/* Map of cumulative metrics keyed by the whole disk id dev_t */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 256);
	__type(key, __u32); //whole disk device id
	__type(value, struct disk_metrics);
} disk_map SEC(".maps");




/* returns 1 if the current task belongs to the monitored process */
static __always_inline int is_target_task(void)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	return (__u32)(pid_tgid >> 32) == target_tgid;
}

/* returns the dev_t of the whole disk a block device (disk or partition) belongs to,
   or 0 if it can't be found */
static __always_inline __u32 get_disk_dev(struct block_device *bdev)
{
	struct gendisk *disk;
	struct block_device *part0;

	if (!bdev)
		return 0;

	disk = bdev->bd_disk;
	if (!disk)
		return 0;
  
	/*part0 holds the dev_t of the whole disk */
	part0 = disk->part0;
	if (!part0)
		return 0;

	return (__u32)part0->bd_dev;
}

static __always_inline void account_bytes(__u32 dev, __u8 op, __u64 bytes)
{
	struct disk_metrics *m;
	struct disk_metrics zero = {};

	/* skip if empty bytes or unknown disk */
	if (bytes == 0 || dev == 0)
		return;

	/*we only account read, write and cancelled write operations*/
	if (op != REQ_OP_READ && op != REQ_OP_WRITE && op != ECOFLOC_OP_CANCELLED_WRITE)
		return;

	/*we check if this block device id is already in the map 
	if not we create an empty entry for it
	*/
	m = bpf_map_lookup_elem(&disk_map, &dev);
	if (!m) {
		/*create an empty entry for this block device*/
		bpf_map_update_elem(&disk_map, &dev, &zero, BPF_NOEXIST);
		m = bpf_map_lookup_elem(&disk_map, &dev);
		/*if we still can't find it, return*/
		if (!m)
			return;
	}

	/* update the appropriate counter */
	if (op == REQ_OP_READ)
		__sync_fetch_and_add(&m->read_bytes, bytes);
	else if (op == REQ_OP_WRITE)
		__sync_fetch_and_add(&m->write_bytes, bytes);
	else
		__sync_fetch_and_add(&m->cancelled_write_bytes, bytes);
}

/* returns the dev_t of the whole disk holding a page cache folio:
   mapping -> file inode -> filesystem -> block device.
   Filesystems without block device (tmpfs, procfs...) return 0 */
static __always_inline __u32 get_mapping_disk_dev(struct address_space *mapping)
{
	struct inode *host;
	struct super_block *sb;

	if (!mapping)
		return 0;
	host = mapping->host;
	if (!host)
		return 0;
	sb = host->i_sb;
	if (!sb)
		return 0;
	return get_disk_dev(sb->s_bdev);
}

/* returns the size in bytes of a folio: a folio can be a single page
   or a group of pages (large folio), its number of pages is stored in _folio_nr_pages */
static __always_inline __u64 get_folio_bytes(struct folio *folio)
{
	__u64 nr_pages = 1;

	/*if PG_head set, the folio contains multiple pages */
	if ((folio->flags >> PG_head) & 1)
		nr_pages = folio->_folio_nr_pages;
	return nr_pages * ECOFLOC_PAGE_SIZE;
}

/* returns 1 if the page is under writeback,
   which means the bio page cache data was already accounted when dirtied */
static __always_inline int page_under_writeback(struct page *page)
{
	unsigned long head;
	unsigned long flags;

	if (!page)
		return 0;

	/* sometimes multiple pages are grouped together in a compound page.
	if this is the case, get the head page. for that
	we read the compound_head field and check if the least significant bit is set 
	- if not set: the page is the head page 
	- if set: the page is not the head page (it's a tail page), so we need to get the head page (by subtracting 1)
	*/
	head = BPF_CORE_READ(page, compound_head);
	if (head & 1)
		page = (struct page *)(head - 1);

	/*flags are stored in the head page*/
	flags = BPF_CORE_READ(page, flags);

	/*check if the page is under writeback*/
	return (flags >> PG_writeback) & 1;
}

/* System mode monitoring: account all the bytes completed by the disks */
SEC("tp_btf/block_rq_complete")
int BPF_PROG(handle_block_request_complete, struct request *rq, blk_status_t error, unsigned int nr_bytes)
{
	struct bio *bio;
	__u32 dev;
	__u8 op;

	(void)error;

	/* this handler is only used in system mode */
	if (target_tgid != 0)
		return 0;

	/* Skip if request pointer is null */
	if (!rq)
		return 0;

	/* From the request struct we get the block IO struct
	   which holds informations about the block IO operation
	*/
	bio = rq->bio;
	if (!bio)
		return 0;

	/* Identify the associated whole disk and operation type */
	dev = get_disk_dev(bio->bi_bdev);
	op = (__u8)(rq->cmd_flags & REQ_OP_MASK);

	/* Once we have the associated disk, the operation and the nbr of bytes
	   we update the cumulative bytes counters
	*/
	account_bytes(dev, op, (__u64)nr_bytes);
	return 0;
}

/* This function handles reads and "direct" writes submitted by the monitored process */
SEC("fentry/submit_bio")
int BPF_PROG(handle_submit_bio, struct bio *bio)
{
	__u32 dev;
	__u8 op;

	/* Continue only if process monitoring 
	   and the actual task belongs to the monitored process */
	if (target_tgid == 0 || !is_target_task())
		return 0;

	if (!bio)
		return 0;

	op = (__u8)(bio->bi_opf & REQ_OP_MASK);

	/* for write op: the cached writes are already counted when the pages were dirtied (in handle_dirty_folio)
	   we only keep direct I/O writes, so check if the page is under writeback */
	if (op == REQ_OP_WRITE) {
		if (bio->bi_vcnt == 0 || !bio->bi_io_vec)
			return 0;
		if (page_under_writeback(bio->bi_io_vec->bv_page))
			return 0;
	}

	/*get the disk device and account the bytes*/
	dev = get_disk_dev(bio->bi_bdev);
	account_bytes(dev, op, (__u64)bio->bi_iter.bi_size);
	return 0;
}

/* This function handles buffered writes. writeback_dirty_folio is emitted when a clean
   page cache becomes dirty, in the context of the task that dirtied it.
   */
SEC("tp_btf/writeback_dirty_folio")
int BPF_PROG(handle_dirty_folio, struct folio *folio, struct address_space *mapping)
{
	__u64 bytes;
	__u32 dev;

	/* Continue only if process monitoring 
	   and the actual task belongs to the monitored process */
	if (target_tgid == 0 || !is_target_task())
		return 0;

	if (!folio)
		return 0;

	/* Get the number of the written bytes*/
	bytes = get_folio_bytes(folio);
	/*get the disk device from the folio mapping*/
	dev = get_mapping_disk_dev(mapping);
	account_bytes(dev, REQ_OP_WRITE, bytes);
	return 0;
}

/* This function handles "cancelled" buffered writes. folio_account_cleaned is called when a dirty
   page cache folio is dropped without being flushed to disk.
   */
SEC("fentry/folio_account_cleaned")
int BPF_PROG(handle_folio_cleaned, struct folio *folio, struct bdi_writeback *wb)
{
	__u64 bytes;
	__u32 dev;

	(void)wb;

	/* Continue only if process monitoring 
	   and the actual task belongs to the monitored process */
	if (target_tgid == 0 || !is_target_task())
		return 0;

	if (!folio)
		return 0;

	/* Get the number of the cancelled bytes*/
	bytes = get_folio_bytes(folio);

	/*get the disk device from the folio mapping*/
	dev = get_mapping_disk_dev(folio->mapping);
	account_bytes(dev, ECOFLOC_OP_CANCELLED_WRITE, bytes);
	return 0;
}

char LICENSE[] SEC("license") = "GPL";