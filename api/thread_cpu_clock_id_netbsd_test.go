package api

// threadCPUClockID is the per-thread CPU-time clock threadCPUTime reads.
// golang.org/x/sys/unix has ClockGettime for netbsd but no
// CLOCK_THREAD_CPUTIME_ID constant, so its value is taken from NetBSD's
// sys/sys/time.h ("#define CLOCK_THREAD_CPUTIME_ID 0x20000000"):
// https://github.com/NetBSD/src/blob/trunk/sys/sys/time.h
// Should a kernel reject it, ClockGettime errors and sampleCost falls back
// to wall-clock time, so a wrong value cannot break the tests.
const threadCPUClockID = 0x20000000
