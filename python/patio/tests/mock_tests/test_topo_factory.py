# -*- coding: utf-8 -*-
"""
Tests for the topology factory.
"""
import pytest
from unittest.mock import patch

from patio.topo.factory import create_topo_client, create_topo_server
from patio.topo.client.sgl_topo_client import SGLangGroupTopoClient
from patio.topo.server.sgl_topo_server import SGLangGroupTopoServer


def test_create_topo_client_sglang():
    """Test that create_topo_client returns SGLang client for sglang type."""
    client = create_topo_client("sglang")
    assert isinstance(client, SGLangGroupTopoClient)


def test_create_topo_client_invalid():
    """Test that create_topo_client raises ValueError for invalid type."""
    with pytest.raises(ValueError, match="Invalid topo type: invalid"):
        create_topo_client("invalid")


def test_create_topo_server_sglang():
    """Test that create_topo_server returns SGLang server for sglang type."""
    server = create_topo_server("sglang")
    assert isinstance(server, SGLangGroupTopoServer)


def test_create_topo_server_invalid():
    """Test that create_topo_server raises ValueError for invalid type."""
    with pytest.raises(ValueError, match="Invalid topo type: invalid"):
        create_topo_server("invalid")