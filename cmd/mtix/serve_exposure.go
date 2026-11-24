// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"

	mtixhttp "github.com/hyper-swe/mtix/internal/api/http"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// serveDefaultBind is the bind address when neither --addr nor api.bind names
// one (NFR-5.2).
const serveDefaultBind = "127.0.0.1"

// serveTokenRecord names the operator-local record that holds the bearer token
// a network bind requires (NFR-5.2, MTIX-220). The record store keeps it in the
// operator configuration directory at mode 0600, outside the project.
const serveTokenRecord = "serve-token"

// serveExposureCheckID is the doctor row for the serve exposure check.
const serveExposureCheckID = "serve.exposure"

var serveTokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type serveTokenFile struct {
	Token string `json:"token"`
}

// serveRequest is what `mtix serve` was asked to do.
type serveRequest struct {
	Addr         string
	AddrChanged  bool
	Port         int
	InsecureBind bool
	// AllowedOrigins are extra browser origins from --allow-origin.
	AllowedOrigins []string
}

// effectiveServeBind returns the address serve binds and doctor reports
// (NFR-5.2, MTIX-220): --addr when it was given, else the api.bind setting,
// else loopback. serve and doctor both call it, so they cannot disagree.
func effectiveServeBind(addr string, addrChanged bool, configured string) string {
	switch {
	case addrChanged:
		return addr
	case configured != "":
		return configured
	default:
		return serveDefaultBind
	}
}

// configuredServeBind reads the api.bind setting ("" when unavailable).
func configuredServeBind() string {
	if app.configSvc == nil {
		return ""
	}
	bind, err := app.configSvc.Get("api.bind")
	if err != nil {
		return ""
	}
	return bind
}

// newServeToken returns 32 random bytes as 64 lowercase hex characters.
func newServeToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate serve token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// readServeToken reads the stored token. present is false when no record
// exists. A record that is not a well-formed token is an error, and so is a
// record or directory the operator state refuses (group or world readable,
// inside a project, a symlink).
func readServeToken(state *oplocal.State) (token string, present bool, err error) {
	var stored serveTokenFile
	reports, err := state.ReadJSON(serveTokenRecord, &stored)
	if err != nil {
		return "", false, fmt.Errorf("read serve token: %w", err)
	}
	if len(reports) > 0 {
		return "", false, fmt.Errorf("read serve token: the record belongs to another host")
	}
	if stored.Token == "" {
		return "", false, nil
	}
	if !serveTokenPattern.MatchString(stored.Token) {
		return "", true, fmt.Errorf("read serve token: the stored token is malformed")
	}
	return stored.Token, true, nil
}

// ensureServeToken returns the stored token, creating it on first use. The
// first writer wins, so two concurrent serves share one token.
func ensureServeToken(state *oplocal.State, generate func() (string, error)) (token string, created bool, err error) {
	token, present, err := readServeToken(state)
	if err != nil || present {
		return token, false, err
	}
	fresh, err := generate()
	if err != nil {
		return "", false, err
	}
	won, err := state.CreateJSON(serveTokenRecord, serveTokenFile{Token: fresh})
	if err != nil {
		return "", false, fmt.Errorf("store serve token: %w", err)
	}
	token, _, err = readServeToken(state)
	return token, won, err
}

// serveTokenPath is where the token record lives, for messages.
func serveTokenPath(state *oplocal.State) string {
	dir, err := state.Path()
	if err != nil {
		return "(operator state directory unavailable)"
	}
	return dir + "/" + serveTokenRecord + ".json"
}

// runServeCommand applies the exposure rules of NFR-5.2 and starts the
// server. A loopback bind runs as before. A network bind is refused without
// --insecure-bind; with it, the bearer token is read or created on first use
// and only its path is printed, never its value (MTIX-220).
func runServeCommand(req serveRequest) error {
	cfg, err := prepareServe(req)
	if err != nil {
		return err
	}
	return runServeWith(cfg)
}

