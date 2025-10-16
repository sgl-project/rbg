# -*- coding: utf-8 -*-
# @Author: zibai.gj
# @Time  : 2025-03-07
from abc import ABC
from dataclasses import dataclass
from http import HTTPStatus
from typing import Union, Optional

import requests
from urllib.parse import urlparse

from patio import envs
from patio.api.protocol import ErrorResponse, LoadLoraAdapterRequest, UnLoadLoraAdapterRequest
from patio.logger import init_logger
from patio.api.response_error import not_implemented_error

logger = init_logger(__name__)


@dataclass
class InferenceEngine(ABC):
    name: str
    version: str
    endpoint: str
    headers: Optional[dict] = None

    async def ready(self) -> bool:
        return _is_url_ready(self.endpoint) if self.endpoint else True

    async def load_lora_adapter(
            self, request: LoadLoraAdapterRequest
    ) -> Union[ErrorResponse, str]:
        return not_implemented_error(
            f"Inference engine {self.name} with version {self.version} not support load lora adapter")

    async def unload_lora_adapter(
            self, request: UnLoadLoraAdapterRequest
    ) -> Union[ErrorResponse, str]:
        return not_implemented_error(
            f"Inference engine {self.name} with version {self.version} not support unload lora adapter")


def inference_engine() -> InferenceEngine:
    engine = envs.INFERENCE_ENGINE
    version = envs.INFERENCE_ENGINE_VERSION
    endpoint = envs.INFERENCE_ENGINE_ENDPOINT

    if not engine or not version or not endpoint:
        raise ValueError("Inference engine or version or endpoint is not set")

    # TODO: check engine supported version

    if engine.lower() == "vllm":
        from patio.engine.vllm_engine import VLLMInferenceEngine
        return VLLMInferenceEngine(engine, version, endpoint)
    elif engine.lower() == "sglang":
        from patio.engine.sglang_engine import SGLangInferenceEngine
        return SGLangInferenceEngine(engine, version, endpoint)
    else:
        raise ValueError(f"Inference engine {engine} with version {version} not support")


def _is_url_ready(url: str) -> bool:
    """
    Checks if a given URL is correctly formatted and if the endpoint is reachable.

    Args:
        url (str): The URL to check.

    Returns:
        bool: True if the URL is correctly formatted and a connection can be established
              (even if it returns a 4xx/5xx HTTP status code, as that means the server
              is still responding).
              False otherwise (e.g., malformed URL, connection refused, timeout).
    """
    if not isinstance(url, str):
        logger.warning("Invalid input type: INFERENCE_ENGINE_ENDPOINT must be a string.")
        return False

    # 1. Check URL format
    try:
        parsed_url = urlparse(url)
        # A valid URL should have at least a scheme (http, https) and a network location (domain.com)
        if not all([parsed_url.scheme, parsed_url.netloc]):
            logger.warning(f"INFERENCE_ENGINE_ENDPOINT Invalid URL format: missing scheme or netloc for '{url}'")
            return False
    except Exception as e:
        logger.warning(f"INFERENCE_ENGINE_ENDPOINT Invalid URL format: parsing error for '{url}' - {e}")
        return False

    # 2. Curl the endpoint (make an HTTP GET request)
    try:
        _ = requests.get(url, timeout=5)
        # Even if a 4xx or 5xx status is returned, the server *is* responding,
        # so we consider it "ready" in this context. raise_for_status() would
        # typically raise an HTTPError, but we want to catch that and still return True.
        # So we can remove raise_for_status() if we only care about connection status.
        # If you *do* want to consider 4xx/5xx as "not ready", uncomment the next line
        # and modify the HTTPError handler to return False.
        # response.raise_for_status() # If uncommented, move HTTPError handling here

        # If we reached here, it means a connection was established and we got a response.
        return True

    except requests.exceptions.ConnectionError as e:
        logger.warning(f"Connection Error for '{url}': {e}")
        return False
    except requests.exceptions.Timeout:
        logger.warning(f"Request timed out for '{url}'")
        return False
    except requests.exceptions.HTTPError as e:
        # This means the server responded with a 4xx or 5xx status code.
        # For the purpose of "is the URL ready (can we connect)?", the server *is*
        # responding. If you want 4xx/5xx to mean "not ready", change this to return False.
        logger.info(f"Server responded with HTTP error {e.response.status_code} for '{url}', but is reachable.")
        return True
    except requests.exceptions.RequestException as e:
        # This catches all other requests-related exceptions (e.g., TooManyRedirects, InvalidSchema)
        logger.warning(f"Request failed for '{url}': {e}")
        return False
    except Exception as e:
        # Catch any other unexpected errors
        logger.error(f"An unexpected error occurred while checking '{url}': {e}")
        return False
