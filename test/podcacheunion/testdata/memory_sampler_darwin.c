// Native, out-of-process memory sampling for the transform comparison (macOS).
// Build separately; files under testdata are not linked into the Go cache.
#include <errno.h>
#include <inttypes.h>
#include <libproc.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/resource.h>
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
    pid_t pid = (pid_t)strtol(argv[1], NULL, 10);
    if (pid <= 0) {
        return 2;
    }
    signal(SIGTERM, stop_sampling);
    signal(SIGINT, stop_sampling);
    setvbuf(stdout, NULL, _IOLBF, 0);
    int64_t start = nanoseconds(CLOCK_MONOTONIC);
    while (!stopped) {
        struct rusage_info_v4 usage = {0};
        if (proc_pid_rusage(pid, RUSAGE_INFO_V4, (rusage_info_t *)&usage) != 0) {
            perror("proc_pid_rusage");
            return 2;
        }
        printf("%" PRId64 ",%" PRIu64 ",%" PRIu64 ",%" PRIu64 "\n",
               nanoseconds(CLOCK_REALTIME), usage.ri_resident_size,
               usage.ri_phys_footprint, usage.ri_lifetime_max_phys_footprint);
        usleep(nanoseconds(CLOCK_MONOTONIC) - start < 15000000000LL ? 50000 : 500000);
    }
    return 0;
}
