// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// newCommentCmd creates the mtix comment command per FR-6.3 / FR-19.1.
func newCommentCmd() *cobra.Command {
	var to, await string
	cmd := &cobra.Command{
		Use:   "comment <id> <text>",
		Short: "Add a comment annotation to a node",
		Long: `Add a comment annotation to a node.

Use --to <agent> to address the comment at a specific agent; it then lands in
that agent's inbox (see 'mtix inbox'), which is how a worker gets woken by a
ruling without a manual relay (FR-19.1).

Use --await-reply [<duration>] with --to when you need an answer: this host
then keeps contacting the hub every 60 seconds until that agent replies or the
duration passes (default 1h, at most 8h; for example --await-reply=30m). The
event carries the deadline, so a recipient host (daemon or host dispatcher)
holds its pulls for the reply too. A send to an agent registered on this host
needs no hub contact: it opens no window and holds nothing.
A reply window keeps a scale-to-zero hub awake for up to 8 h.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: withAutoExport(func(cmd *cobra.Command, args []string) error {
			wait, err := awaitReplyValue(cmd.Flags().Changed("await-reply"), await, args)
			if err != nil {
				return err
			}
			return runCommentWith(args[0], args[1], commentOptions{To: to, Await: wait, AwaitSet: cmd.Flags().Changed("await-reply")})
		}),
	}
	cmd.Flags().StringVar(&to, "to", "", "Address the comment to an agent (delivers to its inbox)")
	cmd.Flags().StringVar(&await, "await-reply", "", "With --to: keep hub pulls going until that agent replies, for the duration (default 1h, max 8h; write --await-reply=30m)")
	cmd.Flags().Lookup("await-reply").NoOptDefVal = awaitReplyDefault
	return cmd
}

// awaitReplyDefault marks --await-reply given without a duration.
const awaitReplyDefault = "default"

// commentOptions are the send options of one comment.
type commentOptions struct {
	To       string
	Await    string
	AwaitSet bool
}

// awaitReplyValue resolves the --await-reply duration text. A bare flag takes
// a third positional argument as its duration (mtix comment ID TEXT --to X
// --await-reply 30m); more than two arguments without the flag is an error.
func awaitReplyValue(set bool, flagValue string, args []string) (string, error) {
	if len(args) == 2 {
		return flagValue, nil
	}
	if !set || flagValue != awaitReplyDefault {
		return "", fmt.Errorf("accepts 2 arg(s), received %d (write --await-reply=<duration> to give a duration): %w", len(args), model.ErrInvalidInput)
	}
	return args[2], nil
}

func runComment(id, text, to string) error {
	return runCommentWith(id, text, commentOptions{To: to})
}

func runCommentWith(id, text string, opt commentOptions) error {
	to := opt.To
	if app.promptSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := mutationContext()
	// Use the process's resolved identity (MTIX-24: MTIX_AUTHOR_ID env >
	// author_id config > "cli"), not a hardcoded "cli": the annotation's
	// displayed author is what the addressee's inbox shows as the sender, so
	// hardcoding made every agent's addressed comment unattributable and
	// broke reply-routing (relay-epic prerequisite fix; see FR-21 §12.4).
	req := service.AnnotationRequest{NodeID: id, Text: text, Author: app.authorID, Addressee: to}
	if opt.AwaitSet {
		wait := opt.Await
		if wait == awaitReplyDefault {
			wait = ""
		}
		d, err := model.ParseAwaitReply(wait)
		if err != nil {
			return err
		}
		req.AwaitReply = d
	}
	res, err := app.promptSvc.AddAnnotationRequest(ctx, req)
	if err != nil {
		return err
	}
	var windowErr error
	if !res.ReplyBy.IsZero() {
		windowErr = openSenderWindow(ctx, app.mtixDir, app.store, utcNow, app.authorID, id, res)
	}

	// Show the sender whether the addressee can be woken (MTIX-112.40.6); never a silent skip.
	var wake service.WakeStatus
	if to != "" {
		wake = newCLIWakeStatusService().Current(ctx, to)
	}
	// ADR-007 D21 (MTIX-112.20.2): an addressed send requests a push of the
	// board's pending events; the command returns before any network work.
	var push addressedPush
	if to != "" {
		push = cliAddressedPush(ctx)
	}
	switch {
	case app.jsonOutput:
		out := map[string]any{"id": id, "status": "annotation_added"}
		if to != "" {
			out["to"] = to
			out["wake"] = wake
			out["push"] = push
			if !res.ReplyBy.IsZero() {
				out["reply_by"] = res.ReplyBy.Format(time.RFC3339)
				out["reply_window"] = replyWindowLine(res, windowErr)
			}
		}
		data, _ := json.Marshal(out)
		fmt.Println(string(data))
	case to != "":
		fmt.Printf("Added annotation to %s (→ %s inbox)\n", id, to)
		fmt.Printf("  push: %s\n", pushStatusLine(push))
		fmt.Printf("  wake: %s\n", wakeStatusLine(wake))
		if !res.ReplyBy.IsZero() {
			fmt.Printf("  reply window: %s\n", replyWindowLine(res, windowErr))
		}
		if wake.Fix != "" {
			fmt.Printf("  fix: %s\n", wake.Fix)
		}
	default:
		fmt.Printf("Added annotation to %s\n", id)
	}
	return nil
}

// pushStatusLine renders the push state of an addressed send.
func pushStatusLine(p addressedPush) string {
	switch p.State {
	case pushStateQueued:
		return "queued for hub push"
	case pushStateDeferred:
		return "deferred (" + p.Reason + ")"
	}
	return "off (" + p.Reason + ")"
}
