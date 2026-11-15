package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olddognewflex/sift/internal/contract"
)

const fixturesDir = "testdata/fixtures"

// Totals across all benchmark fixtures. Changing a key changes these on
// purpose; an accidental edit fails here.
const (
	wantDefects = 12
	wantDecoys  = 8
	wantKnown   = 1
)

func loadBenchFixtures(t *testing.T) []Fixture {
	t.Helper()
	fs, err := LoadFixtures(fixturesDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestBenchFixturesLoad(t *testing.T) {
	fs := loadBenchFixtures(t)
	var names []string
	for _, f := range fs {
		names = append(names, f.Name)
	}
	if want := []string{"invoicer", "jobrunner", "ledger"}; !slices.Equal(names, want) {
		t.Fatalf("fixtures: got %v, want %v", names, want)
	}
}

func TestBenchFixturesCoverage(t *testing.T) {
	type tally struct{ defects, decoys, known int }
	byCategory := map[contract.Category]*tally{}
	for _, r := range contract.Reviewers {
		byCategory[r.Category] = &tally{}
	}
	var total tally
	for _, f := range loadBenchFixtures(t) {
		var own tally
		for _, e := range f.Key.Entries {
			c := byCategory[e.Category]
			switch e.Kind {
			case KindDefect:
				c.defects++
				own.defects++
			case KindDecoy:
				c.decoys++
				own.decoys++
			case KindKnown:
				own.known++
			}
		}
		if own.defects < 1 || own.decoys < 1 {
			t.Errorf("%s: needs at least one defect and one decoy, has %d and %d", f.Name, own.defects, own.decoys)
		}
		total.defects += own.defects
		total.decoys += own.decoys
		total.known += own.known
	}
	for _, r := range contract.Reviewers {
		if c := byCategory[r.Category]; c.defects < 1 || c.decoys < 1 {
			t.Errorf("reviewer %s: needs at least one defect and one decoy, has %d and %d", r.Category, c.defects, c.decoys)
		}
	}
	if total.defects != wantDefects || total.decoys != wantDecoys || total.known != wantKnown {
		t.Errorf("totals: got %d defects, %d decoys, %d known; want %d, %d, %d",
			total.defects, total.decoys, total.known, wantDefects, wantDecoys, wantKnown)
	}
}

// copyFixtureRepo writes a fixture's files into a fresh temp dir.
func copyFixtureRepo(t *testing.T, name string) string {
	t.Helper()
	fs, err := LoadFixtures(fixturesDir, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for p, content := range fs[0].Files {
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// needTool skips the test, saying why, when a tool is not on the PATH.
func needTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Logf("%s not on PATH; skipping this fixture check", name)
		t.SkipNow()
	}
}

func runTool(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// The Go fixture is a standalone module with no dependencies; it must
// compile and vet cleanly so findings are about its logic, not its syntax.
func TestBenchFixtureLedgerVets(t *testing.T) {
	needTool(t, "go")
	dir := copyFixtureRepo(t, "ledger")
	runTool(t, dir, []string{"GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local"}, "go", "vet", "./...")
}

func TestBenchFixtureInvoicerCompiles(t *testing.T) {
	needTool(t, "python3")
	dir := copyFixtureRepo(t, "invoicer")
	var files []string
	for _, pattern := range []string{"invoicer/*.py", "tests/*.py"} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no Python files found")
	}
	runTool(t, dir, nil, "python3", append([]string{"-I", "-m", "py_compile"}, files...)...)
}

// Importing the CLI imports every invoicer module, so this also proves
// the package has no import cycle at runtime.
func TestBenchFixtureInvoicerImports(t *testing.T) {
	needTool(t, "python3")
	dir := copyFixtureRepo(t, "invoicer")
	runTool(t, dir, nil, "python3", "-I", "-c", "import sys; sys.path.insert(0, '.'); import invoicer.cli")
}

// jobrunnerAmbient declares just the parts of Express and Node that the
// jobrunner fixture uses, so tsc can check it without npm install. It is
// added to the temp copy only, never to the fixture.
const jobrunnerAmbient = `declare module "express" {
  export interface Request {
    body: any;
    params: Record<string, string>;
    query: Record<string, unknown>;
  }
  export interface Response {
    status(code: number): Response;
    json(body: unknown): Response;
    end(): Response;
  }
  type Handler = (req: Request, res: Response) => void;
  interface Router {
    get(path: string, handler: Handler): Router;
    post(path: string, handler: Handler): Router;
  }
  interface App {
    use(handler: unknown): App;
    listen(port: number, callback?: () => void): { close(): void };
  }
  interface Express {
    (): App;
    json(options?: { limit?: string }): unknown;
    Router(): Router;
  }
  const express: Express;
  export default express;
}
declare module "node:crypto" {
  export function randomUUID(): string;
}
declare const process: {
  env: Record<string, string | undefined>;
  on(event: string, listener: () => void): void;
  exit(code?: number): never;
};
declare const console: { log(...args: unknown[]): void };
declare function setTimeout(callback: () => void, ms: number): number;
declare function setInterval(callback: () => void, ms: number): number;
declare function clearInterval(id: number | undefined): void;
`

func TestBenchFixtureJobrunnerTypechecks(t *testing.T) {
	needTool(t, "tsc")
	dir := copyFixtureRepo(t, "jobrunner")
	if err := os.WriteFile(filepath.Join(dir, "src", "zz-ambient.d.ts"), []byte(jobrunnerAmbient), 0o644); err != nil {
		t.Fatal(err)
	}
	runTool(t, dir, nil, "tsc", "--noEmit", "-p", "tsconfig.json")
}
