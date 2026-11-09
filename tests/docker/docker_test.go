// Package docker tests the program's image, built from the project's
// Dockerfile, in real containers, for what unit tests cannot set up: docker
// stop and its grace period, kill -9, a container memory limit, a small disk,
// a disk that fills while the program runs, and a restart that must find the
// saved cache.
//
// It is its own Go module, so the program itself keeps no dependencies, and
// `go test ./...` in the project root does not need Docker. Run it with
// `task test:docker`. Without a running Docker the tests are skipped.
package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

// image is the program's image, built once from the project's Dockerfile for
// all tests, so they test what `docker build` makes, not a binary copied
// into some other image.
const image = "evmgraph:test"

// uid is the user the image runs as (the Dockerfile's USER). A tmpfs is
// mounted for root only unless it is given to this user.
const uid = 10001

const (
	runningLine = "running; press Ctrl+C or send SIGTERM to stop"
	upToDate    = "nothing to save: the dataset is already on disk"
	generating  = "generating addresses (seed" // logged only when it really generates
	mb          = 1 << 20
)

func TestMain(m *testing.M) {
	if !dockerRunning() {
		os.Exit(m.Run()) // every test skips
	}
	// The docker command, not the Docker API that testcontainers would use:
	// the Dockerfile uses BuildKit's platform arguments, and the command
	// shares BuildKit's cache with the person's own `docker build`, so this
	// takes seconds when nothing changed.
	build := exec.Command("docker", "build", "--quiet", "--tag", image, "../..")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the image from the project's Dockerfile: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	// Remove the tag; the layers stay in the build cache for the next run.
	exec.Command("docker", "image", "rm", image).Run()
	os.Exit(code)
}

// dockerRunning reports whether a Docker daemon answers.
func dockerRunning() (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false // testcontainers panics when it finds no Docker at all
		}
	}()
	p, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false
	}
	defer p.Close()
	return p.Health(context.Background()) == nil
}

// start runs `evmgraph run -show 0 -data /data` and then args in a new
// container of the image, waits for waitFor (nil: does not wait), and removes
// the container, with its volumes, when the test ends. hostConfig sets limits
// and mounts; without a mount, /data is a new anonymous volume (the
// Dockerfile's VOLUME), a real disk.
func start(t *testing.T, args []string, waitFor wait.Strategy, hostConfig func(*container.HostConfig)) testcontainers.Container {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithCmd(append([]string{"run", "-show", "0", "-data", "/data"}, args...)...),
	}
	if waitFor != nil {
		opts = append(opts, testcontainers.WithWaitStrategy(waitFor))
	}
	if hostConfig != nil {
		opts = append(opts, testcontainers.WithHostConfigModifier(hostConfig))
	}
	c, err := testcontainers.Run(context.Background(), image, opts...)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// bind mounts a host folder as /data, so a later container, and the test,
// see what was saved.
func bind(dir string) func(*container.HostConfig) {
	return func(hc *container.HostConfig) { hc.Binds = []string{dir + ":/data"} }
}

// smallDisk gives the container a /data of size bytes, held in memory and
// owned by the image's user.
func smallDisk(size int) func(*container.HostConfig) {
	return func(hc *container.HostConfig) {
		hc.Tmpfs = map[string]string{"/data": fmt.Sprintf("size=%d,uid=%d,gid=%d", size, uid, uid)}
	}
}

// memoryLimit is docker run -m bytes --memory-swap bytes: no swap, so the
// limit is the limit.
func memoryLimit(bytes int64) func(*container.HostConfig) {
	return func(hc *container.HostConfig) { hc.Memory, hc.MemorySwap = bytes, bytes }
}

// stop sends SIGTERM, waits up to grace for the program to exit, then lets
// Docker kill it, exactly as `docker stop -t` does. It returns the exit state.
func stop(t *testing.T, c testcontainers.Container, grace time.Duration) *container.State {
	t.Helper()
	if err := c.Stop(context.Background(), &grace); err != nil {
		t.Fatal(err)
	}
	return state(t, c)
}

