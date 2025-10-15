# -*- coding: utf-8 -*-
"""
Tests for the metrics components.
"""
import pytest
from unittest.mock import patch, MagicMock

from prometheus_client import CollectorRegistry, Counter, Histogram

from patio.metrics.metrics import REQUEST_COUNT, REQUEST_LATENCY, patio_registry
from patio.metrics.engine_collector import EngineCollector
from patio.metrics.standard_rules import RenameStandardRule
from patio.metrics.engine_metric_rules import get_metric_standard_rules, VLLM_METRIC_STANDARD_RULES, \
    SGLANG_METRIC_STANDARD_RULES


@patch('patio.metrics.engine_collector.requests.Session')
def test_engine_collector_success(mock_session_class):
    """Test EngineCollector when metrics are successfully collected."""
    # Mock the session and response
    mock_session = MagicMock()
    mock_session_class.return_value = mock_session
    mock_response = MagicMock()
    mock_response.status_code = 200
    mock_response.text = "# HELP test_metric A test metric\n# TYPE test_metric counter\ntest_metric 1.0\n"
    mock_session.get.return_value = mock_response

    # Create collector
    collector = EngineCollector("http://test-endpoint/metrics", {}, "test_prefix")

    # Test collector
    result = collector._collector()
    assert "test_metric" in result


@patch('patio.metrics.engine_collector.requests.Session')
def test_engine_collector_http_error(mock_session_class):
    """Test EngineCollector when HTTP error occurs."""
    # Mock the session and response
    mock_session = MagicMock()
    mock_session_class.return_value = mock_session
    mock_response = MagicMock()
    mock_response.status_code = 500
    mock_response.text = "Internal Server Error"
    mock_session.get.return_value = mock_response

    # Create collector
    collector = EngineCollector("http://test-endpoint/metrics", {}, "test_prefix")

    # Test collector
    result = collector._collector()
    assert result == ""


@patch('patio.metrics.engine_collector.requests.Session')
def test_engine_collector_exception(mock_session_class):
    """Test EngineCollector when exception occurs."""
    # Mock the session to raise an exception
    mock_session = MagicMock()
    mock_session_class.return_value = mock_session
    mock_session.get.side_effect = Exception("Network error")

    # Create collector
    collector = EngineCollector("http://test-endpoint/metrics", {}, "test_prefix")

    # Test collector
    result = collector._collector()
    assert result == ""
