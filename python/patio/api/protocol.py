# -*- coding: utf-8 -*-
# @Author: zibai.gj
from typing import Optional, Dict, Any

from pydantic import BaseModel, ConfigDict, Field
from pydantic.v1 import root_validator

from patio import envs
from patio.config import SUPPORTED_TOPO_TYPES


class NoExtraBaseModel(BaseModel):
    """
    pydantic BaseModel with extra forbidden
    """

    model_config = ConfigDict(extra="forbid")


class ErrorResponse(NoExtraBaseModel):
    code: int
    type: str
    message: str
    object: str = "error"
    param: Optional[str] = None


# ============== Topo
class TopoTypeValidationMixin:
    topo_type: str = Field(..., description="Type of inference engine, e.g., 'SGLang'.")

    @root_validator(pre=True)
    def validate_topo_type(cls, values):
        topo_type = values.get('topo_type')
        if not topo_type:
            topo_type = envs.TOPO_TYPE
            if not topo_type:
                raise ValueError(
                    "topo_type is not set. Please provide it in the request or set the TOPO_TYPE environment variable.")
            values['topo_type'] = topo_type
        if topo_type not in SUPPORTED_TOPO_TYPES:
            raise ValueError(
                f"Invalid topo_type: '{topo_type}'. Supported values are: {', '.join(SUPPORTED_TOPO_TYPES)}.")

        return values


class TopoRegisterRequest(NoExtraBaseModel, TopoTypeValidationMixin):
    worker_info: Dict[str, Any] = Field(..., description="Dictionary containing worker-specific information.")

    @root_validator
    def validate_worker_info_endpoint(cls, values):
        worker_info = values.get('worker_info')

        if not isinstance(worker_info, dict):
            raise ValueError("worker_info must be a dictionary")
        if "endpoint" not in worker_info:
            raise ValueError("'worker_info' must contain an 'endpoint' key.")
        if not isinstance(worker_info["endpoint"], str) or not worker_info["endpoint"]:
            raise ValueError("'endpoint' in worker_info must be a non-empty string.")

        return values


class TopoUnRegisterRequest(NoExtraBaseModel, TopoTypeValidationMixin):
    endpoint: str = Field(..., description="Worker endpoint, must be a non-empty string.")

    @root_validator
    def validate_worker_info_endpoint(cls, values):
        endpoint = values.get('endpoint')

        if not isinstance(endpoint, str) or not endpoint:
            raise ValueError("endpoint must be a non-empty string.")

        return values


class TopoInfoRequest(NoExtraBaseModel, TopoTypeValidationMixin):
    """
       A request model that only requires a valid 'topo_type'.

       The 'topo_type' field and its validation logic are fully inherited
       from the TopoTypeValidationMixin. No additional fields or logic
       are needed in this class.
       """
    pass


# ============== Lora
class LoadLoraAdapterRequest(NoExtraBaseModel):
    lora_name: str
    lora_path: str


class UnLoadLoraAdapterRequest(NoExtraBaseModel):
    lora_name: str
