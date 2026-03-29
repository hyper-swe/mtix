"""MtixClient — Python SDK for mtix gRPC API per FR-8.3.

Provides a Pythonic interface to all mtix operations. Connects to
the mtix gRPC server and converts protobuf responses to dataclasses.

Usage:
    from mtix import MtixClient

    client = MtixClient()  # Connects to localhost:6850
    node = client.micro("Implement login", project="AUTH")
    client.claim(node.id, agent="my-agent")
    client.done(node.id, agent="my-agent")
"""

from __future__ import annotations

import json
from typing import Any, Optional

from .types import (
    AgentState,
    AlreadyClaimedError,
    AlreadyExistsError,
    ConflictError,
    ConnectionError,
    ContextResponse,
    DepType,
    Dependency,
    InvalidInputError,
    InvalidTransitionError,
    MtixError,
    Node,
    NotFoundError,
    SessionSummary,
    Status,
)

# Default server address per FR-8.1 (gRPC on port 6850).
_DEFAULT_ADDRESS = "localhost:6850"

# HTTP base URL for REST fallback.
_DEFAULT_HTTP_BASE = "http://localhost:6849"


class MtixClient:
    """Python client for the mtix micro-issue manager per FR-8.3.

    Connects to the mtix REST API server and provides Pythonic access
    to all 24+ operations. Uses HTTP/REST as the transport layer
    (gRPC client requires generated stubs; REST is always available).

    Args:
        address: HTTP base URL (default: http://localhost:6849).
        timeout: Request timeout in seconds (default: 30).
    """

    def __init__(
        self,
        address: str = _DEFAULT_HTTP_BASE,
        timeout: float = 30.0,
    ) -> None:
        self._base_url = address.rstrip("/")
        self._timeout = timeout
        self._session: Optional[Any] = None

    def _ensure_session(self) -> Any:
        """Lazily create HTTP session."""
        if self._session is None:
            try:
                import urllib.request  # noqa: F401 — verify stdlib available
                self._session = True  # Use urllib for zero-dependency client
            except ImportError as exc:
                raise ConnectionError(f"HTTP not available: {exc}") from exc
        return self._session

    def _request(
        self,
        method: str,
        path: str,
        body: Optional[dict[str, Any]] = None,
    ) -> dict[str, Any]:
        """Make an HTTP request to the mtix REST API."""
        import urllib.request
        import urllib.error

        self._ensure_session()
        url = f"{self._base_url}{path}"

        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")

        req = urllib.request.Request(
            url,
            data=data,
            method=method,
            headers={
                "Content-Type": "application/json",
                "X-Requested-With": "mtix",
            },
        )

        try:
            with urllib.request.urlopen(req, timeout=self._timeout) as resp:
                resp_body = resp.read().decode("utf-8")
                if not resp_body:
                    return {}
                return json.loads(resp_body)
        except urllib.error.HTTPError as exc:
            self._handle_http_error(exc)
        except urllib.error.URLError as exc:
            raise ConnectionError(f"connection to {url}: {exc}") from exc
        return {}  # Unreachable but satisfies type checker.

    def _handle_http_error(self, exc: Any) -> None:
        """Convert HTTP error responses to typed Python exceptions."""
        try:
            body = json.loads(exc.read().decode("utf-8"))
            error_info = body.get("error", {})
            code = error_info.get("code", "")
            message = error_info.get("message", str(exc))
        except (json.JSONDecodeError, AttributeError):
            code = ""
            message = str(exc)

        status_code = getattr(exc, "code", 500)

        if status_code == 404 or code == "NOT_FOUND":
            raise NotFoundError(message)
        if status_code == 400 or code == "INVALID_INPUT":
            raise InvalidInputError(message)
        if status_code == 409:
            if code == "ALREADY_EXISTS":
                raise AlreadyExistsError(message)
            if code == "ALREADY_CLAIMED":
                raise AlreadyClaimedError(message)
            raise ConflictError(message)
        if status_code == 422 or code == "INVALID_TRANSITION":
            raise InvalidTransitionError(message)

        raise MtixError(message, code=code)

    # --- CRUD Operations ---

    def micro(
        self,
        title: str,
        *,
        project: str = "DEFAULT",
        parent_id: str = "",
        description: str = "",
        prompt: str = "",
        acceptance: str = "",
        creator: str = "python-sdk",
    ) -> Node:
        """Create a new micro-issue per FR-8.3.

        Args:
            title: Node title (required).
            project: Project key (default: DEFAULT).
            parent_id: Parent node ID for decomposition.
            description: Optional description.
            prompt: Optional LLM prompt.
            acceptance: Optional acceptance criteria.
            creator: Creator identifier.

        Returns:
            The created Node.
        """
        body: dict[str, Any] = {
            "title": title,
            "project": project,
            "creator": creator,
        }
        if parent_id:
            body["parent_id"] = parent_id
        if description:
            body["description"] = description
        if prompt:
            body["prompt"] = prompt
        if acceptance:
            body["acceptance"] = acceptance

        resp = self._request("POST", "/api/v1/nodes", body)
        return Node.from_dict(resp)

    def get(self, node_id: str) -> Node:
        """Get a node by ID.

        Args:
            node_id: The node ID.

        Returns:
            The Node.

        Raises:
            NotFoundError: If the node does not exist.
        """
        resp = self._request("GET", f"/api/v1/nodes/{node_id}")
        return Node.from_dict(resp)

    def delete(self, node_id: str, *, cascade: bool = False, author: str = "python-sdk") -> None:
        """Soft-delete a node.

        Args:
            node_id: The node ID.
            cascade: Whether to cascade delete children.
            author: Who is deleting.
        """
        params = f"?cascade={str(cascade).lower()}&author={author}"
        self._request("DELETE", f"/api/v1/nodes/{node_id}{params}")

    def undelete(self, node_id: str) -> Node:
        """Restore a soft-deleted node.

        Args:
            node_id: The node ID.

        Returns:
            The restored Node.
        """
        resp = self._request("POST", f"/api/v1/nodes/{node_id}/undelete")
        return Node.from_dict(resp)

    # --- LLM Shortcut Operations ---

    def claim(self, node_id: str, *, agent: str, force: bool = False) -> Node:
        """Claim a node for an agent per FR-8.3.

        Args:
            node_id: The node ID.
            agent: Agent identifier.
            force: Force reclaim from stale agent.

        Returns:
            The claimed Node.
        """
        body: dict[str, Any] = {"agent_id": agent}
        if force:
            body["force"] = True
        resp = self._request("POST", f"/api/v1/nodes/{node_id}/claim", body)
        return Node.from_dict(resp)

    def unclaim(self, node_id: str, *, reason: str, agent: str) -> Node:
        """Release a node assignment.

        Args:
            node_id: The node ID.
            reason: Mandatory reason for unclaiming.
            agent: Agent releasing the claim.

        Returns:
            The unclaimed Node.
        """
        body = {"reason": reason, "agent_id": agent}
        resp = self._request("POST", f"/api/v1/nodes/{node_id}/unclaim", body)
        return Node.from_dict(resp)

    def done(self, node_id: str, *, agent: str) -> Node:
        """Mark a node as done per FR-8.3.

        Args:
            node_id: The node ID.
            agent: Agent marking done.

        Returns:
            The completed Node.
        """
        resp = self._request("POST", f"/api/v1/nodes/{node_id}/done", {"agent_id": agent})
        return Node.from_dict(resp)

    def cancel(self, node_id: str, *, reason: str, agent: str, cascade: bool = False) -> Node:
        """Cancel a node.

        Args:
            node_id: The node ID.
            reason: Reason for cancellation.
            agent: Agent cancelling.
            cascade: Whether to cascade cancel children.

        Returns:
            The cancelled Node.
        """
        body = {"reason": reason, "agent_id": agent, "cascade": cascade}
        resp = self._request("POST", f"/api/v1/nodes/{node_id}/cancel", body)
        return Node.from_dict(resp)

    def comment(self, node_id: str, text: str, *, author: str = "python-sdk") -> None:
        """Add a comment to a node per FR-8.3.

        Args:
            node_id: The node ID.
            text: Comment text.
            author: Comment author.
        """
        self._request("POST", f"/api/v1/nodes/{node_id}/comment", {"text": text, "author": author})

    def decompose(
        self,
        parent_id: str,
        children: list[dict[str, str]],
        *,
        creator: str = "python-sdk",
    ) -> list[str]:
        """Decompose a node into children per FR-8.3.

        Args:
            parent_id: Parent node ID.
            children: List of dicts with title, prompt, acceptance keys.
            creator: Creator identifier.

        Returns:
            List of created node IDs.
        """
        body = {"children": children, "creator": creator}
        resp = self._request("POST", f"/api/v1/nodes/{parent_id}/decompose", body)
        return resp.get("ids", [])

    # --- Query Operations ---

    def search(
        self,
        *,
        status: Optional[list[Status]] = None,
        under: str = "",
        assignee: str = "",
        limit: int = 50,
        offset: int = 0,
    ) -> tuple[list[Node], int, bool]:
        """Search nodes with filters per FR-8.3.

        Args:
            status: Filter by status values.
            under: Filter by subtree prefix.
            assignee: Filter by assignee.
            limit: Max results per page.
            offset: Pagination offset.

        Returns:
            Tuple of (nodes, total_count, has_more).
        """
        params: list[str] = [f"limit={limit}", f"offset={offset}"]
        if status:
            for s in status:
                params.append(f"status={s.value}")
        if under:
            params.append(f"under={under}")
        if assignee:
            params.append(f"assignee={assignee}")

        query = "&".join(params)
        resp = self._request("GET", f"/api/v1/search?{query}")
        nodes = [Node.from_dict(n) for n in resp.get("nodes", [])]
        return nodes, resp.get("total", 0), resp.get("has_more", False)

    def context(self, node_id: str) -> ContextResponse:
        """Get assembled context for a node per FR-8.3.

        Args:
            node_id: The node ID.

        Returns:
            ContextResponse with chain and assembled prompt.
        """
        resp = self._request("GET", f"/api/v1/nodes/{node_id}/context")
        return ContextResponse.from_dict(resp)

    def progress(self, node_id: str) -> dict[str, Any]:
        """Get progress rollup for a node.

        Args:
            node_id: The node ID.

        Returns:
            Progress data dict.
        """
        return self._request("GET", f"/api/v1/nodes/{node_id}/progress")

    def prompt(self, node_id: str, text: str, *, author: str = "python-sdk") -> Node:
        """Update a node's prompt per FR-8.3.

        Args:
            node_id: The node ID.
            text: New prompt text.
            author: Who is updating.

        Returns:
            Updated Node.
        """
        resp = self._request("PUT", f"/api/v1/nodes/{node_id}/prompt", {"text": text, "author": author})
        return Node.from_dict(resp)

    def annotate(self, node_id: str, text: str, *, author: str = "python-sdk") -> None:
        """Add an annotation to a node per FR-8.3.

        Args:
            node_id: The node ID.
            text: Annotation text.
            author: Annotation author.
        """
        self._request("POST", f"/api/v1/nodes/{node_id}/annotate", {"text": text, "author": author})

    def resolve_annotation(self, node_id: str, annotation_id: str) -> None:
        """Resolve an annotation.

        Args:
            node_id: The node ID.
            annotation_id: The annotation ID to resolve.
        """
        self._request("POST", f"/api/v1/nodes/{node_id}/annotations/{annotation_id}/resolve")

    def rerun(self, node_id: str, *, strategy: str = "all", reason: str = "", author: str = "python-sdk") -> None:
        """Rerun (invalidate and reprocess) descendants per FR-8.3.

        Args:
            node_id: The node ID.
            strategy: Rerun strategy (all, open_only, delete, review).
            reason: Reason for rerun.
            author: Who is triggering rerun.
        """
        body = {"strategy": strategy, "reason": reason, "author": author}
        self._request("POST", f"/api/v1/nodes/{node_id}/rerun", body)

    def restore(self, node_id: str, *, author: str = "python-sdk") -> Node:
        """Restore an invalidated node per FR-8.3.

        Args:
            node_id: The node ID.
            author: Who is restoring.

        Returns:
            Restored Node.
        """
        resp = self._request("POST", f"/api/v1/nodes/{node_id}/restore", {"author": author})
        return Node.from_dict(resp)

    # --- Convenience Queries ---

    def ready(self, *, limit: int = 50) -> list[Node]:
        """Get nodes ready for work (unblocked, unassigned, open)."""
        resp = self._request("GET", f"/api/v1/ready?limit={limit}")
        return [Node.from_dict(n) for n in resp.get("nodes", [])]

    def blocked(self, *, limit: int = 50) -> list[Node]:
        """Get blocked nodes."""
        resp = self._request("GET", f"/api/v1/blocked?limit={limit}")
        return [Node.from_dict(n) for n in resp.get("nodes", [])]

    def stale(self, *, threshold_minutes: int = 30, limit: int = 50) -> list[Node]:
        """Get nodes with stale agent assignments."""
        resp = self._request("GET", f"/api/v1/stale?threshold={threshold_minutes}&limit={limit}")
        return [Node.from_dict(n) for n in resp.get("nodes", [])]

    def orphans(self, *, limit: int = 50) -> list[Node]:
        """Get orphaned nodes (no parent, not root)."""
        resp = self._request("GET", f"/api/v1/orphans?limit={limit}")
        return [Node.from_dict(n) for n in resp.get("nodes", [])]

    # --- Dependency Operations ---

    def dep_add(self, from_id: str, to_id: str, *, dep_type: DepType = DepType.BLOCKS) -> None:
        """Add a dependency between nodes per FR-8.3.

        Args:
            from_id: Source node ID.
            to_id: Target node ID.
            dep_type: Dependency type.
        """
        body = {"from_id": from_id, "to_id": to_id, "dep_type": dep_type.value}
        self._request("POST", "/api/v1/dependencies", body)

    def dep_remove(self, from_id: str, to_id: str, *, dep_type: DepType = DepType.BLOCKS) -> None:
        """Remove a dependency between nodes per FR-8.3.

        Args:
            from_id: Source node ID.
            to_id: Target node ID.
            dep_type: Dependency type.
        """
        self._request("DELETE", f"/api/v1/dependencies/{from_id}/{to_id}?dep_type={dep_type.value}")

    # --- Session/Agent Operations ---

    def session_start(self, agent_id: str, project: str = "DEFAULT") -> str:
        """Start an agent session.

        Args:
            agent_id: Agent identifier.
            project: Project scope.

        Returns:
            Session ID.
        """
        resp = self._request("POST", "/api/v1/sessions/start", {"agent_id": agent_id, "project": project})
        return resp.get("session_id", "")

    def session_end(self, agent_id: str) -> None:
        """End an agent session.

        Args:
            agent_id: Agent identifier.
        """
        self._request("POST", "/api/v1/sessions/end", {"agent_id": agent_id})

    def session_summary(self, agent_id: str) -> SessionSummary:
        """Get session summary.

        Args:
            agent_id: Agent identifier.

        Returns:
            SessionSummary dataclass.
        """
        resp = self._request("GET", f"/api/v1/sessions/{agent_id}/summary")
        return SessionSummary(**{
            k: v for k, v in resp.items()
            if k in SessionSummary.__dataclass_fields__
        })

    def heartbeat(self, agent_id: str) -> None:
        """Send agent heartbeat.

        Args:
            agent_id: Agent identifier.
        """
        self._request("POST", "/api/v1/agents/heartbeat", {"agent_id": agent_id})

    # --- Admin Operations ---

    def gc(self, *, retention_days: int = 30) -> dict[str, Any]:
        """Run garbage collection per FR-8.3.

        Args:
            retention_days: Days before permanent deletion.

        Returns:
            GC result with deleted count.
        """
        return self._request("POST", "/api/v1/admin/gc", {"retention_days": retention_days})
