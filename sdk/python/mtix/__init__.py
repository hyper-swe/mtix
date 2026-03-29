"""mtix Python SDK — AI-native micro issue manager client.

Usage:
    from mtix import MtixClient, Node, Status

    client = MtixClient()
    node = client.micro("Implement feature", project="PROJ")
    print(node.id, node.status)
"""

__version__ = "0.1.0"

from .client import MtixClient
from .types import (
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
)

__all__ = [
    "MtixClient",
    "ActivityEntry",
    "ActivityType",
    "AgentState",
    "AlreadyClaimedError",
    "AlreadyExistsError",
    "Annotation",
    "CodeRef",
    "ConflictError",
    "ConnectionError",
    "ContextEntry",
    "ContextResponse",
    "ContextSibling",
    "DepType",
    "Dependency",
    "Event",
    "EventType",
    "InvalidInputError",
    "InvalidTransitionError",
    "IssueType",
    "MtixError",
    "Node",
    "NodeType",
    "NotFoundError",
    "Priority",
    "ProgressResponse",
    "SessionSummary",
    "Status",
]
