# -*- coding: utf-8 -*-
"""
Tests for the topology utilities.
"""
import os
import pytest
import socket
from unittest.mock import patch, mock_open, MagicMock

from patio.topo.utils import get_worker_endpoint, _get_worker_ip_address, _get_worker_hostname, write_config_file, retry


@patch('patio.topo.utils.ROLE_INDEX', None)
@patch('patio.topo.utils._get_worker_ip_address')
def test_get_worker_endpoint_ip(mock_get_ip):
    """Test get_worker_endpoint returns IP when ROLE_INDEX is None."""
    mock_get_ip.return_value = "192.168.1.100"
    endpoint = get_worker_endpoint()
    assert endpoint == "192.168.1.100"


@patch('patio.topo.utils.ROLE_INDEX', "0")
@patch('patio.topo.utils.GROUP_NAME', "test-group")
@patch('patio.topo.utils.ROLE_NAME', "test-role")
def test_get_worker_endpoint_hostname():
    """Test get_worker_endpoint returns hostname when ROLE_INDEX is set."""
    endpoint = get_worker_endpoint()
    assert endpoint == "test-group-test-role-0.test-group-test-role"


@patch('patio.topo.utils.ROLE_INDEX', None)
@patch('patio.topo.utils.socket.socket')
def test_get_worker_ip_address_success(mock_socket_class):
    """Test _get_worker_ip_address when connection succeeds."""
    mock_socket = MagicMock()
    mock_socket_class.return_value = mock_socket
    mock_socket.getsockname.return_value = ("192.168.1.100", 12345)

    ip = _get_worker_ip_address("8.8.8.8:53")
    assert ip == "192.168.1.100"

    mock_socket.connect.assert_called_once_with(("8.8.8.8", 53))
    mock_socket.close.assert_called_once()


@patch('patio.topo.utils.GROUP_NAME', None)
@patch('patio.topo.utils.ROLE_NAME', None)
@patch('patio.topo.utils.ROLE_INDEX', "0")
def test_get_worker_hostname_missing_env():
    """Test _get_worker_hostname when environment variables are missing."""
    hostname = _get_worker_hostname()
    assert hostname == "unknown"


@patch('patio.topo.utils.GROUP_NAME', "test-group")
@patch('patio.topo.utils.ROLE_NAME', "test-role")
@patch('patio.topo.utils.ROLE_INDEX', "0")
def test_get_worker_hostname_success():
    """Test _get_worker_hostname when environment variables are set."""
    hostname = _get_worker_hostname()
    assert hostname == "test-group-test-role-0.test-group-test-role"


@patch('patio.topo.utils.yaml.dump')
@patch('patio.topo.utils.os.makedirs')
def test_write_config_file_yaml(mock_makedirs, mock_yaml_dump):
    """Test write_config_file with YAML format."""
    with patch('builtins.open', mock_open()) as mock_file:
        write_config_file("yaml", {"key": "value"}, "/test/path/config.yaml")

        mock_makedirs.assert_called_once_with("/test/path")
        mock_file.assert_called_once_with("/test/path/config.yaml", "w")
        mock_yaml_dump.assert_called_once_with({"key": "value"}, mock_file.return_value, default_flow_style=False)


@patch('patio.topo.utils.json.dump')
@patch('patio.topo.utils.os.makedirs')
def test_write_config_file_json(mock_makedirs, mock_json_dump):
    """Test write_config_file with JSON format."""
    with patch('builtins.open', mock_open()) as mock_file:
        write_config_file("json", {"key": "value"}, "/test/path/config.json")

        mock_makedirs.assert_called_once_with("/test/path")
        mock_file.assert_called_once_with("/test/path/config.json", "w")
        mock_json_dump.assert_called_once_with({"key": "value"}, mock_file.return_value, ensure_ascii=False, indent=4)


def test_write_config_file_invalid_type():
    """Test write_config_file with invalid file type."""
    # This should not raise an exception but should log a warning
    write_config_file("invalid", {"key": "value"})


def test_retry_success():
    """Test retry function when the function succeeds."""

    def test_func(x, y):
        return x + y

    result = retry(test_func, args=(2, 3), retry_times=3, interval=0.1)
    assert result == 5


def test_retry_failure():
    """Test retry function when the function fails all attempts."""

    def test_func():
        raise ValueError("Test error")

    with pytest.raises(ValueError, match="Test error"):
        retry(test_func, retry_times=3, interval=0.1)


def test_retry_partial_failure():
    """Test retry function when the function fails initially but succeeds later."""
    call_count = 0

    def test_func():
        nonlocal call_count
        call_count += 1
        if call_count < 3:
            raise ValueError("Test error")
        return "success"

    result = retry(test_func, retry_times=5, interval=0.1)
    assert result == "success"
    assert call_count == 3