// kill sends SIGKILL, as `docker kill` does and as docker stop does when the
// grace period ends, and returns the exit state once the container stopped.
func kill(t *testing.T, c testcontainers.Container) *container.State {
	t.Helper()
	ctx := context.Background()
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if _, err := cli.ContainerKill(ctx, c.GetContainerID(), client.ContainerKillOptions{Signal: "KILL"}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if st := state(t, c); !st.Running {
			return st
		}
	}
	t.Fatal("the container still runs 30 s after SIGKILL")
	return nil
}

func state(t *testing.T, c testcontainers.Container) *container.State {
	t.Helper()
	st, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func logs(t *testing.T, c testcontainers.Container) string {
	t.Helper()
	r, err := c.Logs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// waitForLog waits until the program logs line (true) or the container stops
// without it (false). It polls the log rather than sleeping a guessed time,
// so a test acts on a step within about 50 ms of its start.
func waitForLog(t *testing.T, c testcontainers.Container, line string, timeout time.Duration) bool {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if strings.Contains(logs(t, c), line) {
			return true
		}
		if !state(t, c).Running {
			return strings.Contains(logs(t, c), line)
		}
	}
	t.Fatalf("no %q after %v:\n%s", line, timeout, lastLines(logs(t, c)))
	return false
}

// run runs cmd in the container as the image's user and returns its exit
// code and output (stdout and stderr).
func run(t *testing.T, c testcontainers.Container, cmd ...string) (int, string) {
	t.Helper()
	code, r, err := c.Exec(context.Background(), cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return code, string(b)
}

func mustContain(t *testing.T, logs string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(logs, w) {
			t.Fatalf("the log does not contain %q:\n%s", w, lastLines(logs))
		}
	}
}

func mustNotContain(t *testing.T, logs string, unwanted string) {
	t.Helper()
	if strings.Contains(logs, unwanted) {
		t.Fatalf("the log contains %q:\n%s", unwanted, lastLines(logs))
	}
}

// lastLines keeps error output readable: progress lines are long.
func lastLines(s string) string {
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-12):], "\n")
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// saved reads the cache's manifest (decision 009) and returns the address
// count it records and its dataset folder.
func saved(t *testing.T, dir string) (count int, folder string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest"))
	if err != nil {
		t.Fatalf("no manifest: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "addresses "); ok {
			fmt.Sscan(v, &count)
		}
		if v, ok := strings.CutPrefix(line, "folder "); ok {
			folder = filepath.Join(dir, v)
		}
	}
	return count, folder
}

// mustHaveSaved checks that the cache's current dataset holds n addresses,
// with files of the right sizes: 20 bytes per address, 16 per balance, and
// 4 per address plus 4 for the graph's offsets.
func mustHaveSaved(t *testing.T, dir string, n int) {
	t.Helper()
	count, folder := saved(t, dir)
	if count != n {
		t.Fatalf("the manifest records %d addresses, want %d", count, n)
	}
	if a, b := fileSize(t, filepath.Join(folder, "addresses.bin")), fileSize(t, filepath.Join(folder, "balances.bin")); a != int64(n)*20 || b != int64(n)*16 {
		t.Fatalf("addresses.bin is %d bytes and balances.bin %d, want %d and %d", a, b, n*20, n*16)
	}
	if o := fileSize(t, filepath.Join(folder, "offsets.bin")); o != int64(n+1)*4 {
		t.Fatalf("offsets.bin is %d bytes, want %d", o, (n+1)*4)
	}
}

// The dataset is saved before the program says it is running, so it is on
// disk while the program waits, and docker stop (SIGTERM) finds nothing to
// write: the program exits 0 at once. A new container on the same folder
// loads the cache instead of generating it.
func TestDockerStopAndRestartLoadsTheCache(t *testing.T) {
	dir := t.TempDir()
	first := start(t, []string{"-n", "1000000"}, wait.ForLog(runningLine), bind(dir))
	mustContain(t, logs(t, first), "saved 1000000 addresses and balances")
	mustHaveSaved(t, dir, 1_000_000) // before any signal
	began := time.Now()
	st := stop(t, first, 10*time.Second)
	took := time.Since(began)
	out := logs(t, first)
	if st.ExitCode != 0 {
		t.Fatalf("exit code %d, want 0:\n%s", st.ExitCode, lastLines(out))
	}
	mustContain(t, out, upToDate)
	if took > 2*time.Second {
		t.Fatalf("docker stop took %v with nothing to save", took.Round(10*time.Millisecond))
	}
	t.Logf("exited %v after SIGTERM", took.Round(10*time.Millisecond))

	second := start(t, []string{"-n", "1000000"}, wait.ForLog(runningLine), bind(dir))
	st = stop(t, second, 10*time.Second)
	out = logs(t, second)
	if st.ExitCode != 0 {
		t.Fatalf("restart: exit code %d, want 0:\n%s", st.ExitCode, lastLines(out))
	}
	mustContain(t, out, "loaded 1000000 addresses and balances", upToDate)
	mustNotContain(t, out, generating)
}

