// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
)

// urlLongHelp documents mtix url for operators and agents.
const urlLongHelp = `Print the web UI link for a ticket.

The link opens the ticket's full detail (prompt, annotations, acceptance) in
the running web UI. It needs a running 'mtix serve' for this project; with no
serve running the command prints nothing on stdout and exits non-zero.

The id must be a display id such as MTIX-12.3. An unknown id fails with not
found. With --json the output is {"id": ..., "url": ...}.

The serve publishes .mtix/serve.json (pid, port, bound host) while
it runs and removes it when it stops. The link uses the host the serve is bound
to, or 127.0.0.1 when it is bound to a wildcard address. A stale record (dead
process, or a port that does not answer as an mtix serve) counts as no running
serve. 'mtix doctor' warns when a git project does not ignore the file and
prints the fix. Use 'mtix -C <dir> url <id>' to target another project.`

// newURLCmd creates the mtix url command (MTIX-230).
func newURLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "url <id>",
		Short: "Print the web UI link for a ticket (needs a running mtix serve)",
		Long:  urlLongHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runURL(args[0])
		},
	}
}

// runURL prints the absolute web UI URL of one ticket for the running serve.
// The id is validated before use, the node must exist, and the serve is
// discovered through the serve-address record .mtix/serve.json that mtix serve
// writes (MTIX-232), not the PID lock that routes other commands (FR-14.1b).
// Nothing is written to stdout on any error.
func runURL(id string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}
	if err := model.ValidateNodeID(id); err != nil {
		return fmt.Errorf("mtix url needs a display id: %w", err)
	}
	node, err := resolveNodeRef(context.Background(), app.store, id)
	if err != nil {
		return err
	}
	mtixDir, err := findMtixDir()
	if err != nil {
		return err
	}
	rec, alive := readServeRecord(mtixDir)
	if !alive {
		return fmt.Errorf("no running serve for this project: start it with 'mtix serve', then run mtix url again")
	}
	link := fmt.Sprintf("http://%s:%d/ticket/%s", serveLinkHost(rec.Host), rec.Port, url.PathEscape(node.ID))

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(map[string]any{"id": node.ID, "url": link})
	}
	out.WriteHuman("%s\n", link)
	return nil
}
