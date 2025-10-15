# -*- coding: utf-8 -*-
"""
Tests for the base engine functionality.
"""
import pytest
from unittest.mock import patch, MagicMock

from patio.engine.base import inference_engine, _is_url_ready
from patio.api.protocol import LoadLoraAdapterRequest, UnLoadLoraAdapterRequest


def test_inference_engine_vllm():
    """Test that inference_engine returns VLLM engine when configured for vllm."""
    with patch('patio.engine.base.envs') as mock_envs:
        mock_envs.INFERENCE_ENGINE = "vllm"
        mock_envs.INFERENCE_ENGINE_VERSION = "v1.0"
        mock_envs.INFERENCE_ENGINE_ENDPOINT = "http://test-endpoint"
        
        # This should not raise an exception
        engine = inference_engine()
        assert engine.name == "vllm"
        assert engine.version == "v1.0"
        assert engine.endpoint == "http://test-endpoint"


def test_inference_engine_sglang():
    """Test that inference_engine returns SGLang engine when configured for sglang."""
    with patch('patio.engine.base.envs') as mock_envs:
        mock_envs.INFERENCE_ENGINE = "sglang"
        mock_envs.INFERENCE_ENGINE_VERSION = "v0.5.3"
        mock_envs.INFERENCE_ENGINE_ENDPOINT = "http://test-endpoint"
        
        # This should not raise an exception
        engine = inference_engine()
        assert engine.name == "sglang"
        assert engine.version == "v0.5.3"
        assert engine.endpoint == "http://test-endpoint"


def test_inference_engine_missing_config():
    """Test that inference_engine raises ValueError when config is missing."""
    with patch('patio.engine.base.envs') as mock_envs:
        mock_envs.INFERENCE_ENGINE = None
        mock_envs.INFERENCE_ENGINE_VERSION = "v1.0"
        mock_envs.INFERENCE_ENGINE_ENDPOINT = "http://test-endpoint"
        
        with pytest.raises(ValueError, match="Inference engine or version or endpoint is not set"):
            inference_engine()


def test_inference_engine_unsupported():
    """Test that inference_engine raises ValueError for unsupported engine."""
    with patch('patio.engine.base.envs') as mock_envs:
        mock_envs.INFERENCE_ENGINE = "unsupported"
        mock_envs.INFERENCE_ENGINE_VERSION = "v1.0"
        mock_envs.INFERENCE_ENGINE_ENDPOINT = "http://test-endpoint"
        
        with pytest.raises(ValueError, match="Inference engine unsupported with version v1.0 not support"):
            inference_engine()


@patch('patio.engine.base.requests.get')
def test_is_url_ready_valid_url(mock_get):
    """Test _is_url_ready with a valid URL that responds."""
    mock_get.return_value = MagicMock(status_code=200)
    
    result = _is_url_ready("http://test-endpoint")
    assert result is True


@patch('patio.engine.base.requests.get')
def test_is_url_ready_connection_error(mock_get):
    """Test _is_url_ready with connection error."""
    mock_get.side_effect = Exception("Connection error")
    
    result = _is_url_ready("http://test-endpoint")
    assert result is False


def test_is_url_ready_invalid_url():
    """Test _is_url_ready with invalid URL format."""
    result = _is_url_ready("invalid-url")
    assert result is False


def test_is_url_ready_non_string():
    """Test _is_url_ready with non-string input."""
    result = _is_url_ready(123)
    assert result is False