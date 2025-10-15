# -*- coding: utf-8 -*-
# @Author: zibai.gj

# -*- coding: utf-8 -*-
# @Author: zibai.gj
# @Time  : 2025-03-07

import os

from patio.config import DEFAULT_PATIO_PORT

# Inference Engine Config
INFERENCE_ENGINE = os.getenv("INFERENCE_ENGINE", "sglang")
INFERENCE_ENGINE_VERSION = os.getenv("INFERENCE_ENGINE_VERSION", "v0.5.3")
INFERENCE_ENGINE_ENDPOINT = os.getenv("INFERENCE_ENGINE_ENDPOINT")

# Prometheus
PROMETHEUS_MULTIPROC_DIR = os.getenv("PROMETHEUS_MULTIPROC_DIR", "/tmp/patio/metrics/")

# Standard Inference Engine Metrics
METRIC_SCRAPE_PATH = os.getenv("METRIC_SCRAPE_PATH", "/metrics")

# Topo
TOPO_TYPE = os.getenv("TOPO_TYPE")
TOPO_CONFIG_FILE = os.getenv("TOPO_CONFIG_FILE", "/etc/patio/instance-config.yaml")
SCHEDULER_ROLE_NAME = os.getenv("SCHEDULER_ROLE_NAME")
_topo_register_endpoint = os.getenv("TOPO_REGISTER_ENDPOINT")

# Patio config
HEALTH_CHECK_INTERVAL = os.getenv("HEALTH_CHECK_INTERVAL", 300)
HEARTBEAT_INTERVAL = os.getenv("HEARTBEAT_INTERVAL", 10)

# RBG Env
GROUP_NAME = os.getenv("GROUP_NAME")
ROLE_NAME = os.getenv("ROLE_NAME")
ROLE_INDEX = os.getenv("ROLE_INDEX")

LWS_GROUP_SIZE = os.getenv("LWS_GROUP_SIZE")


def get_topo_register_endpoint() -> str | None:
    if _topo_register_endpoint:
        return _topo_register_endpoint
    if SCHEDULER_ROLE_NAME and GROUP_NAME:
        return f"http://{GROUP_NAME}-{SCHEDULER_ROLE_NAME}-0.{GROUP_NAME}-{SCHEDULER_ROLE_NAME}:{DEFAULT_PATIO_PORT}"
    return None