// On a small disk the program refuses a dataset that would not fit, before
// generating anything; one that fits runs normally.
func TestSmallDisk(t *testing.T) {
	t.Run("too large is refused up front", func(t *testing.T) {
		// 5M addresses with balances and connections need 400 MB; the disk is 50 MB.
		c := start(t, []string{"-n", "5000000"}, wait.ForExit(), smallDisk(50*mb))
		out := logs(t, c)
		if st := state(t, c); st.ExitCode != 1 {
			t.Fatalf("exit code %d, want 1:\n%s", st.ExitCode, lastLines(out))
		}
		mustContain(t, out, "cannot save 5000000 addresses", "kept free for the system")
		mustNotContain(t, out, generating)
	})
	t.Run("fits and runs", func(t *testing.T) {
		// 500k addresses with balances and connections need 40 MB of the 50 MB disk.
		c := start(t, []string{"-n", "500000"}, wait.ForLog(runningLine), smallDisk(50*mb))
		mustContain(t, logs(t, c), "saved 500000 addresses and balances")
		if st := stop(t, c, 10*time.Second); st.ExitCode != 0 {
			t.Fatalf("exit code %d, want 0:\n%s", st.ExitCode, lastLines(logs(t, c)))
		}
	})
}

// firstAddress is the first address the program printed (-show 1).
var firstAddress = regexp.MustCompile(`(?m)^(0x[0-9a-fA-F]{40}) [0-9]+ wei$`)

// The dataset is saved before the program runs, so a disk that fills up
// afterwards cannot harm it. What still needs space is folding balance
// updates (decision 014) in at shutdown. On a full disk the program checks
// the space first and refuses: it says so and exits 1, and the update stays
// in its log, which lookups read, until a start with room folds it in.
func TestDiskFillingUpWhileRunning(t *testing.T) {
	// 1M addresses with balances and connections (80 MB) fit on a 100 MB disk.
	c := start(t, []string{"-n", "1000000", "-show", "1"}, wait.ForLog(runningLine), smallDisk(100*mb))
	m := firstAddress.FindStringSubmatch(logs(t, c))
	if m == nil {
		t.Fatalf("no address printed:\n%s", lastLines(logs(t, c)))
	}
	addr := m[1]
	if code, out := run(t, c, "evmgraph", "update", "-data", "/data", "balance", addr, "123456789"); code != 0 {
		t.Fatalf("update: exit %d: %s", code, out)
	}
	// Something else then takes all the space that is left; dd stops when
	// the disk is full.
	if code, out := run(t, c, "dd", "if=/dev/zero", "of=/data/other-program", "bs=1M"); !strings.Contains(out, "No space left on device") {
		t.Fatalf("filling the disk: exit %d: %s", code, out)
	}
	if code, out := run(t, c, "evmgraph", "balance", "-data", "/data", addr); code != 0 || !strings.Contains(out, "(123456789 wei)") {
		t.Fatalf("balance on a full disk: exit %d: %s", code, out)
	}

	st := stop(t, c, 10*time.Second)
	log := logs(t, c)
	if st.ExitCode != 1 {
		t.Fatalf("exit code %d, want 1:\n%s", st.ExitCode, lastLines(log))
	}
	mustContain(t, log, "the pending updates (1 balance update) stay in their logs, to be folded in at the next start")
	mustNotContain(t, log, "folding 1 balance updates")
}

