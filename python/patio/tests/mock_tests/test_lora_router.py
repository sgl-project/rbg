# -*- coding: utf-8 -*-
"""
Tests for the LoRA router endpoints.
"""
import pytest
from fastapi.testclient import TestClient
from unittest.mock import patch, AsyncMock

from patio.app import app
from patio.api.protocol import LoadLoraAdapterRequest, UnLoadLoraAdapterRequest

client = TestClient(app)


@patch('patio.api.lora_router.inference_engine')
def test_load_lora_adapter_success(mock_inference_engine):
    """Test successful loading of a LoRA adapter."""
    # Mock the engine response
    mock_engine = AsyncMock()
    mock_engine.load_lora_adapter.return_value = "Success: LoRA adapter 'test-adapter' added successfully."
    mock_inference_engine.return_value = mock_engine
    
    # Prepare the request
    request_data = {
        "lora_name": "test-adapter",
        "lora_path": "/path/to/adapter"
    }
    
    # Make the request
    response = client.post("/load_lora_adapter", json=request_data)
    
    # Assert the response
    assert response.status_code == 200
    assert "Success" in response.json()


@patch('patio.api.lora_router.inference_engine')
def test_load_lora_adapter_error(mock_inference_engine):
    """Test error handling when loading a LoRA adapter fails."""
    # Mock the engine to return an error response
    from patio.api.protocol import ErrorResponse
    mock_engine = AsyncMock()
    mock_engine.load_lora_adapter.return_value = ErrorResponse(
        code=500,
        type="ServerError",
        message="Failed to load LoRA adapter"
    )
    mock_inference_engine.return_value = mock_engine
    
    # Prepare the request
    request_data = {
        "lora_name": "test-adapter",
        "lora_path": "/path/to/adapter"
    }
    
    # Make the request
    response = client.post("/load_lora_adapter", json=request_data)
    
    # Assert the response
    assert response.status_code == 500
    assert response.json()["type"] == "ServerError"


@patch('patio.api.lora_router.inference_engine')
def test_unload_lora_adapter_success(mock_inference_engine):
    """Test successful unloading of a LoRA adapter."""
    # Mock the engine response
    mock_engine = AsyncMock()
    mock_engine.unload_lora_adapter.return_value = "Success: LoRA adapter 'test-adapter' unloaded successfully."
    mock_inference_engine.return_value = mock_engine
    
    # Prepare the request
    request_data = {
        "lora_name": "test-adapter"
    }
    
    # Make the request
    response = client.post("/unload_lora_adapter", json=request_data)
    
    # Assert the response
    assert response.status_code == 200
    assert "Success" in response.json()


@patch('patio.api.lora_router.inference_engine')
def test_unload_lora_adapter_error(mock_inference_engine):
    """Test error handling when unloading a LoRA adapter fails."""
    # Mock the engine to return an error response
    from patio.api.protocol import ErrorResponse
    mock_engine = AsyncMock()
    mock_engine.unload_lora_adapter.return_value = ErrorResponse(
        code=500,
        type="ServerError",
        message="Failed to unload LoRA adapter"
    )
    mock_inference_engine.return_value = mock_engine
    
    # Prepare the request
    request_data = {
        "lora_name": "test-adapter"
    }
    
    # Make the request
    response = client.post("/unload_lora_adapter", json=request_data)
    
    # Assert the response
    assert response.status_code == 500
    assert response.json()["type"] == "ServerError"