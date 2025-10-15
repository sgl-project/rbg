# -*- coding: utf-8 -*-
# @Author: zibai.gj

from fastapi import APIRouter
from prometheus_client import generate_latest, CONTENT_TYPE_LATEST, REGISTRY
from starlette.responses import JSONResponse, Response

from patio.logger import init_logger
from patio.metrics.metrics import patio_registry

server_router = APIRouter()
logger = init_logger(__name__)


@server_router.get("/")
async def root():
    return {"message": "Patio Server is running"}


@server_router.get("/metrics")
def metrics():
    """
    Returns the metrics for Prometheus.
    """
    data = generate_latest(patio_registry)
    return Response(content=data, media_type=CONTENT_TYPE_LATEST)


# Health check endpoint
@server_router.get("/health")
async def health():
    # for liveness check
    return JSONResponse(status_code=200, content={"status": "ok"})