// kill -9 while the program runs (docker kill, or a docker stop whose grace
// period ran out) loses nothing: the dataset was saved before the program
// began to wait, and the next start loads it.
func TestKillWhileRunningLosesNothing(t *testing.T) {
	dir := t.TempDir()
	first := start(t, []string{"-n", "1000000"}, wait.ForLog(runningLine), bind(dir))
	if st := kill(t, first); st.ExitCode != 137 {
		t.Fatalf("exit code %d after SIGKILL, want 137:\n%s", st.ExitCode, lastLines(logs(t, first)))
	}
	mustHaveSaved(t, dir, 1_000_000)

	again := start(t, []string{"-n", "1000000"}, wait.ForLog(runningLine), bind(dir))
	out := logs(t, again)
	mustContain(t, out, "loaded 1000000 addresses and balances")
	mustNotContain(t, out, generating)
	if st := stop(t, again, 10*time.Second); st.ExitCode != 0 {
		t.Fatalf("restart: exit code %d, want 0:\n%s", st.ExitCode, lastLines(logs(t, again)))
	}
}

// kill -9 in the middle of a save leaves the previous dataset current and
// whole (the manifest is written last, decision 009), and the next start
// removes the half-written dataset folder.
func TestKillDuringSaveKeepsPreviousDataset(t *testing.T) {
	dir := t.TempDir()
	first := start(t, []string{"-n", "1000000"}, wait.ForLog(runningLine), bind(dir))
	if st := stop(t, first, 10*time.Second); st.ExitCode != 0 {
		t.Fatalf("first run: exit code %d:\n%s", st.ExitCode, lastLines(logs(t, first)))
	}
	_, previous := saved(t, dir)

	// Growing to 20M addresses generates the dataset again (decision 012)
	// and saves 1.5 GB, which takes seconds: it is killed as soon as the
	// save begins.
	big := start(t, []string{"-n", "20000000"}, nil, bind(dir))
	if !waitForLog(t, big, "saving addresses and balances", 5*time.Minute) {
		t.Fatalf("stopped before saving:\n%s", lastLines(logs(t, big)))
	}
	if st := kill(t, big); st.ExitCode != 137 {
		t.Fatalf("exit code %d after SIGKILL, want 137:\n%s", st.ExitCode, lastLines(logs(t, big)))
	}
	mustNotContain(t, logs(t, big), "saved 20000000")
	mustHaveSaved(t, dir, 1_000_000)
	if _, folder := saved(t, dir); folder != previous {
		t.Fatalf("the manifest names %s, want the previous %s", folder, previous)
	}
	if folders, _ := filepath.Glob(filepath.Join(dir, "dataset-*")); len(folders) != 2 {
		t.Fatalf("dataset folders after the kill: %v, want the previous one and the half-written one", folders)
	}

	again := start(t, []string{"-n", "1000000"}, wait.ForLog(runningLine), bind(dir))
	mustContain(t, logs(t, again), "loaded 1000000 addresses and balances", "a dataset folder no manifest names (an unfinished save")
	stop(t, again, 10*time.Second)
	if folders, _ := filepath.Glob(filepath.Join(dir, "dataset-*")); len(folders) != 1 || folders[0] != previous {
		t.Fatalf("dataset folders after a restart: %v, want only %s", folders, previous)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*", "*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary files left after a restart: %v", leftovers)
	}
}

// Known issue 6 (security review, 8 November 2026), fixed 9 November: the
// memory check read the computer's memory, not the container's limit, so the
// limit killed the program. It now reads the limit (cgroup). Under
// docker run -m 512m the default 20M addresses (1.8 GB) are refused up
// front, with a message that names the container's limit.
func TestMemoryLimitIsRespected(t *testing.T) {
	c := start(t, nil, wait.ForExit(), memoryLimit(512*mb))
	st, out := state(t, c), logs(t, c)
	if st.OOMKilled || st.ExitCode == 137 {
		t.Fatalf("issue 6: the container's 512 MiB memory limit killed the program (exit %d); "+
			"the memory check did not see the limit:\n%s", st.ExitCode, lastLines(out))
	}
	if st.ExitCode != 1 {
		t.Fatalf("exit code %d, want 1:\n%s", st.ExitCode, lastLines(out))
	}
	mustContain(t, out, "cannot generate 20000000 addresses in this container", "the container's memory limit is 536.9 MB")
	mustNotContain(t, out, generating)
}

