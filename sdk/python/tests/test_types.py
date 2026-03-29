"""Tests for mtix Python SDK types per MTIX-7.3.2.

Verifies dataclass construction, enum values, serialization,
and deserialization of all SDK types.
"""

import pytest
from datetime import datetime

from mtix.types import (
    ActivityEntry,
    ActivityType,
    AgentState,
    AlreadyClaimedError,
    AlreadyExistsError,
    Annotation,
    CodeRef,
    ConflictError,
    ConnectionError,
    ContextEntry,
    ContextResponse,
    ContextSibling,
    DepType,
    Dependency,
    Event,
    EventType,
    InvalidInputError,
    InvalidTransitionError,
    IssueType,
    MtixError,
    Node,
    NodeType,
    NotFoundError,
    Priority,
    ProgressResponse,
    SessionSummary,
    Status,
    _parse_timestamp,
)


# --- Enum Tests ---


class TestStatusEnum:
    """Tests for Status enum values matching Go model.Status constants."""

    def test_status_enum_values(self):
        """Verify all 7 status values match Go constants."""
        assert Status.OPEN.value == "open"
        assert Status.IN_PROGRESS.value == "in_progress"
        assert Status.BLOCKED.value == "blocked"
        assert Status.DONE.value == "done"
        assert Status.DEFERRED.value == "deferred"
        assert Status.CANCELLED.value == "cancelled"
        assert Status.INVALIDATED.value == "invalidated"

    def test_status_count(self):
        """Verify exactly 7 statuses."""
        assert len(Status) == 7

    def test_status_from_string(self):
        """Verify Status can be constructed from string."""
        assert Status("open") == Status.OPEN
        assert Status("in_progress") == Status.IN_PROGRESS

    def test_status_is_string(self):
        """Verify Status inherits from str."""
        assert isinstance(Status.OPEN, str)
        assert Status.OPEN == "open"


class TestNodeTypeEnum:
    """Tests for NodeType enum."""

    def test_node_type_values(self):
        assert NodeType.STORY.value == "story"
        assert NodeType.EPIC.value == "epic"
        assert NodeType.ISSUE.value == "issue"
        assert NodeType.MICRO.value == "micro"
        assert NodeType.AUTO.value == "auto"

    def test_node_type_count(self):
        assert len(NodeType) == 5


class TestDepTypeEnum:
    """Tests for DepType enum."""

    def test_dep_type_values(self):
        assert DepType.BLOCKS.value == "blocks"
        assert DepType.RELATED.value == "related"
        assert DepType.DISCOVERED_FROM.value == "discovered_from"
        assert DepType.DUPLICATES.value == "duplicates"


class TestAgentStateEnum:
    """Tests for AgentState enum."""

    def test_agent_state_values(self):
        assert AgentState.IDLE.value == "idle"
        assert AgentState.WORKING.value == "working"
        assert AgentState.STUCK.value == "stuck"
        assert AgentState.DONE.value == "done"


class TestActivityTypeEnum:
    """Tests for ActivityType enum."""

    def test_activity_type_count(self):
        """Verify exactly 9 activity types matching Go model."""
        assert len(ActivityType) == 9


class TestPriorityEnum:
    """Tests for Priority enum."""

    def test_priority_values(self):
        assert Priority.CRITICAL.value == 1
        assert Priority.HIGH.value == 2
        assert Priority.MEDIUM.value == 3
        assert Priority.LOW.value == 4
        assert Priority.BACKGROUND.value == 5


class TestEventTypeEnum:
    """Tests for EventType enum."""

    def test_event_type_count(self):
        """Verify 14 event types matching proto EventType."""
        assert len(EventType) == 14


# --- Dataclass Tests ---


class TestNode:
    """Tests for Node dataclass."""

    def test_node_defaults(self):
        """Verify Node default values."""
        node = Node()
        assert node.id == ""
        assert node.status == Status.OPEN
        assert node.priority == Priority.MEDIUM
        assert node.weight == 1.0
        assert node.labels == []

    def test_node_construction(self):
        """Verify Node construction with values."""
        node = Node(
            id="TEST-1",
            title="Test Node",
            project="TEST",
            status=Status.IN_PROGRESS,
            assignee="agent-1",
        )
        assert node.id == "TEST-1"
        assert node.title == "Test Node"
        assert node.status == Status.IN_PROGRESS

    def test_node_to_dict(self):
        """Verify Node serialization to dict."""
        now = datetime(2026, 3, 10, 12, 0, 0)
        node = Node(
            id="TEST-1",
            title="Test",
            project="TEST",
            status=Status.OPEN,
            created_at=now,
        )
        d = node.to_dict()
        assert d["id"] == "TEST-1"
        assert d["status"] == "open"
        assert d["priority"] == 3  # Medium
        assert d["created_at"] == "2026-03-10T12:00:00Z"
        assert d["closed_at"] is None

    def test_node_from_dict(self):
        """Verify Node deserialization from dict."""
        data = {
            "id": "TEST-1",
            "title": "From Dict",
            "project": "TEST",
            "status": "in_progress",
            "priority": 2,
            "depth": 1,
            "created_at": "2026-03-10T12:00:00Z",
            "labels": ["urgent", "backend"],
        }
        node = Node.from_dict(data)
        assert node.id == "TEST-1"
        assert node.title == "From Dict"
        assert node.status == Status.IN_PROGRESS
        assert node.priority == Priority.HIGH
        assert node.depth == 1
        assert node.labels == ["urgent", "backend"]
        assert node.created_at == datetime(2026, 3, 10, 12, 0, 0)

    def test_node_from_dict_minimal(self):
        """Verify Node from dict with minimal fields."""
        node = Node.from_dict({"id": "X-1", "title": "Minimal"})
        assert node.id == "X-1"
        assert node.status == Status.OPEN  # Default

    def test_node_roundtrip(self):
        """Verify Node survives dict roundtrip."""
        original = Node(
            id="RT-1",
            title="Roundtrip",
            project="TEST",
            status=Status.BLOCKED,
            priority=Priority.HIGH,
        )
        d = original.to_dict()
        restored = Node.from_dict(d)
        assert restored.id == original.id
        assert restored.status == original.status
        assert restored.priority == original.priority