// prepareServe resolves the effective bind and builds the server
// configuration, applying the exposure rules before anything listens or any
// token file is touched.
func prepareServe(req serveRequest) (mtixhttp.ServerConfig, error) {
	bind := effectiveServeBind(req.Addr, req.AddrChanged, configuredServeBind())
	cfg := serveConfig(bind, req.Port)
	if err := mtixhttp.ValidateAllowedOrigins(req.AllowedOrigins); err != nil {
		return cfg, fmt.Errorf("--allow-origin: %w", err)
	}
	cfg.AllowedOrigins = req.AllowedOrigins
	if mtixhttp.IsLoopbackHost(bind) {
		return cfg, nil
	}
	if !req.InsecureBind {
		return cfg, mtixhttp.CheckExposure(bind, false, false)
	}
	state, err := operatorState()
	if err != nil {
		return cfg, err
	}
	token, created, err := ensureServeToken(state, newServeToken)
	if err != nil {
		return cfg, err
	}
	cfg.InsecureBind, cfg.AuthToken = true, token
	announceServeToken(state, created)
	return cfg, nil
}

// announceServeToken tells the operator where the token is and how to read
// it. It never prints the token itself.
func announceServeToken(state *oplocal.State, created bool) {
	path := serveTokenPath(state)
	verb := "using"
	if created {
		verb = "created"
	}
	fmt.Printf("Bearer token %s: %s (mode 0600; read it with: jq -r .data.token %s). "+
		"Clients send it as Authorization: Bearer <token>. To rotate it, delete that file and restart.\n",
		verb, path, path)
}

// serveExposureResult builds the doctor row for a bind and the token state
// (NFR-5.2, MTIX-220). It never carries the token.
func serveExposureResult(bind string, token string, present bool, path string, readErr error) service.DoctorResult {
	loopback := mtixhttp.IsLoopbackHost(bind)
	data := map[string]any{"bind": bind, "loopback": loopback, "token_present": present && readErr == nil}
	switch {
	case loopback:
		return service.DoctorResult{Status: service.DoctorPass, Data: data,
			Detail: fmt.Sprintf("serve binds %s, a loopback address: local clients only, no token needed", bind)}
	case readErr != nil:
		return service.DoctorResult{Status: service.DoctorFail, Data: data,
			Detail: fmt.Sprintf("serve is configured for the network address %s but the bearer token at %s cannot be used: %v", bind, path, readErr),
			Fix:    "chmod 600 " + path + " if it is group or world readable; otherwise delete it and run mtix serve --addr " + bind + " --insecure-bind to create a new one"}
	case token != "":
		return service.DoctorResult{Status: service.DoctorPass, Data: data,
			Detail: fmt.Sprintf("serve is configured for the network address %s and a bearer token exists at %s; serve also needs --insecure-bind", bind, path)}
	default:
		return service.DoctorResult{Status: service.DoctorWarn, Data: data,
			Detail: fmt.Sprintf("serve is configured for the network address %s but no bearer token exists, so mtix serve will refuse to start", bind),
			Fix:    "run mtix serve --addr " + bind + " --insecure-bind (it creates the token on first use), or set api.bind to 127.0.0.1"}
	}
}

// registerServeExposureCheck adds the serve exposure row to mtix doctor when
// the effective bind is not loopback: there is nothing to report for a local
// bind, and the default doctor output keeps its existing rows. The bind comes
// from the same function serve uses, so the two cannot disagree.
func registerServeExposureCheck(svc *service.DoctorService) error {
	bind := effectiveServeBind("", false, configuredServeBind())
	if mtixhttp.IsLoopbackHost(bind) {
		return nil
	}
	return svc.Register(service.DoctorCheck{ID: serveExposureCheckID, Run: func(context.Context) service.DoctorResult {
		state, err := operatorState()
		if err != nil {
			return serveExposureResult(bind, "", false, "(operator state)", err)
		}
		token, present, readErr := readServeToken(state)
		return serveExposureResult(bind, token, present, serveTokenPath(state), readErr)
	}})
}