// Known issue (9 November 2026): an -n the memory check allows must also run
// and save under the limit. Under 512 MiB the check allows up to 5.14M
// addresses (473 MB of the 537 MB). 5.1M fit while they are generated, but
// the files the save writes wait in the page cache, which counts against the
// limit, and the kernel does not write them back in time: the program is
// killed while writing neighbors.bin (204 MB). It passes once the save syncs
// its files every few tens of MB as it writes them, not only at the end.
// /data is an anonymous volume, a real disk: a tmpfs holds the files in
// memory anyway, and a Docker Desktop bind mount did not show the problem.
func TestRunsCloseToTheMemoryLimit(t *testing.T) {
	c := start(t, []string{"-n", "5100000"}, nil, memoryLimit(512*mb))
	if !waitForLog(t, c, runningLine, 2*time.Minute) {
		st := state(t, c)
		t.Fatalf("exit code %d, killed for memory: %v; the memory check allowed 5.1M addresses under 512 MiB:\n%s",
			st.ExitCode, st.OOMKilled, lastLines(logs(t, c)))
	}
	mustContain(t, logs(t, c), "saved 5100000 addresses and balances")
	if st := stop(t, c, 10*time.Second); st.ExitCode != 0 || st.OOMKilled {
		t.Fatalf("exit code %d, killed for memory: %v:\n%s", st.ExitCode, st.OOMKilled, lastLines(logs(t, c)))
	}
}

// Known issue 7 (security review, 8 November 2026): docker stop kills the
// program 10 seconds after SIGTERM by default. At the default size (20M
// addresses, decision 013):
//
//   - while it runs, the dataset is already saved, so it exits at once;
//   - during the first start, a signal before the save stops the work at
//     once and saves nothing new; the next start generates the same dataset
//     from the seed. The worst moment used to be right after generating,
//     with sorting, the graph and the save still to do (9.6 to 10.4 s at
//     20M). It must fit in 8 s, so a slower machine has room inside 10 s.
//
// The stop is sent with a long grace period and the time measured, so the
// test reports how long the program needs rather than racing Docker's kill.
func TestDefaultDatasetWithinDockerStopGracePeriod(t *testing.T) {
	const docker, budget = 10 * time.Second, 8 * time.Second
	t.Run("while running", func(t *testing.T) {
		dir := t.TempDir()
		c := start(t, nil, wait.ForLog(runningLine).WithStartupTimeout(5*time.Minute), bind(dir))
		began := time.Now()
		st := stop(t, c, time.Minute)
		took := time.Since(began)
		if st.ExitCode != 0 {
			t.Fatalf("exit code %d, want 0:\n%s", st.ExitCode, lastLines(logs(t, c)))
		}
		mustContain(t, logs(t, c), upToDate)
		mustHaveSaved(t, dir, 20_000_000)
		if took > budget {
			t.Fatalf("issue 7: exited %v after SIGTERM; docker stop kills after %v", took.Round(10*time.Millisecond), docker)
		}
		t.Logf("exited %v after SIGTERM", took.Round(10*time.Millisecond))
	})
	t.Run("during the first start", func(t *testing.T) {
		dir := t.TempDir()
		c := start(t, nil, nil, bind(dir))
		if !waitForLog(t, c, "generated 20000000 addresses", 5*time.Minute) {
			t.Fatalf("stopped before generating:\n%s", lastLines(logs(t, c)))
		}
		began := time.Now()
		st := stop(t, c, time.Minute)
		took := time.Since(began)
		if st.ExitCode != 0 {
			t.Fatalf("exit code %d, want 0:\n%s", st.ExitCode, lastLines(logs(t, c)))
		}
		mustContain(t, logs(t, c), "nothing new was saved")
		if _, err := os.Stat(filepath.Join(dir, "manifest")); !os.IsNotExist(err) {
			t.Fatalf("a dataset was committed although the signal came before the save: %v", err)
		}
		if took > budget {
			t.Fatalf("issue 7: stopped right after generating, the program took %v to exit; docker stop kills after %v, "+
				"and this test wants %v so slower machines have room:\n%s",
				took.Round(10*time.Millisecond), docker, budget, lastLines(logs(t, c)))
		}
		t.Logf("exited %v after SIGTERM", took.Round(10*time.Millisecond))
	})
}
