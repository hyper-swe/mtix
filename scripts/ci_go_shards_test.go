// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.17.22: explicit CI shards must cover every selected package exactly
// once. These tests run the real guard in private replicas with a fake go list.
package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const shardModule = "github.com/hyper-swe/mtix"

func shardManifests() map[string][]string {
	return map[string][]string{
		"sqlite": {shardModule + "/internal/store/sqlite"},
		"cli":    {shardModule + "/cmd/mtix"},
		"other":  {shardModule},
	}
}

func shardFixture(t *testing.T, packages []string, manifests map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "scripts", "ci-go-shards")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	source, err := os.ReadFile("ci-go-shards.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "ci-go-shards.sh"), source, 0o600))
	for name, entries := range manifests {
		text := strings.Join(entries, "\n")
		if len(entries) > 0 {
			text += "\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0o750))
	shim := "#!/usr/bin/env bash\n[ \"$*\" = 'list ./...' ] || exit 99\ncat \"$SHARD_TEST_LIST\"\nexit \"${SHARD_TEST_EXIT:-0}\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "go"), []byte(shim), 0o750))
	list := strings.Join(packages, "\n")
	if len(packages) > 0 {
		list += "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "packages.txt"), []byte(list), 0o600))
	return root
}

func runShardGuard(root string, args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", "ci-go-shards.sh")}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SHARD_TEST_LIST="+filepath.Join(root, "packages.txt"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestGoShards_UnlistedPackage_Fails(t *testing.T) {
	packages := []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite", shardModule + "/newpackage"}
	root := shardFixture(t, packages, shardManifests())
	out, err := runShardGuard(root, "check")
	require.Error(t, err, "the guard accepted an unlisted package: %s", out)
	require.Contains(t, out, "unlisted packages")
	require.Contains(t, out, shardModule+"/newpackage")
	path := filepath.Join(root, "scripts", "ci-go-shards", "other.txt")
	require.NoError(t, os.WriteFile(path, []byte(shardModule+"\n"+shardModule+"/newpackage\n"), 0o600))
	out, err = runShardGuard(root, "check")
	require.NoError(t, err, out)
}

func TestGoShards_InvalidPartition_Fails(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string][]string)
		want   string
	}{
		{"duplicate within shard", func(m map[string][]string) { m["other"] = append(m["other"], shardModule) }, "duplicate packages"},
		{"duplicate across shards", func(m map[string][]string) { m["other"] = append(m["other"], m["cli"]...) }, "duplicate packages"},
		{"stale package", func(m map[string][]string) { m["other"] = append(m["other"], shardModule+"/removed") }, "stale packages"},
		{"empty shard", func(m map[string][]string) { m["sqlite"] = nil }, "missing or empty manifest"},
		{"missing shard", func(m map[string][]string) { delete(m, "cli") }, "missing or empty manifest"},
		{"blank entry", func(m map[string][]string) { m["other"] = append(m["other"], "") }, "stale packages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifests := shardManifests()
			tc.change(manifests)
			packages := []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite"}
			root := shardFixture(t, packages, manifests)
			out, err := runShardGuard(root, "check")
			require.Error(t, err, out)
			require.Contains(t, out, tc.want)
		})
	}
}

func TestGoShards_InvalidInventory_Fails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		packages []string
		exit     string
	}{
		{"empty inventory", nil, "0"},
		{"go list failure after output", []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite"}, "7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHARD_TEST_EXIT", tc.exit)
			root := shardFixture(t, tc.packages, shardManifests())
			out, err := runShardGuard(root, "check")
			require.Error(t, err, out)
			require.Contains(t, out, "cannot enumerate")
		})
	}
}