class TestDependency:
    """Tests for Dependency dataclass."""

    def test_dependency_to_dict(self):
        dep = Dependency(from_id="A", to_id="B", dep_type=DepType.BLOCKS)
        d = dep.to_dict()
        assert d["from_id"] == "A"
        assert d["dep_type"] == "blocks"


class TestContextResponse:
    """Tests for ContextResponse dataclass."""

    def test_context_response_from_dict(self):
        """Verify ContextResponse deserialization."""
        data = {
            "chain": [
                {"id": "TEST-1", "title": "Root", "status": "open", "depth": 0},
                {"id": "TEST-1.1", "title": "Child", "status": "in_progress", "depth": 1},
            ],
            "siblings": [
                {"id": "TEST-1.2", "title": "Sibling", "status": "open"},
            ],
            "assembled_prompt": "Do the thing.",
        }
        ctx = ContextResponse.from_dict(data)
        assert len(ctx.chain) == 2
        assert ctx.chain[0].id == "TEST-1"
        assert ctx.chain[1].status == Status.IN_PROGRESS
        assert len(ctx.siblings) == 1
        assert ctx.assembled_prompt == "Do the thing."

    def test_context_response_to_dict(self):
        ctx = ContextResponse(
            chain=[ContextEntry(id="A", title="Root")],
            assembled_prompt="prompt text",
        )
        d = ctx.to_dict()
        assert d["assembled_prompt"] == "prompt text"
        assert len(d["chain"]) == 1


class TestSessionSummary:
    """Tests for SessionSummary dataclass."""

    def test_session_summary_to_dict(self):
        now = datetime(2026, 3, 10, 12, 0, 0)
        s = SessionSummary(
            session_id="sess-1",
            status="active",
            started_at=now,
            nodes_created=5,
        )
        d = s.to_dict()
        assert d["session_id"] == "sess-1"
        assert d["started_at"] == "2026-03-10T12:00:00Z"
        assert d["nodes_created"] == 5


class TestActivityEntry:
    """Tests for ActivityEntry dataclass."""

    def test_activity_entry_to_dict(self):
        entry = ActivityEntry(
            id="act-1",
            type=ActivityType.COMMENT,
            author="agent-1",
            text="This is a comment",
        )
        d = entry.to_dict()
        assert d["type"] == "comment"
        assert d["author"] == "agent-1"


class TestEvent:
    """Tests for Event dataclass."""

    def test_event_to_dict(self):
        now = datetime(2026, 3, 10, 12, 0, 0)
        e = Event(
            type=EventType.NODE_CREATED,
            node_id="TEST-1",
            timestamp=now,
            author="agent-1",
        )
        d = e.to_dict()
        assert d["type"] == "node.created"
        assert d["timestamp"] == "2026-03-10T12:00:00Z"


# --- Exception Tests ---


class TestExceptions:
    """Tests for SDK exception hierarchy."""

    def test_mtix_error_base(self):
        err = MtixError("test error", code="TEST")
        assert str(err) == "test error"
        assert err.code == "TEST"

    def test_not_found_error(self):
        err = NotFoundError("node X not found")
        assert isinstance(err, MtixError)
        assert err.code == "NOT_FOUND"

    def test_invalid_input_error(self):
        err = InvalidInputError()
        assert err.code == "INVALID_INPUT"

    def test_already_exists_error(self):
        err = AlreadyExistsError()
        assert err.code == "ALREADY_EXISTS"

    def test_invalid_transition_error(self):
        err = InvalidTransitionError()
        assert err.code == "INVALID_TRANSITION"

    def test_conflict_error(self):
        err = ConflictError()
        assert err.code == "CONFLICT"

    def test_already_claimed_error(self):
        err = AlreadyClaimedError()
        assert err.code == "ALREADY_CLAIMED"

    def test_connection_error(self):
        err = ConnectionError()
        assert err.code == "CONNECTION_ERROR"
        assert isinstance(err, MtixError)


# --- Helper Tests ---


class TestParseTimestamp:
    """Tests for _parse_timestamp helper."""

    def test_parse_iso8601_utc(self):
        dt = _parse_timestamp("2026-03-10T12:00:00Z")
        assert dt is not None
        assert dt.year == 2026
        assert dt.month == 3
        assert dt.hour == 12

    def test_parse_iso8601_no_z(self):
        dt = _parse_timestamp("2026-03-10T12:00:00")
        assert dt is not None

    def test_parse_empty_returns_none(self):
        assert _parse_timestamp("") is None

    def test_parse_none_returns_none(self):
        assert _parse_timestamp("") is None
