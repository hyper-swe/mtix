// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

const usageText = `usage: listener-spike <command> [args]

commands:
  report <stamps-file>...                  print the matrix evidence table from stamp logs
  registry -pid N <samples-file>           check the registry contract over recorded samples
  launch -root R -cwd C [-config-dir D] [-socket S] -- <claude args>   check a scratch launch plan (never starts a session)
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes one command and returns the process exit code: 0 success,
// 1 a refused or failed check, 2 a usage error (MTIX-112.40.1).
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "report":
		return runReport(args[1:], stdout, stderr)
	case "registry":
		return runRegistry(args[1:], stdout, stderr)
	case "launch":
		return runLaunch(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usageText)
	return 2
}

func runReport(files []string, stdout, stderr io.Writer) int {
	if len(files) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var all []Stamp
	for _, name := range files {
		stamps, err := readStamps(name)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		all = append(all, stamps...)
	}
	sums, err := Summarize(all)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	table, err := Render(sums, Matrix())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprint(stdout, table)
	return 0
}

func readStamps(name string) (stamps []Stamp, err error) {
	f, err := os.Open(name) //nolint:gosec // G304: the operator names the evidence log to analyze
	if err != nil {
		return nil, fmt.Errorf("open stamps: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close %s: %w", name, cerr)
		}
	}()
	stamps, err = ParseStamps(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stamps, nil
}

func runRegistry(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("registry", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pid := fs.Int("pid", 0, "pid of the scratch session started by the driver")
	if err := fs.Parse(args); err != nil || *pid <= 0 || fs.NArg() != 1 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, fmt.Errorf("open samples: %w", err))
		return 1
	}
	recs, perr := ParseRegistry(f)
	if cerr := f.Close(); cerr != nil {
		fmt.Fprintln(stderr, fmt.Errorf("close samples: %w", cerr))
		return 1
	}
	if perr != nil {
		fmt.Fprintln(stderr, perr)
		return 1
	}
	if err := CheckRegistry(recs, *pid); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "ok: %d samples satisfy the registry contract\n", len(recs))
	return 0
}

func runLaunch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "absolute scratch root")
	cwd := fs.String("cwd", "", "working directory of the session")
	configDir := fs.String("config-dir", "", "CLAUDE_CONFIG_DIR the session will get (optional)")
	socket := fs.String("socket", "", "CLAUDE_CODE_MESSAGING_SOCKET the session will get (optional)")
	if err := fs.Parse(args); err != nil || *root == "" || *cwd == "" {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var env []string
	if *configDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+*configDir)
	}
	if *socket != "" {
		env = append(env, envMessagingSocket+"="+*socket)
	}
	if err := CheckLaunch(Launch{Root: *root, Cwd: *cwd, Args: fs.Args(), Env: env}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "ok: launch plan stays inside the scratch root")
	return 0
}