func TestGoShards_ValidPartition_ListsSelectedPackages(t *testing.T) {
	manifests := shardManifests()
	packages := []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/store/sqlite", shardModule + "/node_modules/ignored"}
	root := shardFixture(t, packages, manifests)
	for _, shard := range []string{"sqlite", "cli", "other"} {
		t.Run(shard, func(t *testing.T) {
			out, err := runShardGuard(root, "list", shard)
			require.NoError(t, err, out)
			require.Equal(t, strings.Join(manifests[shard], "\n")+"\n", out)
		})
	}
	for _, args := range [][]string{nil, {"list", "unknown"}, {"list"}, {"check", "extra"}} {
		out, err := runShardGuard(root, args...)
		require.Error(t, err, out)
		require.Contains(t, out, "usage:")
	}
}

// MTIX-112.17.27: remote bar package sets and test-level shards.
const (
	shardCmdMtix      = shardModule + "/cmd/mtix"
	shardBarManifests = "remote-bar-packages"
)

// pgDSNRead matches a test file that reads the PostgreSQL DSN variable: a
// constant or variable bound to its name, or a direct Getenv/LookupEnv call.
// Comments and text checks that only mention the name do not match.
var pgDSNRead = regexp.MustCompile(`(?:=\s*|(?:Getenv|LookupEnv)\(\s*)"MTIX_PG_TEST_DSN"`)

// pgConsumerPackages returns the import path of every package under root whose
// *_test.go files read MTIX_PG_TEST_DSN.
func pgConsumerPackages(t *testing.T, root string) []string {
	t.Helper()
	found := map[string]bool{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "web") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // repository file found by the walk
		if err != nil {
			return err
		}
		if pgDSNRead.Match(src) {
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			pkg := shardModule
			if rel != "." {
				pkg += "/" + filepath.ToSlash(rel)
			}
			found[pkg] = true
		}
		return nil
	}))
	return sortedKeys(found)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readSetManifest(t *testing.T, name string) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(shardBarManifests, name+".txt"))
	require.NoError(t, err, "missing package-set manifest %s", name)
	var out []string
	for _, line := range strings.Split(string(src), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func goListPackages(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = ".."
	out, err := cmd.Output()
	require.NoError(t, err)
	var pkgs []string
	for _, p := range strings.Fields(string(out)) {
		if !strings.Contains(p, "node_modules") {
			pkgs = append(pkgs, p)
		}
	}
	return pkgs
}

// partitionProblems reports every way the serial, parallel and sharded sets
// fail to be an exact, disjoint cover of all (cmd/mtix has its own shards) or
// let a PostgreSQL consumer run outside the serial set.
func partitionProblems(all, serial, parallel, sharded, consumers []string) []string {
	var problems []string
	owner := map[string]string{}
	for name, set := range map[string][]string{"serial": serial, "parallel": parallel, "sharded": sharded} {
		for _, p := range set {
			if prev, dup := owner[p]; dup {
				problems = append(problems, "in two sets ("+prev+", "+name+"): "+p)
			}
			owner[p] = name
		}
	}
	listed := map[string]bool{}
	for _, p := range all {
		listed[p] = true
		if p != shardCmdMtix && owner[p] == "" {
			problems = append(problems, "in no set: "+p)
		}
	}
	for p := range owner {
		if !listed[p] {
			problems = append(problems, "stale entry: "+p)
		}
		if p == shardCmdMtix {
			problems = append(problems, "cmd/mtix belongs to its own shards: "+p)
		}
	}
	for _, p := range consumers {
		if p != shardCmdMtix && listed[p] && owner[p] != "serial" {
			problems = append(problems, "PostgreSQL consumer outside the serial set: "+p)
		}
	}
	sort.Strings(problems)
	return problems
}

func TestRemoteBarPackageSets_RepositoryPartition_IsExactAndKeepsPGSerial(t *testing.T) {
	consumers := pgConsumerPackages(t, "..")
	require.Contains(t, consumers, shardModule+"/internal/store/postgres/transport", "scan must find the known consumers")
	require.Contains(t, consumers, shardModule+"/e2e")
	require.Contains(t, consumers, shardModule+"/benchmarks")
	require.NotContains(t, consumers, shardModule+"/scripts", "text checks are not consumers")
	require.NotContains(t, consumers, shardModule+"/internal/docs", "text checks are not consumers")

	problems := partitionProblems(goListPackages(t),
		readSetManifest(t, "serial"), readSetManifest(t, "parallel"), readSetManifest(t, "sharded"), consumers)
	require.Empty(t, problems)
}

func TestRemoteBarPackageSets_Mutations_AreCaught(t *testing.T) {
	pg := shardModule + "/e2e"
	all := []string{shardCmdMtix, pg, shardModule + "/a", shardModule + "/s"}
	serial, parallel, sharded := []string{pg}, []string{shardModule + "/a"}, []string{shardModule + "/s"}
	consumers := []string{shardCmdMtix, pg}
	require.Empty(t, partitionProblems(all, serial, parallel, sharded, consumers), "baseline is clean")

	cases := []struct {
		name                       string
		all, serial, parallel, shd []string
		want                       string
	}{
		{"consumer moved to parallel", all, nil, []string{shardModule + "/a", pg}, sharded, "PostgreSQL consumer outside the serial set: " + pg},
		{"consumer moved to sharded", all, nil, parallel, []string{shardModule + "/s", pg}, "PostgreSQL consumer outside the serial set: " + pg},
		{"package in no set", append(append([]string{}, all...), shardModule+"/new"), serial, parallel, sharded, "in no set: " + shardModule + "/new"},
		{"package in two sets", all, serial, []string{shardModule + "/a", shardModule + "/s"}, sharded, "in two sets"},
		{"stale entry", all, serial, []string{shardModule + "/a", shardModule + "/gone"}, sharded, "stale entry: " + shardModule + "/gone"},
		{"cmd/mtix in a set", all, serial, []string{shardModule + "/a", shardCmdMtix}, sharded, "cmd/mtix belongs to its own shards"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, strings.Join(partitionProblems(tc.all, tc.serial, tc.parallel, tc.shd, consumers), "\n"), tc.want)
		})
	}
}

