# -*- coding: utf-8 -*-
# @Author: zibai.gj
import argparse
import json
import signal
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

from patio.topo.client.base_topo_client import GroupTopoClient

from fastapi import FastAPI, Request
import uvicorn

from patio.topo.factory import create_topo_client

app = FastAPI(title="Patio Server", debug=False)

# add router
app.include_router(server_router)
app.include_router(lora_router)

topo_client: GroupTopoClient = None

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


def stop_topo_client_signal_handler(signal, frame):
    global topo_client
    topo_client.unregister()
    sys.exit(0)

def run_topo_client(worker_instance_info: str) -> GroupTopoClient:
    logger = init_logger(__name__)
    logger.info("Starting Patio TopoClient...")
    logger.info(f"worker instance info: {worker_instance_info}")

    worker_dict = None
    try:
        worker_dict = json.loads(worker_instance_info)
    except json.decoder.JSONDecodeError as e:
        logger.error(f"Failed to decode worker instance info: {worker_instance_info}: {e}")
        sys.exit(1)

    if not worker_dict.get("topo_type"):
        topo_type = envs.TOPO_TYPE
        if not topo_type:
            logger.error(f"No topo_type defined for worker instance, either --instance-info or env variable {envs.TOPO_TYPE} should be set")
            sys.exit(1)
        logger.info(f"Found topo type from env variable {envs.TOPO_TYPE}: {topo_type}")
        worker_dict["topo_type"] = topo_type

    try:
        global topo_client
        worker_info = worker_dict["data"]
        if worker_info is None:
            logger.error(f"No worker info found for worker instance info, please set \"data\" field in instance info")
        topo_client = create_topo_client(worker_dict["topo_type"], worker_info)
        topo_client.wait_engine_ready(worker_info)
        topo_client.register("", worker_info)
        signal.signal(signal.SIGTERM, stop_topo_client_signal_handler)
        signal.signal(signal.SIGINT, stop_topo_client_signal_handler)

        return topo_client
    except Exception as e:
        logger.error(f"Failed to start Patio TopoClient: {e}")
        sys.exit(1)


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

    if args.instance_info:
        topo_client = run_topo_client(args.instance_info)
    else:
        topo_client = None

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
