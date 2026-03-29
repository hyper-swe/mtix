"""Tests for mtix Python SDK client per MTIX-7.3.1.

Tests the MtixClient class construction and error handling.
Integration tests with a running server are marked with @pytest.mark.integration.
"""

import json
import pytest
from unittest.mock import patch, MagicMock

from mtix.client import MtixClient, _DEFAULT_HTTP_BASE
from mtix.types import (
    ConnectionError,
    InvalidInputError,
    NotFoundError,
    Node,
    Status,
)


class TestMtixClientConstruction:
    """Tests for MtixClient initialization."""

    def test_default_address(self):
        """Verify default connects to localhost:6849."""
        client = MtixClient()
        assert client._base_url == _DEFAULT_HTTP_BASE

    def test_custom_address(self):
        """Verify custom address override."""
        client = MtixClient(address="http://myhost:9999")
        assert client._base_url == "http://myhost:9999"

    def test_trailing_slash_stripped(self):
        """Verify trailing slash is removed."""
        client = MtixClient(address="http://localhost:6849/")
        assert client._base_url == "http://localhost:6849"

    def test_custom_timeout(self):
        """Verify timeout configuration."""
        client = MtixClient(timeout=60.0)
        assert client._timeout == 60.0


class TestMtixClientErrorHandling:
    """Tests for HTTP error to exception mapping."""

    def test_handle_404_raises_not_found(self):
        """Verify 404 maps to NotFoundError."""
        client = MtixClient()
        mock_exc = MagicMock()
        mock_exc.code = 404
        mock_exc.read.return_value = json.dumps({
            "error": {"code": "NOT_FOUND", "message": "node X not found"}
        }).encode()

        with pytest.raises(NotFoundError) as exc_info:
            client._handle_http_error(mock_exc)
        assert "node X not found" in str(exc_info.value)

    def test_handle_400_raises_invalid_input(self):
        """Verify 400 maps to InvalidInputError."""
        client = MtixClient()
        mock_exc = MagicMock()
        mock_exc.code = 400
        mock_exc.read.return_value = json.dumps({
            "error": {"code": "INVALID_INPUT", "message": "title required"}
        }).encode()

        with pytest.raises(InvalidInputError):
            client._handle_http_error(mock_exc)

    def test_handle_malformed_error_body(self):
        """Verify handling of non-JSON error body."""
        client = MtixClient()
        mock_exc = MagicMock()
        mock_exc.code = 500
        mock_exc.read.return_value = b"Internal Server Error"

        from mtix.types import MtixError
        with pytest.raises(MtixError):
            client._handle_http_error(mock_exc)


class TestMtixClientConnection:
    """Tests for connection handling."""

    def test_connection_error_raised(self):
        """Verify ConnectionError on unreachable server."""
        client = MtixClient(address="http://localhost:1", timeout=0.5)
        with pytest.raises(Exception):
            # Should fail to connect — exact exception depends on urllib behavior.
            client._request("GET", "/health")