type shardWorkflowJob struct {
	Steps []struct {
		Name string
		Run  string
	}
	Strategy struct {
		Matrix struct {
			Include []map[string]string
		}
	}
}

func shardWorkflowJobs(t *testing.T) map[string]shardWorkflowJob {
	t.Helper()
	src, err := os.ReadFile("../.github/workflows/remote-bar.yml")
	require.NoError(t, err)
	var wf struct{ Jobs map[string]shardWorkflowJob }
	require.NoError(t, yaml.Unmarshal(src, &wf))
	return wf.Jobs
}

func (j shardWorkflowJob) run(t *testing.T, name string) string {
	t.Helper()
	for _, s := range j.Steps {
		if s.Name == name {
			return s.Run
		}
	}
	require.FailNow(t, "step not found", name)
	return ""
}

func TestRemoteBarWorkflow_PackageSets_RunWithTheRightFlags(t *testing.T) {
	jobs := shardWorkflowJobs(t)

	serial := jobs["bar"].run(t, "Run committed bar")
	require.Contains(t, serial, "make lint-go")
	require.Contains(t, serial, "bash scripts/ci-go-shards.sh bar-list serial")
	require.Contains(t, serial, "-race -count=1 -p 1 -timeout 20m")

	parallel := jobs["bar-parallel"].run(t, "Run committed bar")
	require.Contains(t, parallel, "bash scripts/ci-go-shards.sh bar-list parallel")
	require.Contains(t, parallel, "-race -count=1 -p 4 -timeout 20m")

	for name, job := range jobs {
		for _, s := range job.Steps {
			require.NotContains(t, s.Run, "-skip", "%s must not skip tests", name)
		}
	}
	for _, job := range []string{"bar", "bar-parallel", "bar-shard"} {
		require.Contains(t, jobs[job].run(t, "Verify exact package sets"), "bash scripts/ci-go-shards.sh bar-check", job)
	}
}

