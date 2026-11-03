// Native, out-of-process memory sampling on Linux, with the same CSV columns as
// memory_sampler_darwin.c. Build separately; files under testdata are not
// linked into the Go cache.
//
// Columns: wall-clock nanoseconds, VmRSS, RssAnon, and the running maximum of
// RssAnon, all in bytes from /proc/PID/status. RssAnon (private anonymous
// memory, mostly the Go heap) stands in for the macOS physical footprint. Linux
// keeps no lifetime peak of RssAnon, so the fourth column is the peak this
// sampler observed: like the Darwin column it never decreases, but it can miss
// a spike shorter than the sampling interval. The process's true peak RSS is
// VmHWM, which the Go side records as the historical peak RSS through
// getrusage. Compare modes within one platform only.
#include <inttypes.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>
#include <unistd.h>

static volatile sig_atomic_t stopped;

static void stop_sampling(int signal_number) {
    (void)signal_number;
    stopped = 1;
}

static int64_t nanoseconds(clockid_t clock_id) {
    struct timespec value;
    if (clock_gettime(clock_id, &value) != 0) {
        perror("clock_gettime");
        exit(2);
    }
    return (int64_t)value.tv_sec * 1000000000 + value.tv_nsec;
}

int main(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "usage: memory_sampler PID\n");
        return 2;
    }
    long pid = strtol(argv[1], NULL, 10);
    if (pid <= 0) {
        return 2;
    }
    char path[64];
    snprintf(path, sizeof(path), "/proc/%ld/status", pid);
    signal(SIGTERM, stop_sampling);
    signal(SIGINT, stop_sampling);
    setvbuf(stdout, NULL, _IOLBF, 0);
    int64_t start = nanoseconds(CLOCK_MONOTONIC);
    uint64_t peak = 0;
    char line[256];
    while (!stopped) {
        FILE *status = fopen(path, "r");
        if (status == NULL) {
            perror(path);
            return 2;
        }
        uint64_t rss = 0, anon = 0, kib;
        while (fgets(line, sizeof(line), status) != NULL) {
            if (sscanf(line, "VmRSS: %" SCNu64 " kB", &kib) == 1) {
                rss = kib * 1024;
            } else if (sscanf(line, "RssAnon: %" SCNu64 " kB", &kib) == 1) {
                anon = kib * 1024;
            }
        }
        fclose(status);
        if (rss == 0 || anon == 0) {
            fprintf(stderr, "%s: missing VmRSS or RssAnon\n", path);
            return 2;
        }
        if (anon > peak) {
            peak = anon;
        }
        printf("%" PRId64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n",
               nanoseconds(CLOCK_REALTIME), rss, anon, peak);
        usleep(nanoseconds(CLOCK_MONOTONIC) - start < 15000000000LL ? 50000 : 500000);
    }
    return 0;
}
