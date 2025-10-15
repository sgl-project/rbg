# -*- coding: utf-8 -*-
"""
Tests for the server router endpoints.
"""
import pytest
from fastapi.testclient import TestClient

from patio.app import app

client = TestClient(app)


def test_root_endpoint():
    """Test the root endpoint returns the correct message."""
    response = client.get("/")
    assert response.status_code == 200
    assert response.json() == {"message": "Patio Server is running"}


def test_health_endpoint():
    """Test the health endpoint returns status OK."""
    response = client.get("/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_metrics_endpoint():
    """Test the metrics endpoint returns prometheus metrics."""
    response = client.get("/metrics")
    assert response.status_code == 200
    assert response.headers["content-type"] == "text/plain; version=0.0.4; charset=utf-8"