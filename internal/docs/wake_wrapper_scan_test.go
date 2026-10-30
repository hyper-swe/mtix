// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"path"
	"strings"
)

// Known wrappers expose a lexical command or script body, never an expansion.
func wakeUnwrappedReader(command string) bool {
	head, tail := wakeShellWord(command)
	if strings.Contains(head, "=") {
		return wakeUnwrappedReader(tail)
	}
	name := path.Base(head)
	switch name {
	case "env", "nice", "nohup", "exec", "command", "time":
		return wakeUnwrappedReader(wakeWrapperOptions(name, tail))
	case "timeout":
		_, tail = wakeShellWord(wakeWrapperOptions(name, tail))
		return wakeUnwrappedReader(tail)
	case "sh", "bash":
		return wakeReaderSource(wakeShellBody(tail))
	default:
		return head != "" && head != ":" && wakeInboxAfterFlags(tail, wakeReaderAppearanceRE.MatchString(head+" "))
	}
}

var wakeWrapperValueFlags = map[string]bool{
	"-u": true, "--unset": true, "-C": true, "--chdir": true,
	"-a": true, "--argv0": true, "-n": true, "--adjustment": true,
	"-s": true, "--signal": true, "-k": true, "--kill-after": true,
	"-o": true, "--output": true, "-f": true, "--format": true,
}

func wakeWrapperOptions(wrapper, text string) string {
	for {
		word, tail := wakeShellWord(text)
		if word == "--" {
			return tail
		}
		if !strings.HasPrefix(word, "-") || word == "-" {
			return text
		}
		if wrapper == "command" && (word == "-v" || word == "-V") {
			return ""
		}
		if wrapper == "env" && (word == "-S" || word == "--split-string" || strings.HasPrefix(word, "--split-string=")) {
			body, rest := wakeShellWord(tail)
			if _, value, found := strings.Cut(word, "="); found {
				body, rest = value, tail
			}
			text = body + " " + rest
			continue
		}
		if wakeWrapperValueFlags[word] {
			_, tail = wakeShellWord(tail)
		}
		text = tail
	}
}

func wakeShellBody(text string) string {
	for {
		word, tail := wakeShellWord(text)
		if !strings.HasPrefix(word, "-") {
			return ""
		}
		if !strings.HasPrefix(word, "--") && strings.Contains(word[1:], "c") {
			body, _ := wakeShellWord(tail)
			return body
		}
		if word == "--rcfile" || word == "--init-file" || word == "-O" || word == "-o" {
			_, tail = wakeShellWord(tail)
		}
		text = tail
	}
}
