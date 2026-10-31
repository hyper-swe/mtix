import { describe, it, expect } from "vitest";
import { isValidNodeId } from "../nodeId";
import { parseTicketLocation, ticketPath, deselectedPath } from "../ticketLocation";

/** MTIX-230: the ticket id in the URL is hostile input and is validated before use. */
describe("isValidNodeId", () => {
  it.each(["MTIX-1", "MTIX-230.4.1", "MTIX-DEV-OPS-12.3", "A-1.u0a1b2c"])(
    "accepts %s",
    (id) => expect(isValidNodeId(id)).toBe(true),
  );
  it.each([
    "",
    "MTIX",
    "MTIX-",
    "mtix-1",
    "MTIX-1.",
    "MTIX-1..2",
    "MTIX-x",
    "MTIX-1.U0A",
    "MTIX-1 ",
    "<script>alert(1)</script>",
    "MTIX-1<img src=x onerror=alert(1)>",
    "../../etc/passwd",
    "MTIX-1/../../x",
    "..%2f..%2fetc",
    "MTIX-1%2e%2e",
    "javascript:alert(1)",
    "MTIX-1\u0000",
    "MTIX-1\n",
    "A".repeat(40) + "-1",
  ])("rejects %j", (id) => expect(isValidNodeId(id)).toBe(false));
});

describe("parseTicketLocation", () => {
  it("returns none for the root path", () => {
    expect(parseTicketLocation("/", "")).toEqual({ kind: "none" });
  });
  it("reads a canonical ticket path", () => {
    expect(parseTicketLocation("/ticket/MTIX-230.1", "")).toEqual({ kind: "ticket", id: "MTIX-230.1", alias: false });
  });
  it("reads the node query alias and marks it for rewrite", () => {
    expect(parseTicketLocation("/", "?node=MTIX-5")).toEqual({ kind: "ticket", id: "MTIX-5", alias: true });
  });
  it("prefers the path over the alias", () => {
    expect(parseTicketLocation("/ticket/MTIX-1", "?node=MTIX-2")).toEqual({ kind: "ticket", id: "MTIX-1", alias: false });
  });
  it.each([
    "/ticket/<script>alert(1)</script>",
    "/ticket/..%2f..%2fetc%2fpasswd",
    "/ticket/%2e%2e/%2e%2e/x",
    "/ticket/MTIX-1/extra",
    "/ticket/%E0%A4%A",
    "/ticket/",
    "/ticket/javascript:alert(1)",
  ])("flags hostile path %s as invalid", (p) => {
    expect(parseTicketLocation(p, "").kind).toBe("invalid");
  });
  it("flags a hostile alias as invalid", () => {
    expect(parseTicketLocation("/", "?node=%3Cscript%3E").kind).toBe("invalid");
  });
  it("ignores a ticket-like path that is not under /ticket/", () => {
    expect(parseTicketLocation("/other/MTIX-1", "").kind).toBe("none");
  });
});

describe("path builders", () => {
  it("builds the canonical ticket path keeping filters and dropping the alias", () => {
    expect(ticketPath("MTIX-3", "?status=open&node=MTIX-9")).toBe("/ticket/MTIX-3?status=open");
    expect(ticketPath("MTIX-3", "")).toBe("/ticket/MTIX-3");
  });
  it("deselect returns to root keeping filters", () => {
    expect(deselectedPath("?status=open")).toBe("/?status=open");
    expect(deselectedPath("")).toBe("/");
  });
});
