# -*- coding: utf-8 -*-
"""
Tests for the VLLM engine implementation.
"""
import pytest
from unittest.mock import patch, AsyncMock, MagicMock

from patio.engine.vllm_engine import VLLMInferenceEngine
from patio.api.protocol import LoadLoraAdapterRequest, UnLoadLoraAdapterRequest


@pytest.fixture
def vllm_engine():
    """Create a VLLM engine instance for testing."""
    return VLLMInferenceEngine("vllm", "v1.0", "http://test-endpoint")


@pytest.mark.asyncio
async def test_vllm_load_lora_adapter_success(vllm_engine):
    """Test successful loading of a LoRA adapter in VLLM engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 200
    mock_response.text = "Success"
    
    with patch.object(vllm_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = LoadLoraAdapterRequest(
            lora_name="test-adapter",
            lora_path="/path/to/adapter"
        )
        
        # Call the method
        result = await vllm_engine.load_lora_adapter(request)
        
        # Assert the result
        assert "Success" in result
        mock_client.post.assert_called_once()


@pytest.mark.asyncio
async def test_vllm_load_lora_adapter_http_error(vllm_engine):
    """Test HTTP error when loading a LoRA adapter in VLLM engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 500
    mock_response.text = "Internal Server Error"
    
    with patch.object(vllm_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = LoadLoraAdapterRequest(
            lora_name="test-adapter",
            lora_path="/path/to/adapter"
        )
        
        # Call the method
        result = await vllm_engine.load_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500


@pytest.mark.asyncio
async def test_vllm_load_lora_adapter_exception(vllm_engine):
    """Test exception handling when loading a LoRA adapter in VLLM engine."""
    # Mock the HTTP client to raise an exception
    with patch.object(vllm_engine, 'client') as mock_client:
        mock_client.post.side_effect = Exception("Network error")
        
        # Prepare the request
        request = LoadLoraAdapterRequest(
            lora_name="test-adapter",
            lora_path="/path/to/adapter"
        )
        
        # Call the method
        result = await vllm_engine.load_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500


@pytest.mark.asyncio
async def test_vllm_unload_lora_adapter_success(vllm_engine):
    """Test successful unloading of a LoRA adapter in VLLM engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 200
    mock_response.text = "Success"
    
    with patch.object(vllm_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = UnLoadLoraAdapterRequest(
            lora_name="test-adapter"
        )
        
        # Call the method
        result = await vllm_engine.unload_lora_adapter(request)
        
        # Assert the result
        assert "Success" in result
        mock_client.post.assert_called_once()


@pytest.mark.asyncio
async def test_vllm_unload_lora_adapter_http_error(vllm_engine):
    """Test HTTP error when unloading a LoRA adapter in VLLM engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 500
    mock_response.text = "Internal Server Error"
    
    with patch.object(vllm_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = UnLoadLoraAdapterRequest(
            lora_name="test-adapter"
        )
        
        # Call the method
        result = await vllm_engine.unload_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500


@pytest.mark.asyncio
async def test_vllm_unload_lora_adapter_exception(vllm_engine):
    """Test exception handling when unloading a LoRA adapter in VLLM engine."""
    # Mock the HTTP client to raise an exception
    with patch.object(vllm_engine, 'client') as mock_client:
        mock_client.post.side_effect = Exception("Network error")
        
        # Prepare the request
        request = UnLoadLoraAdapterRequest(
            lora_name="test-adapter"
        )
        
        # Call the method
        result = await vllm_engine.unload_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500