func TestRemoteBarWorkflow_ShardMatrix_CoversEveryShardOfEveryShardedPackage(t *testing.T) {
	job := shardWorkflowJobs(t)["bar-shard"]
	shardedPkgs := readSetManifest(t, "sharded")
	require.NotEmpty(t, shardedPkgs)
	counts := map[string]int{}
	seen := map[string]map[int]bool{}
	for _, inc := range job.Strategy.Matrix.Include {
		pkg := inc["pkg"]
		n, err := strconv.Atoi(inc["shards"])
		require.NoError(t, err)
		k, err := strconv.Atoi(inc["shard"])
		require.NoError(t, err)
		require.True(t, k >= 1 && k <= n, "shard %d of %d", k, n)
		if prev, ok := counts[pkg]; ok {
			require.Equal(t, prev, n, "%s shard count differs between matrix entries", pkg)
		}
		counts[pkg] = n
		if seen[pkg] == nil {
			seen[pkg] = map[int]bool{}
		}
		require.False(t, seen[pkg][k], "%s shard %d listed twice", pkg, k)
		seen[pkg][k] = true
	}
	for _, p := range shardedPkgs {
		rel := strings.TrimPrefix(p, shardModule+"/")
		require.Contains(t, counts, rel, "sharded package %s has no matrix entries", rel)
		require.Len(t, seen[rel], counts[rel], "%s must list every shard 1..%d", rel, counts[rel])
	}
	require.Len(t, counts, len(shardedPkgs), "matrix names a package outside the sharded set")
	run := job.run(t, "Run package shard")
	require.Contains(t, run, `bash scripts/ci-go-shards.sh test-pattern "$PKG" "$SHARDS" "$SHARD"`)
	require.Contains(t, run, "-race -count=1 -timeout 20m")
}

func TestRemoteBar_Classify_PackageSetStepFailuresAreBarFailures(t *testing.T) {
	for steps, want := range map[string]string{
		"Run package shard":                           "1",
		"Verify exact package test coverage":          "1",
		"Verify exact package sets":                   "1",
		"Verify snapshot tree|Run package shard":      "3",
		"Run package shard|Verify exact package sets": "1",
	} {
		out, code := bash(t, t.TempDir(), `source "$RB_SCRIPT"; rb_classify failure "$B"`, "B="+steps)
		require.Equal(t, 0, code, out)
		require.Equal(t, want, strings.TrimSpace(out), steps)
	}
}

