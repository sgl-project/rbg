# -*- coding: utf-8 -*-
# @Author: zibai.gj
import traceback
from http import HTTPStatus
from typing import Union, Optional
from urllib.parse import urljoin

import httpx

from patio.api.protocol import LoadLoraAdapterRequest, ErrorResponse, UnLoadLoraAdapterRequest
from patio.api.response_error import server_error
from patio.engine.base import InferenceEngine
from patio.logger import init_logger

logger = init_logger(__name__)


class VLLMInferenceEngine(InferenceEngine):
    def __init__(
            self, name: str, version: str, endpoint: str, api_key: Optional[str] = None):
        if api_key is not None:
            headers = {"Authorization": f"Bearer {api_key}"}
        else:
            headers = {}
        self.client = httpx.AsyncClient(headers=headers)
        super().__init__(name, version, endpoint)

    async def load_lora_adapter(
            self, request: LoadLoraAdapterRequest
    ) -> Union[ErrorResponse, str]:
        load_url = urljoin(self.endpoint, "/v1/load_lora_adapter")
        lora_name, lora_path = request.lora_name, request.lora_path
        try:
            # Load LoRA timeout 30 mins
            response = await self.client.post(
                load_url, json=request.model_dump(), headers=self.headers, timeout=30 * 60,
            )
        except Exception as e:
            logger.error(
                f"Failed to load LoRA adapter '{lora_name}' from '{lora_path}' \
             for http request failed: {e}"
            )
            traceback.print_exception(e)
            return server_error(f"Failed to load LoRA adapter '{lora_name}'")

        if response.status_code != HTTPStatus.OK:
            logger.error(
                f"Failed to load LoRA adapter '{lora_name}' from '{lora_path}' \
                 with error: {response.text}, status_code: {response.status_code}"
            )
            return server_error(
                message=f"Failed to load LoRA adapter '{lora_name}', message: {response.text}",
                code=HTTPStatus(value=response.status_code),
            )
        return f"Success: LoRA adapter '{lora_name}' added successfully."

    async def unload_lora_adapter(
            self, request: UnLoadLoraAdapterRequest
    ) -> Union[ErrorResponse, str]:
        unload_url = urljoin(self.endpoint, "/v1/unload_lora_adapter")
        lora_name = request.lora_name
        try:
            response = await self.client.post(
                unload_url, json=request.model_dump(), headers=self.headers
            )
        except Exception as e:
            logger.error(
                f"Failed to unload LoRA adapter '{lora_name}' \
             for http request failed: {e}"
            )
            traceback.print_exception(e)
            return server_error(f"Failed to unload LoRA adapter '{lora_name}'")

        if response.status_code != HTTPStatus.OK:
            logger.error(
                f"Failed to unload LoRA adapter '{lora_name}' \
                 with error: {response.text}, status_code: {response.status_code}"
            )
            return server_error(
                message=f"Failed to unload LoRA adapter '{lora_name}', message: {response.text}",
                code=HTTPStatus(value=response.status_code),
            )
        return f"Success: LoRA adapter '{lora_name}' unloaded successfully."