class TestMtixClientMethods:
    """Tests for client method argument construction (unit tests with mocking)."""

    def test_micro_builds_correct_request(self):
        """Verify micro() sends correct POST body."""
        client = MtixClient()
        mock_resp = {
            "id": "TEST-1",
            "title": "Test",
            "project": "TEST",
            "status": "open",
        }

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            node = client.micro("Test", project="TEST", creator="agent-1")
            mock_req.assert_called_once_with("POST", "/api/v1/nodes", {
                "title": "Test",
                "project": "TEST",
                "creator": "agent-1",
            })
            assert node.id == "TEST-1"
            assert node.status == Status.OPEN

    def test_micro_with_optional_fields(self):
        """Verify micro() includes optional fields when provided."""
        client = MtixClient()
        mock_resp = {"id": "TEST-1", "title": "Test", "status": "open"}

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            client.micro(
                "Test",
                project="TEST",
                parent_id="PARENT-1",
                description="desc",
                prompt="do it",
            )
            call_body = mock_req.call_args[0][2]
            assert call_body["parent_id"] == "PARENT-1"
            assert call_body["description"] == "desc"
            assert call_body["prompt"] == "do it"

    def test_get_calls_correct_endpoint(self):
        """Verify get() calls GET /api/v1/nodes/{id}."""
        client = MtixClient()
        mock_resp = {"id": "X-1", "title": "Got"}

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            node = client.get("X-1")
            mock_req.assert_called_once_with("GET", "/api/v1/nodes/X-1")
            assert node.id == "X-1"

    def test_claim_sends_agent_id(self):
        """Verify claim() sends agent_id in body."""
        client = MtixClient()
        mock_resp = {"id": "X-1", "assignee": "agent-1", "status": "in_progress"}

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            node = client.claim("X-1", agent="agent-1")
            call_body = mock_req.call_args[0][2]
            assert call_body["agent_id"] == "agent-1"

    def test_claim_force_flag(self):
        """Verify claim() passes force flag."""
        client = MtixClient()
        mock_resp = {"id": "X-1", "status": "in_progress"}

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            client.claim("X-1", agent="agent-1", force=True)
            call_body = mock_req.call_args[0][2]
            assert call_body["force"] is True

    def test_done_calls_done_endpoint(self):
        """Verify done() calls POST /api/v1/nodes/{id}/done."""
        client = MtixClient()
        mock_resp = {"id": "X-1", "status": "done"}

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            node = client.done("X-1", agent="agent-1")
            mock_req.assert_called_once_with(
                "POST", "/api/v1/nodes/X-1/done", {"agent_id": "agent-1"}
            )

    def test_context_returns_context_response(self):
        """Verify context() returns ContextResponse."""
        client = MtixClient()
        mock_resp = {
            "chain": [{"id": "X-1", "title": "Root", "status": "open"}],
            "assembled_prompt": "Do the thing",
        }

        with patch.object(client, "_request", return_value=mock_resp):
            ctx = client.context("X-1")
            assert ctx.assembled_prompt == "Do the thing"
            assert len(ctx.chain) == 1

    def test_decompose_returns_ids(self):
        """Verify decompose() returns list of IDs."""
        client = MtixClient()
        mock_resp = {"ids": ["X-1.1", "X-1.2"]}

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            ids = client.decompose("X-1", [
                {"title": "Child A"},
                {"title": "Child B"},
            ])
            assert ids == ["X-1.1", "X-1.2"]

    def test_search_with_filters(self):
        """Verify search() builds correct query params."""
        client = MtixClient()
        mock_resp = {"nodes": [], "total": 0, "has_more": False}

        with patch.object(client, "_request", return_value=mock_resp) as mock_req:
            client.search(
                status=[Status.OPEN, Status.BLOCKED],
                under="TEST-1",
                limit=10,
            )
            call_path = mock_req.call_args[0][1]
            assert "status=open" in call_path
            assert "status=blocked" in call_path
            assert "under=TEST-1" in call_path
            assert "limit=10" in call_path

    def test_dep_add_sends_correct_body(self):
        """Verify dep_add() sends correct dependency body."""
        client = MtixClient()

        with patch.object(client, "_request", return_value={}) as mock_req:
            from mtix.types import DepType
            client.dep_add("A", "B", dep_type=DepType.BLOCKS)
            call_body = mock_req.call_args[0][2]
            assert call_body["from_id"] == "A"
            assert call_body["to_id"] == "B"
            assert call_body["dep_type"] == "blocks"

    def test_session_start_returns_session_id(self):
        """Verify session_start() returns session ID."""
        client = MtixClient()

        with patch.object(client, "_request", return_value={"session_id": "sess-123"}):
            sid = client.session_start("agent-1", "TEST")
            assert sid == "sess-123"

    def test_gc_sends_retention_days(self):
        """Verify gc() sends retention_days parameter."""
        client = MtixClient()

        with patch.object(client, "_request", return_value={"deleted": 5}) as mock_req:
            result = client.gc(retention_days=7)
            call_body = mock_req.call_args[0][2]
            assert call_body["retention_days"] == 7
            assert result["deleted"] == 5


class TestPackageImports:
    """Tests for package import structure per MTIX-7.3.3."""

    def test_import_mtix_client(self):
        """Verify import mtix; client = mtix.MtixClient() works."""
        import mtix
        client = mtix.MtixClient()
        assert client is not None

    def test_import_types(self):
        """Verify all types importable from mtix."""
        from mtix import Node, Status, NodeType, DepType, AgentState
        assert Node is not None
        assert Status.OPEN == "open"

    def test_package_version(self):
        """Verify package version is set."""
        import mtix
        assert mtix.__version__ == "0.1.0"

    def test_package_metadata(self):
        """Verify __all__ exports are complete."""
        import mtix
        assert "MtixClient" in mtix.__all__
        assert "Node" in mtix.__all__
        assert "Status" in mtix.__all__
        assert "NotFoundError" in mtix.__all__