// testShardFixture replicates the guard with a fake go whose test -list prints
// the given listing and exits with the given status.
func testShardFixture(t *testing.T, listing string, exit string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal", "slow"), 0o750))
	source, err := os.ReadFile("ci-go-shards.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "scripts", "ci-go-shards.sh"), source, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "bin"), 0o750))
	shim := "#!/usr/bin/env bash\n[ \"$1 $2 $3\" = 'test -list .*' ] || exit 99\n[ \"$4\" = ./internal/slow/ ] || exit 98\ncat \"$SHARD_TEST_LIST\"\nexit \"${SHARD_TEST_EXIT:-0}\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "bin", "go"), []byte(shim), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "listing.txt"), []byte(listing), 0o600))
	t.Setenv("SHARD_TEST_EXIT", exit)
	return root
}

func runTestShards(root string, args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{filepath.Join(root, "scripts", "ci-go-shards.sh")}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SHARD_TEST_LIST="+filepath.Join(root, "listing.txt"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func fakeTestListing(n int) ([]string, string) {
	var names []string
	for i := 0; i < n; i++ {
		kind := []string{"Test", "Example", "Fuzz"}[i%3]
		names = append(names, kind+"Case"+strconv.Itoa(i)+"_Name")
	}
	return names, strings.Join(names, "\n") + "\nok  \texample.com/slow\t0.01s\n"
}

func TestTestShards_Patterns_SelectEveryNameExactlyOnce(t *testing.T) {
	for _, shards := range []int{1, 2, 3, 5} {
		t.Run(strconv.Itoa(shards)+" shards", func(t *testing.T) {
			names, listing := fakeTestListing(40)
			root := testShardFixture(t, listing, "0")
			out, err := runTestShards(root, "test-check", "internal/slow", strconv.Itoa(shards))
			require.NoError(t, err, out)
			require.Contains(t, out, "40 tests in "+strconv.Itoa(shards)+" shards, split is exact")
			hits := map[string]int{}
			for k := 1; k <= shards; k++ {
				pat, err := runTestShards(root, "test-pattern", "internal/slow", strconv.Itoa(shards), strconv.Itoa(k))
				require.NoError(t, err, pat)
				re := regexp.MustCompile(strings.TrimSpace(pat))
				for _, n := range names {
					if re.MatchString(n) {
						hits[n]++
					}
				}
			}
			for _, n := range names {
				require.Equal(t, 1, hits[n], n)
			}
		})
	}
}

func TestTestShards_BrokenListing_FailsInsteadOfPassingEmpty(t *testing.T) {
	_, good := fakeTestListing(6)
	for _, tc := range []struct {
		name, listing, exit, want string
		args                      []string
	}{
		{"zero tests exits non-zero", "ok  \texample.com/slow\t0.01s\n", "0", "no tests listed", []string{"test-check", "internal/slow", "2"}},
		{"zero tests for pattern", "no test files\n", "0", "no tests listed", []string{"test-pattern", "internal/slow", "2", "1"}},
		{"go test -list fails", good, "3", "cannot list", []string{"test-check", "internal/slow", "2"}},
		{"unsupported name", good + "TestBad\xc3\xa9\n", "0", "unsupported test name", []string{"test-check", "internal/slow", "2"}},
		{"more shards than tests leaves one empty", "TestOnly\nok  \tx\t0s\n", "0", "empty shard", []string{"test-check", "internal/slow", "2"}},
		{"shard out of range", good, "0", "usage:", []string{"test-pattern", "internal/slow", "2", "3"}},
		{"path traversal", good, "0", "usage:", []string{"test-check", "../x", "2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testShardFixture(t, tc.listing, tc.exit)
			out, err := runTestShards(root, tc.args...)
			require.Error(t, err, out)
			require.Contains(t, out, tc.want)
		})
	}
}

// Mutate the real script so that the hashed name set drops its last test, or
// puts one test in two shards, and require the exact-split check to name it.
func TestTestShards_DroppedOrDuplicatedName_FailsTheExactCheck(t *testing.T) {
	names, listing := fakeTestListing(30)
	sort.Strings(names)
	for _, tc := range []struct{ name, from, to, want, victim string }{
		{"last name dropped", `"$scratch/listing" | sort -u >"$scratch/names"`,
			`"$scratch/listing" | sort -u | sed '$d' >"$scratch/names"`, "selected by no shard", names[len(names)-1]},
		{"first name in two shards", "print (h % n) + 1, $0",
			"if (NR == 1) { print 1, $0; print 2, $0 } else print (h % n) + 1, $0", "selected by more than one shard", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := testShardFixture(t, listing, "0")
			path := filepath.Join(root, "scripts", "ci-go-shards.sh")
			source, err := os.ReadFile(path) //nolint:gosec // test-owned path
			require.NoError(t, err)
			require.Contains(t, string(source), tc.from)
			mutated := strings.Replace(string(source), tc.from, tc.to, 1)
			require.NoError(t, os.WriteFile(path, []byte(mutated), 0o600))
			out, err := runTestShards(root, "test-check", "internal/slow", "3")
			require.Error(t, err, out)
			require.Contains(t, out, tc.want)
			require.NotContains(t, out, "split is exact")
			if tc.victim != "" {
				require.Contains(t, out, tc.victim)
			}
		})
	}
}

