// mtix pi extension (EXPERIMENTAL; MTIX-112.19.7).
// The live pi leg (interactive extension trust and a real-harness wake) is unproven,
// and no default mtix path installs this file. Install it by hand only to test.
//
// The agent must already be registered (mtix agent register), because the pi adapter
// does nothing for session-start. before_agent_start injects the inbox context (V-P1);
// the wait child (mtix attach wait) exits 0 only after it claimed the wake, and the nudge
// is delivered with followUp so it never cuts into current work (V-P4, V-L5).
import { spawn, spawnSync } from "node:child_process";
import * as path from "node:path";

// MTIX_BIN must be an absolute path: the glue never resolves mtix through PATH.
const MTIX_BIN = process.env.MTIX_BIN ?? "";
const PROTOCOL = "1";
// A failed or crashed wait child is retried with a bounded backoff, then given up on:
// the messages stay in the inbox (mtix inbox) and nothing loops.
const MAX_RETRIES = 5;
const BASE_DELAY_MS = 1000;
const MAX_DELAY_MS = 30000;

type Ctx = { sessionManager: { getSessionId(): string } };
type Api = {
  on(event: string, handler: (event: unknown, ctx: Ctx) => unknown): void;
  sendMessage(message: { customType: string; content: string; display: boolean }, opts: { triggerTurn: boolean; deliverAs: string }): void;
};

export default function (pi: Api): void {
  // Inert unless mtix is configured with an absolute path.
  if (!path.isAbsolute(MTIX_BIN)) return;
  let child: ReturnType<typeof spawn> | undefined;
  let stopped = false;
  let retries = 0;

  const retryLater = (sessionId: string): void => {
    if (stopped) return;
    if (retries >= MAX_RETRIES) {
      console.error("mtix: the wait child kept failing; live wake is off, read messages with mtix inbox");
      return;
    }
    const delay = Math.min(BASE_DELAY_MS * 2 ** retries, MAX_DELAY_MS);
    retries += 1;
    setTimeout(() => startWait(sessionId), delay);
  };

  const startWait = (sessionId: string): void => {
    if (stopped) return;
    // argv only, never a shell: the session id cannot be interpreted.
    child = spawn(MTIX_BIN, ["attach", "wait", "--harness", "pi", "--protocol", PROTOCOL, "--session", sessionId], {
      stdio: ["ignore", "pipe", "ignore"],
    });
    let buffer = "";
    let settled = false;
    const settle = (woke: boolean): void => {
      if (settled) return;
      settled = true;
      if (woke) {
        retries = 0;
        startWait(sessionId);
      } else {
        retryLater(sessionId);
      }
    };
    // A missing or unrunnable binary emits error; unhandled, it would crash the host.
    child.on("error", () => settle(false));
    child.stdout?.on("data", (chunk: Buffer) => {
      buffer += chunk.toString();
    });
    child.on("exit", (code: number | null) => {
      const line = buffer.split("\n").find((l) => l.startsWith("{"));
      if (code !== 0 || !line) return settle(false);
      try {
        const wake = JSON.parse(line) as { nudge?: string };
        if (!wake.nudge) return settle(false);
        pi.sendMessage({ customType: "mtix-nudge", content: wake.nudge, display: true }, { triggerTurn: true, deliverAs: "followUp" });
      } catch {
        return settle(false); // a malformed line is ignored: the message stays in the inbox
      }
      settle(true);
    });
  };

  pi.on("session_start", (_event, ctx) => {
    const sessionId = ctx.sessionManager.getSessionId();
    spawnSync(MTIX_BIN, ["attach", "session-start", "--harness", "pi", "--protocol", PROTOCOL, "--session", sessionId], { stdio: "ignore" });
    startWait(sessionId);
  });

  pi.on("before_agent_start", () => ({
    message: { customType: "mtix-prime", content: "Check your mtix inbox with: mtix inbox", display: false },
  }));

  pi.on("session_shutdown", (_event, ctx) => {
    stopped = true;
    child?.kill();
    spawnSync(MTIX_BIN, ["attach", "session-end", "--harness", "pi", "--protocol", PROTOCOL, "--session", ctx.sessionManager.getSessionId()], { stdio: "ignore" });
  });
}
