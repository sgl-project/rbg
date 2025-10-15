# -*- coding: utf-8 -*-
# @Author: zibai.gj
import argparse
import sys
import time
import traceback
from urllib.parse import urljoin
from prometheus_client import REGISTRY

from patio import envs
from patio.api.server_router import server_router
from patio.api.lora_router import lora_router
from patio.config import DEFAULT_PATIO_PORT, EXCLUDE_ENDPOINTS
from patio.logger import configure_logging, init_logger

from patio.metrics.engine_collector import EngineCollector
from patio.metrics.engine_metric_rules import get_metric_standard_rules
from patio.metrics.metrics import REQUEST_COUNT, REQUEST_LATENCY, patio_registry

from fastapi import FastAPI, Request
import uvicorn

app = FastAPI(title="Patio Server", debug=False)

# add router
app.include_router(server_router)
app.include_router(lora_router)


# Middleware to collect metrics
@app.middleware("http")
async def metrics_middleware(request: Request, call_next):
    method = request.method
    endpoint = request.scope.get("path")

    # skip metrics endpoints
    for exclude_endpoint in EXCLUDE_ENDPOINTS:
        if endpoint.startswith(exclude_endpoint):
            response = await call_next(request)
            return response

    start_time = time.perf_counter()
    response = await call_next(request)
    status = response.status_code
    duration = time.perf_counter() - start_time

    # Update metrics
    REQUEST_COUNT.labels(method=method, endpoint=endpoint, status=status).inc()
    REQUEST_LATENCY.labels(method=method, endpoint=endpoint).observe(duration)
    return response


def _init_metrics(scrape_metrics: bool):
    if scrape_metrics and envs.INFERENCE_ENGINE_ENDPOINT:
        scrape_endpoint = urljoin(envs.INFERENCE_ENGINE_ENDPOINT, envs.METRIC_SCRAPE_PATH)
        engine_collector = EngineCollector(scrape_endpoint, get_metric_standard_rules(envs.INFERENCE_ENGINE))
        patio_registry.register(engine_collector)


def main():
    parser = argparse.ArgumentParser(description="Run patio runtime server")
    parser.add_argument(
        "--enable-fastapi-docs",
        action="store_true",
        default=False,
        help="Enable FastAPI docs",
    )
    parser.add_argument("--host", type=str, default="0.0.0.0", help="Host to listen on")
    parser.add_argument("--port", type=int, default=DEFAULT_PATIO_PORT, help="Port to listen on")
    parser.add_argument("--instance-info", type=str, default="", help="instance info")
    parser.add_argument("--scrape-engine-metrics", type=bool, default=True, help="Enable to scrape engine metrics")
    parser.add_argument(
        "--log-level",
        type=str,
        default="INFO",
        choices=["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"],
        help="Set the logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)",
    )
    args = parser.parse_args()
    configure_logging(log_level=args.log_level)
    logger = init_logger(__name__)
    logger.info("Use %s to start up runtime server", args)

    _init_metrics(args.scrape_engine_metrics)

    # Run the server
    try:
        uvicorn.run(
            app=app,
            host=args.host,
            port=args.port,
            reload=False
        )
    except KeyboardInterrupt:
        logger.info("Server stopped by user")
    except Exception as e:
        logger.error(f"Failed to start server: {e}")
        traceback.print_exception(e)
        sys.exit(1)


if __name__ == "__main__":
    main()
