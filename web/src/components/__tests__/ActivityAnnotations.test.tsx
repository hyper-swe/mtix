import { useState } from "react";
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { ActivityStream } from "../ActivityStream";

const stamp = "2026-10-14T12:00:00Z";
const plain = {id:"plain",type:"comment",author:"writer",text:"A **plain** comment",created_at:stamp};
const addressed = {id:"addressed",type:"comment",author:"other",addressee:"reader",text:"An *addressed* [comment](https://example.com)",created_at:stamp};

// Extra fields on these variables remain structurally assignable to the OLD
// ActivityEntry type. Missing model fields cannot masquerade as behavior RED.
describe("annotation activity", () => {
 it("shows plain and addressed comments with recipient and rendered markdown", () => {
  const { container }=render(<ActivityStream entries={[addressed,plain]} hasMore={false} onLoadMore={vi.fn()} />);
  expect(screen.getByText("reader")).toBeInTheDocument();
  expect(screen.getByText("writer")).toBeInTheDocument();
  expect(screen.getByText("other")).toBeInTheDocument();
  expect(container.querySelector("strong")).toHaveTextContent("plain");
  expect(container.querySelector("em")).toHaveTextContent("addressed");
  expect(screen.getByRole("link",{name:"comment"})).toHaveAttribute("href","https://example.com");
  fireEvent.click(screen.getByTestId("filter-comment"));
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(2);
 });

 it("escapes identities and sanitizes hostile bodies while preserving safe formatting", () => {
  const evil={...addressed,author:'<img src=x onerror="alert(1)">',addressee:'<iframe src=x></iframe>',text:'**safe** <script>alert(1)</script> <img src=x onerror="alert(1)"> [bad](javascript:alert(1))'};
  const {container}=render(<ActivityStream entries={[evil]} hasMore={false} onLoadMore={vi.fn()} />);
  expect(screen.getByText(evil.author)).toBeInTheDocument();
  expect(screen.getByText(evil.addressee)).toBeInTheDocument();
  expect(container.querySelector("strong")).toHaveTextContent("safe");
  expect(container.querySelector("script,iframe,img,[onerror],[onclick]")).toBeNull();
  expect(container.querySelector('a[href^="javascript:"],a[href^="data:"]')).toBeNull();
 });

 it("keeps the Comments filter and existing rows when another activity page arrives", () => {
  const legacy={id:"legacy",type:"status_change",author:"writer",text:"Status changed",created_at:stamp};
  function Pages() {
   const [loaded,setLoaded]=useState(false);
   return <ActivityStream entries={loaded?[legacy,addressed,plain]:[legacy,addressed]} hasMore={!loaded} onLoadMore={()=>setLoaded(true)} />;
  }
  render(<Pages/>);
  fireEvent.click(screen.getByTestId("filter-comment"));
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(1);
  fireEvent.click(screen.getByTestId("load-more"));
  const rows=screen.getAllByTestId("activity-entry");
  expect(rows).toHaveLength(2);
  const [first,second]=rows;
  if (!first||!second) throw new Error("expected two comment rows");
  expect(within(first).getByText("reader")).toBeInTheDocument();
  expect(within(second).getByText("writer")).toBeInTheDocument();
  expect(screen.queryByText("Status changed")).not.toBeInTheDocument();
  fireEvent.click(screen.getByTestId("filter-all"));
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(3);
 });
});

describe("literal identities and plain Markdown comments", () => {
 it("renders a plain comment body as Markdown without an addressee", () => {
  const {container}=render(<ActivityStream entries={[plain]} hasMore={false} onLoadMore={vi.fn()} />);
  expect(container.querySelector("strong")).toHaveTextContent("plain");
  expect(screen.queryByTestId("activity-addressee")).not.toBeInTheDocument();
 });
 it("never interprets Markdown inside author or recipient identifiers", () => {
  const entry={...plain,author:"_writer_",addressee:"[reader](https://example.com)"};
  render(<ActivityStream entries={[entry]} hasMore={false} onLoadMore={vi.fn()} />);
  const author=screen.getByTestId("activity-author"), recipient=screen.getByTestId("activity-addressee");
  expect(author.textContent).toBe(entry.author);expect(recipient.textContent).toBe(entry.addressee);
  expect(author.querySelector("em,strong,a")).toBeNull();expect(recipient.querySelector("em,strong,a")).toBeNull();
 });
});

describe("activity row display boundaries", () => {
 it.each([
  [20,"just now"], [120,"2 min ago"], [7200,"2h ago"], [172800,"2d ago"],
 ])("formats an entry %i seconds old", (seconds,label) => {
  const now=Date.parse("2026-10-14T12:00:00Z");vi.useFakeTimers();vi.setSystemTime(now);
  try {
   render(<ActivityStream entries={[{...plain,created_at:new Date(now-Number(seconds)*1000).toISOString()}]} hasMore={false} onLoadMore={vi.fn()} />);
   expect(screen.getByTestId("activity-timestamp")).toHaveTextContent(String(label));
  } finally {vi.useRealTimers();}
 });
 it("shows a calendar date for older entries", () => {
  const now=Date.parse("2026-10-14T12:00:00Z"),date=new Date(now-8*86400000);vi.useFakeTimers();vi.setSystemTime(now);
  try {
   render(<ActivityStream entries={[{...plain,created_at:date.toISOString()}]} hasMore={false} onLoadMore={vi.fn()} />);
   expect(screen.getByTestId("activity-timestamp")).toHaveTextContent(date.toLocaleDateString());
  } finally {vi.useRealTimers();}
 });
 it("preserves legacy entries with no body and filters them out of Comments", () => {
  const entry={id:"created",type:"created",author:"writer",created_at:stamp};
  render(<ActivityStream entries={[entry]} hasMore={false} onLoadMore={vi.fn()} />);
  expect(screen.getAllByTestId("activity-entry")).toHaveLength(1);
  fireEvent.click(screen.getByTestId("filter-comment"));expect(screen.getByText("No activity")).toBeInTheDocument();
 });
});

describe("legacy activity icons", () => {
 it.each([["child_created","+"],["agent_done","✓"]])("keeps the %s icon", (type,icon) => {
  render(<ActivityStream entries={[{...plain,type}]} hasMore={false} onLoadMore={vi.fn()} />);
  expect(screen.getByTestId("activity-entry").querySelector("span")).toHaveTextContent(icon);
 });
});

describe("hardened Markdown rendering", () => {
 it("keeps comment markup inside the permitted attributes", () => {
  // Vitest's jsdom environment does not enable resource loading or script execution.
  const entry={...plain,text:'![image](https://example.invalid/image" /onerror="window.marker=1")'};
  const {container}=render(<ActivityStream entries={[entry]} hasMore={false} onLoadMore={vi.fn()} />);
  expect(container.querySelector("img")).not.toBeNull();
  expect(container.querySelector("[onerror]")).toBeNull();
 });
});
