"""Python dataclasses matching mtix protobuf types per FR-8.3.

All types mirror the Go model package and protobuf definitions
for wire compatibility. Provides Pythonic access to mtix data.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass, field, asdict
from datetime import datetime
from typing import Any, Optional


# --- Enums ---


class Status(str, enum.Enum):
    """Node lifecycle state per FR-3.5."""

    OPEN = "open"
    IN_PROGRESS = "in_progress"
    BLOCKED = "blocked"
    DONE = "done"
    DEFERRED = "deferred"
    CANCELLED = "cancelled"
    INVALIDATED = "invalidated"


class NodeType(str, enum.Enum):
    """Tier classification per FR-1.2."""

    STORY = "story"
    EPIC = "epic"
    ISSUE = "issue"
    MICRO = "micro"
    AUTO = "auto"


class IssueType(str, enum.Enum):
    """Nature of work classification."""

    BUG = "bug"
    FEATURE = "feature"
    TASK = "task"
    CHORE = "chore"
    REFACTOR = "refactor"
    TEST = "test"
    DOC = "doc"


class DepType(str, enum.Enum):
    """Dependency relationship type per FR-4.2."""

    BLOCKS = "blocks"
    RELATED = "related"
    DISCOVERED_FROM = "discovered_from"
    DUPLICATES = "duplicates"


class AgentState(str, enum.Enum):
    """LLM agent state."""

    IDLE = "idle"
    WORKING = "working"
    STUCK = "stuck"
    DONE = "done"


class ActivityType(str, enum.Enum):
    """Activity entry type per FR-3.6."""

    COMMENT = "comment"
    STATUS_CHANGE = "status_change"
    NOTE = "note"
    ANNOTATION = "annotation"
    UNCLAIM = "unclaim"
    CLAIM = "claim"
    SYSTEM = "system"
    PROMPT_EDIT = "prompt_edit"
    CREATED = "created"


class Priority(int, enum.Enum):
    """Priority levels matching Go model.Priority constants."""

    CRITICAL = 1
    HIGH = 2
    MEDIUM = 3
    LOW = 4
    BACKGROUND = 5


class EventType(str, enum.Enum):
    """Real-time event types per FR-7.5a."""

    NODE_CREATED = "node.created"
    NODE_UPDATED = "node.updated"
    NODE_DELETED = "node.deleted"
    NODE_UNDELETED = "node.undeleted"
    NODE_CLAIMED = "node.claimed"
    STATUS_CHANGED = "status.changed"
    PROGRESS_CHANGED = "progress.changed"
    DEPENDENCY_ADDED = "dependency.added"
    DEPENDENCY_REMOVED = "dependency.removed"
    SESSION_STARTED = "session.started"
    SESSION_ENDED = "session.ended"
    ANNOTATION_ADDED = "annotation.added"
    NODES_INVALIDATED = "nodes.invalidated"
    CONFIG_CHANGED = "config.changed"


# --- Dataclasses ---


@dataclass
class CodeRef:
    """Reference to source code location."""

    file: str = ""
    line: int = 0
    symbol: str = ""


@dataclass
class Annotation:
    """Prompt annotation per FR-12.2."""

    id: str = ""
    text: str = ""
    author: str = ""
    resolved: bool = False
    created_at: Optional[datetime] = None


@dataclass
class ActivityEntry:
    """Activity log entry per FR-3.6."""

    id: str = ""
    type: ActivityType = ActivityType.COMMENT
    author: str = ""
    text: str = ""
    created_at: Optional[datetime] = None

    def to_dict(self) -> dict[str, Any]:
        """Serialize to JSON-compatible dict."""
        d = asdict(self)
        d["type"] = self.type.value
        if self.created_at:
            d["created_at"] = self.created_at.isoformat() + "Z"
        return d


@dataclass
class Dependency:
    """Dependency between nodes per FR-4."""

    from_id: str = ""
    to_id: str = ""
    dep_type: DepType = DepType.BLOCKS
    created_at: Optional[datetime] = None

    def to_dict(self) -> dict[str, Any]:
        """Serialize to JSON-compatible dict."""
        d = asdict(self)
        d["dep_type"] = self.dep_type.value
        if self.created_at:
            d["created_at"] = self.created_at.isoformat() + "Z"
        return d


@dataclass
class Node:
    """A mtix node (story/epic/issue/micro) per FR-1.

    Matches the 37+ fields in the protobuf Node message and Go model.Node.
    """

    id: str = ""
    parent_id: str = ""
    project: str = ""
    depth: int = 0
    seq: int = 0
    title: str = ""
    description: str = ""
    prompt: str = ""
    acceptance: str = ""
    labels: list[str] = field(default_factory=list)
    priority: Priority = Priority.MEDIUM
    status: Status = Status.OPEN
    node_type: NodeType = NodeType.ISSUE
    issue_type: IssueType = IssueType.TASK
    creator: str = ""
    assignee: str = ""
    agent_state: AgentState = AgentState.IDLE
    weight: float = 1.0
    progress: float = 0.0
    content_hash: str = ""
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None
    closed_at: Optional[datetime] = None
    defer_until: Optional[datetime] = None
    deleted_at: Optional[datetime] = None
    code_refs: list[CodeRef] = field(default_factory=list)
    annotations: list[Annotation] = field(default_factory=list)
    activity: list[ActivityEntry] = field(default_factory=list)
    child_count: int = 0

    def to_dict(self) -> dict[str, Any]:
        """Serialize to JSON-compatible dict matching REST API format."""
        d: dict[str, Any] = {
            "id": self.id,
            "parent_id": self.parent_id,
            "project": self.project,
            "depth": self.depth,
            "seq": self.seq,
            "title": self.title,
            "description": self.description,
            "prompt": self.prompt,
            "acceptance": self.acceptance,
            "labels": self.labels,
            "priority": self.priority.value,
            "status": self.status.value,
            "node_type": self.node_type.value,
            "issue_type": self.issue_type.value,
            "creator": self.creator,
            "assignee": self.assignee,
            "agent_state": self.agent_state.value,
            "weight": self.weight,
            "progress": self.progress,
            "content_hash": self.content_hash,
            "child_count": self.child_count,
        }
        for ts_field in ["created_at", "updated_at", "closed_at", "defer_until", "deleted_at"]:
            val = getattr(self, ts_field)
            d[ts_field] = val.isoformat() + "Z" if val else None
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Node:
        """Create a Node from a JSON dict (REST API response)."""
        kwargs: dict[str, Any] = {}
        for f in [
            "id", "parent_id", "project", "title", "description",
            "prompt", "acceptance", "creator", "assignee", "content_hash",
        ]:
            if f in data:
                kwargs[f] = data[f]
        for f in ["depth", "seq", "child_count"]:
            if f in data:
                kwargs[f] = int(data[f])
        for f in ["weight", "progress"]:
            if f in data:
                kwargs[f] = float(data[f])
        if "labels" in data and data["labels"]:
            kwargs["labels"] = list(data["labels"])
        if "status" in data and data["status"]:
            kwargs["status"] = Status(data["status"])
        if "node_type" in data and data["node_type"]:
            kwargs["node_type"] = NodeType(data["node_type"])
        if "priority" in data and data["priority"]:
            kwargs["priority"] = Priority(int(data["priority"]))
        if "agent_state" in data and data["agent_state"]:
            kwargs["agent_state"] = AgentState(data["agent_state"])
        for ts_field in ["created_at", "updated_at", "closed_at", "defer_until", "deleted_at"]:
            if ts_field in data and data[ts_field]:
                kwargs[ts_field] = _parse_timestamp(data[ts_field])
        return cls(**kwargs)


@dataclass
class ContextEntry:
    """A single node in the context chain per FR-12.1."""

    id: str = ""
    title: str = ""
    status: Status = Status.OPEN
    prompt: str = ""
    acceptance: str = ""
    depth: int = 0


@dataclass
class ContextSibling:
    """A sibling node in the context response."""

    id: str = ""
    title: str = ""
    status: Status = Status.OPEN


@dataclass
class ContextResponse:
    """Assembled context for a node per FR-12.1."""

    chain: list[ContextEntry] = field(default_factory=list)
    siblings: list[ContextSibling] = field(default_factory=list)
    blocking_deps: list[Dependency] = field(default_factory=list)
    assembled_prompt: str = ""

    def to_dict(self) -> dict[str, Any]:
        """Serialize to JSON-compatible dict."""
        return {
            "chain": [asdict(e) for e in self.chain],
            "siblings": [asdict(s) for s in self.siblings],
            "blocking_deps": [d.to_dict() for d in self.blocking_deps],
            "assembled_prompt": self.assembled_prompt,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> ContextResponse:
        """Create from a JSON dict."""
        chain = [
            ContextEntry(
                id=e.get("id", ""),
                title=e.get("title", ""),
                status=Status(e["status"]) if e.get("status") else Status.OPEN,
                prompt=e.get("prompt", ""),
                acceptance=e.get("acceptance", ""),
                depth=e.get("depth", 0),
            )
            for e in data.get("chain", [])
        ]
        siblings = [
            ContextSibling(
                id=s.get("id", ""),
                title=s.get("title", ""),
                status=Status(s["status"]) if s.get("status") else Status.OPEN,
            )
            for s in data.get("siblings", [])
        ]
        return cls(
            chain=chain,
            siblings=siblings,
            assembled_prompt=data.get("assembled_prompt", ""),
        )


@dataclass
class SessionSummary:
    """Session summary per FR-11.1."""

    session_id: str = ""
    status: str = ""
    started_at: Optional[datetime] = None
    ended_at: Optional[datetime] = None
    nodes_created: int = 0
    nodes_completed: int = 0
    nodes_deferred: int = 0
    summary_text: str = ""

    def to_dict(self) -> dict[str, Any]:
        """Serialize to JSON-compatible dict."""
        d = asdict(self)
        if self.started_at:
            d["started_at"] = self.started_at.isoformat() + "Z"
        if self.ended_at:
            d["ended_at"] = self.ended_at.isoformat() + "Z"
        return d


@dataclass
class ProgressResponse:
    """Progress rollup for a node per FR-5."""

    node_id: str = ""
    progress: float = 0.0
    total_children: int = 0
    done_children: int = 0
    open_children: int = 0
    blocked_children: int = 0

    def to_dict(self) -> dict[str, Any]:
        """Serialize to JSON-compatible dict."""
        return asdict(self)


@dataclass
class Event:
    """Real-time event per FR-7.5a."""

    type: EventType = EventType.NODE_CREATED
    node_id: str = ""
    timestamp: Optional[datetime] = None
    author: str = ""
    data: Optional[dict[str, Any]] = None

    def to_dict(self) -> dict[str, Any]:
        """Serialize to JSON-compatible dict."""
        d: dict[str, Any] = {
            "type": self.type.value,
            "node_id": self.node_id,
            "author": self.author,
        }
        if self.timestamp:
            d["timestamp"] = self.timestamp.isoformat() + "Z"
        if self.data:
            d["data"] = self.data
        return d


# --- Exceptions ---


class MtixError(Exception):
    """Base exception for mtix SDK errors."""

    def __init__(self, message: str, code: str = ""):
        super().__init__(message)
        self.code = code


class NotFoundError(MtixError):
    """Raised when a resource is not found."""

    def __init__(self, message: str = "not found"):
        super().__init__(message, "NOT_FOUND")


class InvalidInputError(MtixError):
    """Raised when input validation fails."""

    def __init__(self, message: str = "invalid input"):
        super().__init__(message, "INVALID_INPUT")


class AlreadyExistsError(MtixError):
    """Raised when a resource already exists."""

    def __init__(self, message: str = "already exists"):
        super().__init__(message, "ALREADY_EXISTS")


class InvalidTransitionError(MtixError):
    """Raised when a state transition is invalid."""

    def __init__(self, message: str = "invalid transition"):
        super().__init__(message, "INVALID_TRANSITION")


class ConflictError(MtixError):
    """Raised when a conflict (optimistic lock) is detected."""

    def __init__(self, message: str = "conflict"):
        super().__init__(message, "CONFLICT")


class AlreadyClaimedError(MtixError):
    """Raised when a node is already claimed."""

    def __init__(self, message: str = "already claimed"):
        super().__init__(message, "ALREADY_CLAIMED")


class ConnectionError(MtixError):
    """Raised when connection to gRPC server fails."""

    def __init__(self, message: str = "connection failed"):
        super().__init__(message, "CONNECTION_ERROR")


# --- Helpers ---


def _parse_timestamp(value: str) -> Optional[datetime]:
    """Parse ISO-8601 UTC timestamp string to datetime."""
    if not value:
        return None
    # Handle various ISO-8601 formats.
    value = value.rstrip("Z")
    try:
        return datetime.fromisoformat(value)
    except ValueError:
        return None
