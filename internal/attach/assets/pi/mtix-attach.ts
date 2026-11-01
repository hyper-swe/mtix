// mtix pi extension (EXPERIMENTAL; MTIX-112.19.7).
// The live pi leg (interactive extension trust and a real-harness wake) is unproven,
// and no default mtix path installs this file. Install it by hand only to test.
//
// session_start registers the session, before_agent_start injects the inbox context (V-P1),
// the wait child (mtix attach wait) exits 0 only after it claimed the wake, and the
// nudge is delivered with followUp so it never cuts into current work (V-P4, V-L5).
import { spawn, spawnSync } from "node:child_process";
import * as path from "node:path";

// MTIX_BIN must be an absolute path: the glue never resolves mtix through PATH.
const MTIX_BIN = process.env.MTIX_BIN ?? "";
const PROTOCOL = "1";

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

  const startWait = (sessionId: string): void => {
    if (stopped) return;
    // argv only, never a shell: the session id cannot be interpreted.
    child = spawn(MTIX_BIN, ["attach", "wait", "--harness", "pi", "--protocol", PROTOCOL, "--session", sessionId], {
      stdio: ["ignore", "pipe", "ignore"],
    });
    let buffer = "";
    child.stdout?.on("data", (chunk: Buffer) => {
      buffer += chunk.toString();
    });
    child.on("exit", (code: number | null) => {
      const line = buffer.split("\n").find((l) => l.startsWith("{"));
      if (code === 0 && line) {
        try {
          const wake = JSON.parse(line) as { nudge?: string };
          if (wake.nudge) {
            pi.sendMessage({ customType: "mtix-nudge", content: wake.nudge, display: true }, { triggerTurn: true, deliverAs: "followUp" });
          }
        } catch {
          // A malformed line is ignored: the message stays in the inbox.
        }
        startWait(sessionId);
      }
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
