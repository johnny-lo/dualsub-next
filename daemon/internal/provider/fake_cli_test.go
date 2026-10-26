package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// fakeCLIEnv selects the fake behaviour when the test binary re-executes
// itself as a CLI (`codex` or `agy`, see TestMain).
const fakeCLIEnv = "DUALSUB_FAKE_CLI"

// fakeArgsFileEnv, when set, makes the fake record what it was launched with
// (argv, working directory, stdin) as JSON at that path.
const fakeArgsFileEnv = "DUALSUB_FAKE_ARGS_FILE"

// fakeFixtureEnv points the fake at a fixture file to print (absolute path).
const fakeFixtureEnv = "DUALSUB_FAKE_FIXTURE"

// TestMain lets the test binary double as a portable fake CLI: when
// fakeCLIEnv is set it behaves like the CLI mode named there instead of
// running tests. Portable means it works on Windows too, unlike shell scripts.
func TestMain(m *testing.M) {
	mode := os.Getenv(fakeCLIEnv)
	if mode == "" {
		os.Exit(m.Run())
	}
	os.Exit(runFakeCLI(mode, os.Args[1:]))
}

// fakeLaunch is what the fake records about its own launch.
type fakeLaunch struct {
	Args       []string `json:"args"`
	Cwd        string   `json:"cwd"`
	CwdEntries int      `json:"cwd_entries"`
	Stdin      string   `json:"stdin"`
}

func runFakeCLI(mode string, args []string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	if path := os.Getenv(fakeArgsFileEnv); path != "" {
		cwd, _ := os.Getwd()
		entries, _ := os.ReadDir(cwd)
		data, _ := json.Marshal(fakeLaunch{Args: args, Cwd: cwd, CwdEntries: len(entries), Stdin: string(stdin)})
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}

	switch mode {
	case "ok", "args":
		// codex exec: the final message goes to the `-o <path>` file.
		out := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				out = args[i+1]
			}
		}
		if err := os.WriteFile(out, []byte("[1] 你好\n[2] 世界\n"), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	case "ratelimit":
		fmt.Fprintln(os.Stderr, "stream error: rate limit reached for gpt-5")
		return 1
	case "codex-models":
		if len(args) != 2 || args[0] != "debug" || args[1] != "models" {
			fmt.Fprintf(os.Stderr, "unexpected args: %q\n", args)
			return 2
		}
		data, err := os.ReadFile(os.Getenv(fakeFixtureEnv))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_, _ = os.Stdout.Write(data)
		return 0
	case "agy-ok":
		fmt.Print(`{"conversation_id":"c1","status":"SUCCESS","response":"[1] 你好\n[2] 世界\n","usage":{}}`)
		return 0
	case "agy-ok-tricky":
		// A successful translation whose text happens to mention failure words.
		fmt.Print(`{"status":"SUCCESS","response":"[1] 速率限制 rate limit 429\n[2] Please sign in is not recognized quota\n"}`)
		return 0
	case "agy-signin", "agy-models-signin":
		fmt.Println("Please sign in to use Antigravity")
		return 0
	case "agy-badmodel":
		fmt.Fprintln(os.Stderr, `model "nope" is not recognized`)
		return 1
	case "agy-ratelimit":
		fmt.Fprintln(os.Stderr, "RESOURCE_EXHAUSTED: quota exceeded (429)")
		return 1
	case "agy-status-error":
		fmt.Print(`{"status":"ERROR","response":"internal"}`)
		return 0
	case "agy-models":
		fmt.Fprintln(os.Stderr, "Fetching available models...")
		fmt.Print("gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\nclaude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\n")
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown fake mode %q\n", mode)
	return 3
}

// fakeCLI returns the path of a fake CLI (this test binary) that behaves
// according to mode.
func fakeCLI(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv(fakeCLIEnv, mode)
	// Under -race every child would otherwise sleep 1s before exiting.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	return os.Args[0]
}

// fakeCodex is fakeCLI under the name the original codex tests use.
func fakeCodex(t *testing.T, mode string) string {
	t.Helper()
	return fakeCLI(t, mode)
}

// recordLaunch makes the fake record its launch and returns a reader for it.
func recordLaunch(t *testing.T) func() (fakeLaunch, bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "launch.json")
	t.Setenv(fakeArgsFileEnv, path)
	return func() (fakeLaunch, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return fakeLaunch{}, false
		}
		var l fakeLaunch
		if err := json.Unmarshal(data, &l); err != nil {
			t.Fatalf("decode launch record: %v", err)
		}
		return l, true
	}
}

// useFixture points the fake at testdata/<name>.
func useFixture(t *testing.T, name string) {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeFixtureEnv, abs)
}

// hasArgPair reports whether args contains flag immediately followed by value.
func hasArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
