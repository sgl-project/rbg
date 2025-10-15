# -*- coding: utf-8 -*-
"""
Tests for the SGLang engine implementation.
"""
import pytest
from unittest.mock import patch, AsyncMock

from patio.engine.sglang_engine import SGLangInferenceEngine
from patio.api.protocol import LoadLoraAdapterRequest, UnLoadLoraAdapterRequest


@pytest.fixture
def sglang_engine():
    """Create an SGLang engine instance for testing."""
    return SGLangInferenceEngine("sglang", "v0.5.3", "http://test-endpoint")


@pytest.mark.asyncio
async def test_sglang_load_lora_adapter_success(sglang_engine):
    """Test successful loading of a LoRA adapter in SGLang engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 200
    mock_response.text = "Success"
    
    with patch.object(sglang_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = LoadLoraAdapterRequest(
            lora_name="test-adapter",
            lora_path="/path/to/adapter"
        )
        
        # Call the method
        result = await sglang_engine.load_lora_adapter(request)
        
        # Assert the result
        assert "Success" in result
        mock_client.post.assert_called_once()


@pytest.mark.asyncio
async def test_sglang_load_lora_adapter_http_error(sglang_engine):
    """Test HTTP error when loading a LoRA adapter in SGLang engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 500
    mock_response.text = "Internal Server Error"
    
    with patch.object(sglang_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = LoadLoraAdapterRequest(
            lora_name="test-adapter",
            lora_path="/path/to/adapter"
        )
        
        # Call the method
        result = await sglang_engine.load_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500


@pytest.mark.asyncio
async def test_sglang_load_lora_adapter_exception(sglang_engine):
    """Test exception handling when loading a LoRA adapter in SGLang engine."""
    # Mock the HTTP client to raise an exception
    with patch.object(sglang_engine, 'client') as mock_client:
        mock_client.post.side_effect = Exception("Network error")
        
        # Prepare the request
        request = LoadLoraAdapterRequest(
            lora_name="test-adapter",
            lora_path="/path/to/adapter"
        )
        
        # Call the method
        result = await sglang_engine.load_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500


@pytest.mark.asyncio
async def test_sglang_unload_lora_adapter_success(sglang_engine):
    """Test successful unloading of a LoRA adapter in SGLang engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 200
    mock_response.text = "Success"
    
    with patch.object(sglang_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = UnLoadLoraAdapterRequest(
            lora_name="test-adapter"
        )
        
        # Call the method
        result = await sglang_engine.unload_lora_adapter(request)
        
        # Assert the result
        assert "Success" in result
        mock_client.post.assert_called_once()


@pytest.mark.asyncio
async def test_sglang_unload_lora_adapter_http_error(sglang_engine):
    """Test HTTP error when unloading a LoRA adapter in SGLang engine."""
    # Mock the HTTP client response
    mock_response = AsyncMock()
    mock_response.status_code = 500
    mock_response.text = "Internal Server Error"
    
    with patch.object(sglang_engine, 'client') as mock_client:
        mock_client.post.return_value = mock_response
        
        # Prepare the request
        request = UnLoadLoraAdapterRequest(
            lora_name="test-adapter"
        )
        
        # Call the method
        result = await sglang_engine.unload_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500


@pytest.mark.asyncio
async def test_sglang_unload_lora_adapter_exception(sglang_engine):
    """Test exception handling when unloading a LoRA adapter in SGLang engine."""
    # Mock the HTTP client to raise an exception
    with patch.object(sglang_engine, 'client') as mock_client:
        mock_client.post.side_effect = Exception("Network error")
        
        # Prepare the request
        request = UnLoadLoraAdapterRequest(
            lora_name="test-adapter"
        )
        
        # Call the method
        result = await sglang_engine.unload_lora_adapter(request)
        
        # Assert the result is an error response
        assert hasattr(result, 'code')
        assert result.code == 500