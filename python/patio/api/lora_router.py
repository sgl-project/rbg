# -*- coding: utf-8 -*-
# @Author: zibai.gj

from fastapi import APIRouter, Request
from starlette.responses import JSONResponse

from patio.api.protocol import LoadLoraAdapterRequest, ErrorResponse, UnLoadLoraAdapterRequest
from patio.engine.base import inference_engine
from patio.api.response_error import invalid_error

lora_router = APIRouter()


@lora_router.post("/load_lora_adapter")
async def load_lora_adapter(request: LoadLoraAdapterRequest, raw_request: Request):
    """Load a new LoRA adapter without re-launching the server."""
    try:
        engine = inference_engine()
    except Exception as e:
        response = invalid_error(message=str(e))
        return JSONResponse(content=response.model_dump(), status_code=response.code)

    response = await engine.load_lora_adapter(request)
    if isinstance(response, ErrorResponse):
        return JSONResponse(content=response.model_dump(), status_code=response.code)
    return JSONResponse(status_code=200, content=response)


@lora_router.post("/unload_lora_adapter")
async def unload_lora_adapter(request: UnLoadLoraAdapterRequest, raw_request: Request):
    """Unload a new LoRA adapter without re-launching the server."""
    try:
        engine = inference_engine()
    except Exception as e:
        response = invalid_error(message=str(e))
        return JSONResponse(content=response.model_dump(), status_code=response.code)

    response = await engine.unload_lora_adapter(request)
    if isinstance(response, ErrorResponse):
        return JSONResponse(content=response.model_dump(), status_code=response.code)
    return JSONResponse(status_code=200, content=response)
