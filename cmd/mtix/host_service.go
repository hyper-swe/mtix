// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

func buildHostServiceSpec(goos, exe, home string, interval int) (*serviceSpec, error) {
	if interval <= 0 || exe == "" || home == "" || strings.ContainsFunc(exe+home, unicode.IsControl) || strings.ContainsRune(exe, '"') {
		return nil, fmt.Errorf("invalid host dispatcher service inputs")
	}
	switch goos {
	case "darwin":
		return hostLaunchdSpec(exe, home, interval), nil
	case "linux":
		return hostSystemdSpec(exe, home, interval), nil
	case "windows":
		label := "mtix-dispatcher-" + projectSlug(home)
		run := `"` + exe + `"` + " dispatcher run --experimental --interval " + strconv.Itoa(interval) + " --supervisor task-scheduler"
		return &serviceSpec{Label: label, Register: [][]string{{"schtasks", "/Create", "/F", "/TN", label, "/SC", "ONLOGON", "/TR", run}}, Unregister: [][]string{{"schtasks", "/Delete", "/F", "/TN", label}}, Start: [][]string{{"schtasks", "/Run", "/TN", label}}, Stop: [][]string{{"schtasks", "/End", "/TN", label}}, Status: [][]string{{"schtasks", "/Query", "/TN", label}}}, nil
	default:
		return nil, fmt.Errorf("unsupported host dispatcher platform %q", goos)
	}
}

func hostLaunchdSpec(exe, home string, interval int) *serviceSpec {
	label := "com.hyperswe.mtix.dispatcher"
	path := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace
	logdir := hostLogDir(home)
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>dispatcher</string><string>run</string><string>--experimental</string><string>--interval</string><string>%d</string><string>--supervisor</string><string>launchd</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, label, escape(exe), interval, escape(filepath.Join(logdir, "dispatcher.out.log")), escape(filepath.Join(logdir, "dispatcher.err.log")))
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	target := domain + "/" + label
	return &serviceSpec{Label: label, Path: path, Content: content, Register: [][]string{{"launchctl", "bootout", target}, {"launchctl", "bootstrap", domain, path}}, Unregister: [][]string{{"launchctl", "bootout", target}}, Start: [][]string{{"launchctl", "bootout", target}, {"launchctl", "bootstrap", domain, path}}, Stop: [][]string{{"launchctl", "bootout", target}}, Status: [][]string{{"launchctl", "print", target}}}
}

func hostSystemdSpec(exe, home string, interval int) *serviceSpec {
	name := "mtix-dispatcher.service"
	quote := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%", "$", "$$").Replace
	content := fmt.Sprintf(`[Unit]
Description=mtix experimental host dispatcher
[Service]
ExecStart="%s" dispatcher run --experimental --interval %d --supervisor systemd
Restart=always
RestartSec=2
[Install]
WantedBy=default.target
`, quote(exe), interval)
	return &serviceSpec{Label: name, Path: filepath.Join(home, ".config", "systemd", "user", name), Content: content, Register: [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", name}}, Unregister: [][]string{{"systemctl", "--user", "disable", "--now", name}, {"systemctl", "--user", "daemon-reload"}}, Start: [][]string{{"systemctl", "--user", "start", name}}, Stop: [][]string{{"systemctl", "--user", "stop", name}}, Status: [][]string{{"systemctl", "--user", "status", "--no-pager", name}}}
}

func hostLogDir(home string) string { return filepath.Join(home, ".local", "state", "mtix") }