// barFixture builds a replica with the remote bar manifests and one package
// (internal/pg) whose test reads MTIX_PG_TEST_DSN.
func barFixture(t *testing.T, packages []string, sets map[string][]string) string {
	t.Helper()
	root := shardFixture(t, packages, nil)
	dir := filepath.Join(root, "scripts", "remote-bar-packages")
	require.NoError(t, os.MkdirAll(dir, 0o750))
	for name, entries := range sets {
		text := ""
		if len(entries) > 0 {
			text = strings.Join(entries, "\n") + "\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0o600))
	}
	pg := filepath.Join(root, "internal", "pg")
	require.NoError(t, os.MkdirAll(pg, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pg, "hub_test.go"), []byte("package pg\n\nconst envDSN = \"MTIX_PG_TEST_DSN\"\n"), 0o600))
	return root
}

func barPackages() []string {
	return []string{shardModule, shardModule + "/cmd/mtix", shardModule + "/internal/pg", shardModule + "/internal/slow", shardModule + "/internal/fast"}
}

func barSets() map[string][]string {
	return map[string][]string{
		"serial":   {shardModule + "/internal/pg"},
		"parallel": {shardModule, shardModule + "/internal/fast"},
		"sharded":  {shardModule + "/internal/slow"},
	}
}

func TestBarList_EachSet_PrintsItsOwnManifestDisjointAndComplete(t *testing.T) {
	sets := barSets()
	root := barFixture(t, barPackages(), sets)
	union := map[string]int{}
	for _, name := range []string{"serial", "parallel", "sharded"} {
		out, err := runShardGuard(root, "bar-list", name)
		require.NoError(t, err, out)
		require.Equal(t, strings.Join(sets[name], "\n")+"\n", out, name)
		for _, p := range strings.Fields(out) {
			union[p]++
		}
	}
	serial, err := runShardGuard(root, "bar-list", "serial")
	require.NoError(t, err)
	require.Equal(t, shardModule+"/internal/pg\n", serial, "serial is exactly the PostgreSQL consumer set")
	for _, p := range barPackages() {
		if p == shardCmdMtix {
			require.Zero(t, union[p], "cmd/mtix has its own shards")
			continue
		}
		require.Equal(t, 1, union[p], "%s must be in exactly one set", p)
	}
	out, err := runShardGuard(root, "bar-check")
	require.NoError(t, err, out)
	require.Contains(t, out, "split is exact")
}

func TestBarCheck_BrokenSets_Fails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string][]string)
		want   string
	}{
		{"missing package", func(m map[string][]string) { m["parallel"] = []string{shardModule} }, "unlisted packages"},
		{"duplicate package", func(m map[string][]string) { m["parallel"] = append(m["parallel"], shardModule+"/internal/slow") }, "duplicate packages"},
		{"stale package", func(m map[string][]string) { m["parallel"] = append(m["parallel"], shardModule+"/gone") }, "stale packages"},
		{"cmd/mtix listed", func(m map[string][]string) { m["parallel"] = append(m["parallel"], shardCmdMtix) }, "stale packages"},
		{"PG consumer listed as parallel", func(m map[string][]string) {
			m["serial"] = []string{shardModule + "/internal/slow"}
			m["sharded"] = []string{shardModule + "/internal/pg"}
		}, "PostgreSQL consumer outside the serial set: " + shardModule + "/internal/pg"},
		{"empty serial manifest", func(m map[string][]string) { m["serial"] = nil }, "missing or empty manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sets := barSets()
			tc.change(sets)
			root := barFixture(t, barPackages(), sets)
			out, err := runShardGuard(root, "bar-check")
			require.Error(t, err, out)
			require.Contains(t, out, tc.want)
			_, err = runShardGuard(root, "bar-list", "serial")
			if tc.name == "PG consumer listed as parallel" {
				require.Error(t, err, "bar-list also refuses an inexact split")
			}
		})
	}
}
